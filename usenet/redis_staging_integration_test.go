//go:build integration

package usenet

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedis dials the throwaway Redis named by USENET_TEST_REDIS and flushes
// it. Mirrors the pg integration convention (USENET_TEST_DSN): a real server,
// because the whole point of these cases is Redis' own type semantics —
// WRONGTYPE, LTrim's delete-when-empty, SUnionStore — which a fake would only
// restate as the assumptions being tested.
func testRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("USENET_TEST_REDIS")
	if addr == "" {
		t.Skip("USENET_TEST_REDIS not set; skipping. Set it to host:port of a throwaway Redis.")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping %s: %v", addr, err)
	}
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func newTestStaging(rdb redis.UniversalClient) *redisStaging {
	return newRedisStaging(rdb, func(context.Context) int { return 2 }, nil, nil)
}

// newTestStagingReporting is newTestStaging plus a capture of everything the
// backend reports, for the cases that must NOT drop entries silently.
func newTestStagingReporting(rdb redis.UniversalClient) (*redisStaging, *[]error) {
	var notes []error
	r := newRedisStaging(rdb, func(context.Context) int { return 2 }, nil,
		func(_ context.Context, _ string, err error) { notes = append(notes, err) })
	return r, &notes
}

// seedLegacy pushes ready entries onto the legacy LIST and, for the live ones,
// creates the grp: metadata hash the migration checks. Live/dead is what
// decides whether an entry survives, so a fixture without grp: keys would be
// asserting the wrong thing.
func seedLegacy(t *testing.T, rdb redis.UniversalClient, entries []string, live bool) {
	t.Helper()
	ctx := context.Background()
	members := make([]interface{}, 0, len(entries))
	for _, e := range entries {
		members = append(members, e)
		if live {
			if err := rdb.HSet(ctx, "grp:"+e, "base_subject", "s "+e).Err(); err != nil {
				t.Fatalf("seed grp meta: %v", err)
			}
		}
	}
	// LPush, like the pre-2026-07 writer: newest at the head.
	for i := 0; i < len(members); i += 5000 {
		end := i + 5000
		if end > len(members) {
			end = len(members)
		}
		if err := rdb.LPush(ctx, readyKey, members[i:end]...).Err(); err != nil {
			t.Fatalf("seed list: %v", err)
		}
	}
}

func TestEnsureReadySet_ConvertsLegacyList(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	want := map[string]bool{}
	entries := make([]string, 0, 250)
	for i := 0; i < 250; i++ {
		m := fmt.Sprintf("alt.binaries.test:%04x", i)
		entries = append(entries, m)
		want[m] = true
	}
	seedLegacy(t, rdb, entries, true)

	r.ensureReadySet(ctx)

	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after conversion = %q, want set", typ)
	}
	got, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("converted %d members, want %d", len(got), len(want))
	}
	for _, m := range got {
		if !want[m] {
			t.Fatalf("unexpected member %q", m)
		}
	}
	if n, _ := rdb.Exists(ctx, readyConvKey).Result(); n != 0 {
		t.Error("conversion staging key left behind")
	}
}

func TestEnsureReadySet_LargeListConvertsAcrossCalls(t *testing.T) {
	// The prod stall: one whole-list LRange + one giant SAdd timed out
	// identically on every attempt, so the key stayed a LIST forever. A backlog
	// past the per-call window cap must still converge, one call at a time.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	total := readyConvWindow*readyConvMaxWindows + 2500
	entries := make([]string, 0, total)
	for i := 0; i < total; i++ {
		entries = append(entries, fmt.Sprintf("g:%06x", i))
	}
	seedLegacy(t, rdb, entries, true)

	// First pass is capped and must leave the rest queued, not drop it.
	r.ensureReadySet(ctx)
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "list" {
		t.Fatalf("type after capped pass = %q, want list (conversion should not have finished)", typ)
	}
	if n, _ := rdb.LLen(ctx, readyKey).Result(); n != int64(total-readyConvWindow*readyConvMaxWindows) {
		t.Fatalf("remaining list len = %d, want %d", n, total-readyConvWindow*readyConvMaxWindows)
	}

	for i := 0; i < 5; i++ {
		r.ensureReadySet(ctx)
		if typ, _ := rdb.Type(ctx, readyKey).Result(); typ == "set" {
			break
		}
	}
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after resumed passes = %q, want set", typ)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != int64(total) {
		t.Fatalf("converted %d members, want %d — a window was dropped", n, total)
	}
}

func TestEnsureReadySet_DropsDeadEntriesKeepsLive(t *testing.T) {
	// Prod's key held 7.3M entries — the fossil of the O(len) LRem death spiral
	// this migration exists to end, nearly all of it pointing at staging data
	// that had long since TTL'd out. Importing that wholesale would swap a hard
	// stall for a soft one (the builder samples 500 a pass and HGETs each,
	// indexing nothing), so entries whose grp: metadata is gone are discarded —
	// by liveness, never by position, and never silently.
	ctx := context.Background()
	rdb := testRedis(t)
	r, notes := newTestStagingReporting(rdb)

	dead := make([]string, 0, 1500)
	for i := 0; i < 1500; i++ {
		dead = append(dead, fmt.Sprintf("dead.group:%05x", i))
	}
	live := []string{"live.group:aaaa", "live.group:bbbb", "live.group:cccc"}
	seedLegacy(t, rdb, dead, false)
	seedLegacy(t, rdb, live, true)

	r.ensureReadySet(ctx)

	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type = %q, want set", typ)
	}
	got, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(got) != len(live) {
		t.Fatalf("kept %d members, want only the %d live ones: %v", len(got), len(live), got)
	}
	for _, m := range got {
		if !strings.HasPrefix(m, "live.") {
			t.Fatalf("kept dead entry %q — liveness is what decides, not position", m)
		}
	}
	if len(*notes) != 1 {
		t.Fatalf("reported %d notes, want exactly 1 — a discarded backlog must never be silent", len(*notes))
	}
	if msg := (*notes)[0].Error(); !strings.Contains(msg, fmt.Sprint(len(dead))) {
		t.Errorf("note %q does not say how many entries were discarded (%d)", msg, len(dead))
	}
}

func TestEnsureReadySet_ConcurrentConvertersLoseNothing(t *testing.T) {
	// nzb:ready is one fleet-wide key with no lease, and convMu only serializes
	// ONE process — crawl.go splits groups across N crawler workers, each with
	// its own redisStaging. A read-then-trim pair let a second converter LTrim
	// (positional!) a window it never copied: measured at 12-15k completed
	// releases destroyed per overlapped pass on a 40k list. The window must be
	// atomic, so two converters racing must still lose nothing.
	ctx := context.Background()
	rdb := testRedis(t)

	const total = 40000
	entries := make([]string, 0, total)
	for i := 0; i < total; i++ {
		entries = append(entries, fmt.Sprintf("g:%06x", i))
	}
	seedLegacy(t, rdb, entries, true)

	a, b := newTestStaging(rdb), newTestStaging(rdb)
	for pass := 0; pass < 10; pass++ {
		var wg sync.WaitGroup
		for _, r := range []*redisStaging{a, b} {
			wg.Add(1)
			go func(r *redisStaging) { defer wg.Done(); r.ensureReadySet(ctx) }(r)
		}
		wg.Wait()
		if typ, _ := rdb.Type(ctx, readyKey).Result(); typ == "set" {
			break
		}
	}

	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type = %q, want set", typ)
	}
	n, _ := rdb.SCard(ctx, readyKey).Result()
	if n != total {
		t.Fatalf("migrated %d of %d entries — %d completed releases destroyed by the converter race", n, total, total-n)
	}
	if left, _ := rdb.Exists(ctx, readyConvKey).Result(); left != 0 {
		t.Error("conversion staging key left behind")
	}
}

func TestEnsureReadySet_PreservesConcurrentlyQueuedEntries(t *testing.T) {
	// A partial conversion plus live entries in the real set: folding the
	// staged set in must UNION, not RENAME over the top.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	if err := rdb.SAdd(ctx, readyConvKey, "legacy:aaa", "legacy:bbb").Err(); err != nil {
		t.Fatalf("seed staged: %v", err)
	}
	if err := rdb.SAdd(ctx, readyKey, "live:ccc").Err(); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	r.ensureReadySet(ctx)

	got, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("members = %v, want the 2 migrated + 1 live entry", got)
	}
}

func TestStageArticles_SelfHealsWhenReadyBecomesList(t *testing.T) {
	// The 23h prod failure mode, driven through the real entry point: convDone
	// latches true on a healthy key, the key later turns into a LIST, and every
	// SAdd then errors WRONGTYPE for the life of the container — which is why
	// redeploying didn't fix it. Asserting on stageArticles rather than on
	// readyRetry is the point: the wiring is what failed, not the helper.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	r.ensureReadySet(ctx) // key absent -> latches convDone
	r.convMu.Lock()
	latched := r.convDone
	r.convMu.Unlock()
	if !latched {
		t.Fatal("convDone should latch when the key is absent")
	}

	// A legacy list appears behind the latch. Give it live metadata so the
	// migration keeps it and the assertion below can see both entries.
	seedLegacy(t, rdb, []string{"stale.group:legacy"}, true)

	// One single-part article completes its set, so stageArticles reaches the
	// SAdd on nzb:ready — the exact call that errored for 23 hours.
	n, err := r.stageArticles(ctx, []stagedArticle{{
		Group: "alt.binaries.test", BaseSubject: "Fresh Release", MessageID: "<a@b>",
		Subject: "Fresh Release (1/1)", PartNum: 1, TotalParts: 1, FileNum: 1, TotalFiles: 1,
		Posted: time.Now(),
	}})
	if err != nil {
		t.Fatalf("stageArticles did not recover from WRONGTYPE: %v", err)
	}
	if n != 1 {
		t.Fatalf("staged %d articles, want 1", n)
	}
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after self-heal = %q, want set", typ)
	}
	members, _ := rdb.SMembers(ctx, readyKey).Result()
	if len(members) != 2 {
		t.Fatalf("members = %v, want the migrated legacy entry plus the freshly completed set", members)
	}
}

// The reaper is the fix for a queue that became a lottery: production held
// 7,403,408 entries against a 500-entry random draw, 407 of every 500 draws
// already dead. Nothing removed the dead except that same draw, so the queue
// diluted itself faster than it could be cleared.
func TestReapReadyQueueRemovesOnlyTheDead(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	// 40 live entries (metadata present) and 160 fossils (metadata gone) — the
	// production ratio, roughly.
	live := map[string]bool{}
	for i := 0; i < 40; i++ {
		e := fmt.Sprintf("a.b.group:live%02d", i)
		if err := rdb.HSet(ctx, "grp:"+e, "base_subject", "Release "+e).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.SAdd(ctx, readyKey, e).Err(); err != nil {
			t.Fatal(err)
		}
		live[e] = true
	}
	for i := 0; i < 160; i++ {
		if err := rdb.SAdd(ctx, readyKey, fmt.Sprintf("a.b.group:dead%03d", i)).Err(); err != nil {
			t.Fatal(err)
		}
	}

	scanned, removed, err := r.reapReadyQueue(ctx, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 160 {
		t.Errorf("removed %d, want 160 dead entries", removed)
	}
	if scanned < 200 {
		t.Errorf("scanned %d, want a full circuit of 200", scanned)
	}

	rest, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 40 {
		t.Fatalf("queue holds %d after the sweep, want the 40 live entries", len(rest))
	}
	for _, e := range rest {
		if !live[e] {
			t.Errorf("sweep kept a fossil, or invented an entry: %q", e)
		}
	}

	// A completed release must survive the sweep and still be drawable — the
	// reaper exists to make the draw effective, so destroying live work would
	// be strictly worse than the problem.
	keys, stats, err := r.candidateGroups(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Fossil != 0 {
		t.Errorf("draw after the sweep still hit %d fossils", stats.Fossil)
	}
	if len(keys) != 40 {
		t.Errorf("drew %d live candidates, want 40", len(keys))
	}
}

// Bounded per call, so a multi-million-entry queue is worked down over passes
// rather than stalling the pass that runs it. Progress is durable because
// removal is idempotent.
func TestReapReadyQueueIsBounded(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	for i := 0; i < 3000; i++ {
		if err := rdb.SAdd(ctx, readyKey, fmt.Sprintf("a.b.group:dead%05d", i)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	scanned, removed, err := r.reapReadyQueue(ctx, 600)
	if err != nil {
		t.Fatal(err)
	}
	if scanned > 1600 {
		t.Errorf("scanned %d against a 600 budget — the sweep is unbounded and "+
			"would stall the build pass on a large queue", scanned)
	}
	if removed == 0 {
		t.Error("a bounded sweep must still make progress")
	}
	left, _ := rdb.SCard(ctx, readyKey).Result()
	if left == 0 {
		t.Error("a bounded sweep cleared everything; the bound is not applied")
	}

	// Successive passes finish the job.
	for i := 0; i < 20; i++ {
		if _, _, err := r.reapReadyQueue(ctx, 600); err != nil {
			t.Fatal(err)
		}
		if n, _ := rdb.SCard(ctx, readyKey).Result(); n == 0 {
			return
		}
	}
	n, _ := rdb.SCard(ctx, readyKey).Result()
	t.Errorf("queue still holds %d after 20 bounded sweeps — it never converges", n)
}

// An empty or absent queue must be a cheap no-op, not an error: the reaper runs
// on every build pass including the ones with nothing to do.
func TestReapReadyQueueEmptyIsHarmless(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	scanned, removed, err := r.reapReadyQueue(ctx, 1000)
	if err != nil || scanned != 0 || removed != 0 {
		t.Errorf("empty sweep: scanned=%d removed=%d err=%v", scanned, removed, err)
	}
	if _, _, err := r.reapReadyQueue(ctx, 0); err != nil {
		t.Errorf("zero budget must be a no-op, got %v", err)
	}
}

// stagingInfo must report memory and eviction on EVERY supported Redis, not
// just recent ones. Multi-section INFO ("INFO memory stats") needs Redis 7.0;
// against 6.x it yields nothing usable and every field silently reads zero —
// which is indistinguishable from a healthy, unbounded, never-evicting server.
// Production reported exactly that while its own back-pressure gate, using a
// single-section call, read 99% off the same instance.
//
// Point USENET_TEST_REDIS at a 6.x server to exercise the case that regressed.
func TestStagingInfoReadsMemoryAndEvictions(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	// Give the server a ceiling so maxmemory is non-zero and a policy exists.
	if err := rdb.ConfigSet(ctx, "maxmemory", "64mb").Err(); err != nil {
		t.Skipf("cannot set maxmemory on this server: %v", err)
	}
	t.Cleanup(func() { _ = rdb.ConfigSet(ctx, "maxmemory", "0").Err() })
	if err := rdb.ConfigSet(ctx, "maxmemory-policy", "allkeys-lru").Err(); err != nil {
		t.Skipf("cannot set maxmemory-policy: %v", err)
	}
	t.Cleanup(func() { _ = rdb.ConfigSet(ctx, "maxmemory-policy", "noeviction").Err() })

	got, err := r.stagingInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemMaxBytes != 64*1024*1024 {
		t.Errorf("MemMaxBytes = %d, want 67108864 — a zero here reads as "+
			"'unbounded', the opposite of a server at its ceiling", got.MemMaxBytes)
	}
	if got.MemUsedBytes <= 0 {
		t.Errorf("MemUsedBytes = %d, want > 0", got.MemUsedBytes)
	}
	if got.MaxMemoryPolicy != "allkeys-lru" {
		t.Errorf("MaxMemoryPolicy = %q, want allkeys-lru", got.MaxMemoryPolicy)
	}
	if !got.EvictionRisk() {
		t.Error("allkeys-lru must read as an eviction risk")
	}
	// evicted_keys/expired_keys come from the OTHER INFO section. Zero is a
	// legitimate value on a fresh server, so assert the section was reachable
	// rather than the count: a broken call leaves the policy empty too, which
	// the check above already catches.
	if got.ExpiredKeys < 0 || got.EvictedKeys < 0 {
		t.Errorf("negative counters: evicted=%d expired=%d", got.EvictedKeys, got.ExpiredKeys)
	}
}

// The forming-releases readout must cost the same whether ten sets are in
// flight or three million. It used SMEMBERS, which returns every hash in the
// group, and then issued a pipelined HGetAll+HLen per hash — production held
// ~3.5M staged sets, so a readout ran millions of commands per build pass
// against a Redis already at its memory ceiling, starving the expiry cycle that
// was supposed to be draining the backlog.
func TestIncompleteSetsCostIsBounded(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	// Far more staged sets than the sample cap.
	const n = pendingSampleCap * 3
	pipe := rdb.Pipeline()
	for i := 0; i < n; i++ {
		hash := fmt.Sprintf("h%06d", i)
		pipe.SAdd(ctx, activeKey("a.b.group"), hash)
		pipe.HSet(ctx, grpKey("a.b.group", hash),
			"base_subject", fmt.Sprintf("Release %06d", i),
			"file_parts", "0", "total_parts", "500")
		pipe.HSet(ctx, artKey("a.b.group", hash), formatFieldKey(0, 1), "x")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}

	sets, err := r.incompleteSets(ctx, 15, []string{"a.b.group"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 15 {
		t.Errorf("returned %d sets, want the requested 15", len(sets))
	}
	// Every returned row must be real, not a sampling artefact.
	for _, s := range sets {
		if s.Base == "" || s.Need <= 0 {
			t.Errorf("bogus row: %+v", s)
		}
		if s.Have >= s.Need {
			t.Errorf("a COMPLETE set appeared in the incomplete list: %+v", s)
		}
	}
}

// Sampling must not break the small case: with fewer sets than the cap, the
// readout still sees all of them and still picks the largest shortfalls.
func TestIncompleteSetsExactBelowTheCap(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	// Five sets with deliberately different shortfalls.
	for i, need := range []int{100, 900, 300, 700, 500} {
		hash := fmt.Sprintf("h%02d", i)
		if err := rdb.SAdd(ctx, activeKey("a.b.group"), hash).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.HSet(ctx, grpKey("a.b.group", hash),
			"base_subject", fmt.Sprintf("Release %d", need),
			"file_parts", "0", "total_parts", need).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.HSet(ctx, artKey("a.b.group", hash), formatFieldKey(0, 1), "x").Err(); err != nil {
			t.Fatal(err)
		}
	}
	sets, err := r.incompleteSets(ctx, 15, []string{"a.b.group"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 5 {
		t.Fatalf("got %d sets, want all 5", len(sets))
	}
	// Sorted by shortfall, largest first.
	if sets[0].Need != 900 {
		t.Errorf("largest shortfall first: got need=%d, want 900", sets[0].Need)
	}
}

// The span has to survive out-of-order arrival. Batches land across many
// parallel connections and the backfill walks DESCENDING, so a set's lowest
// article number is frequently seen after its highest — a naive
// first-write-wins would record the wrong bounds and make a normal release look
// like a collision.
func TestSpanFoldKeepsTrueBounds(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	base := "[Group] A Real Release - 01"
	stage := func(nums ...int) {
		t.Helper()
		arts := make([]stagedArticle, 0, len(nums))
		for i, n := range nums {
			arts = append(arts, stagedArticle{
				ArticleNum: n,
				MessageID:  fmt.Sprintf("<%d.%d@example>", n, i),
				Subject:    base + ".mkv", BaseSubject: base,
				Group: "a.b.group", PartNum: i + 1, TotalParts: 50, SegTotal: 50,
			})
		}
		if _, err := r.stageArticles(ctx, arts); err != nil {
			t.Fatal(err)
		}
	}

	// Middle first, then higher, then LOWER than anything seen.
	stage(5000, 5001)
	stage(5900)
	stage(4100)

	meta, err := rdb.HGetAll(ctx, grpKey("a.b.group", groupHashKey("a.b.group", base))).Result()
	if err != nil {
		t.Fatal(err)
	}
	if meta["art_lo"] != "4100" {
		t.Errorf("art_lo = %q, want 4100 — a later batch with a LOWER number must "+
			"lower the bound, or descending backfill records the wrong span", meta["art_lo"])
	}
	if meta["art_hi"] != "5900" {
		t.Errorf("art_hi = %q, want 5900", meta["art_hi"])
	}

	// And it reaches the readout as a sane, non-colliding span.
	sets, err := r.incompleteSets(ctx, 15, []string{"a.b.group"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range sets {
		if s.Base != base {
			continue
		}
		found = true
		if s.Span() != 1801 {
			t.Errorf("span = %d, want 1801", s.Span())
		}
		if s.Collided() {
			t.Error("a genuine 4-article release read as a base collision")
		}
	}
	if !found {
		t.Fatal("the set never reached the forming-releases readout")
	}
}

// The shape this was built to find: many unrelated posts sharing one generic
// base subject, so the set spans a swathe of the group and can never complete.
func TestSpanExposesABaseCollision(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	base := "}WT Tube" // the real shape: a base short enough to merge everything
	for i, n := range []int{1_000_000, 1_400_000, 2_100_000, 2_900_000} {
		if _, err := r.stageArticles(ctx, []stagedArticle{{
			ArticleNum: n,
			MessageID:  fmt.Sprintf("<c%d@example>", n),
			Subject:    base + " something.mkv", BaseSubject: base,
			Group: "a.b.group", PartNum: i + 1, TotalParts: 900, SegTotal: 900,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	sets, err := r.incompleteSets(ctx, 15, []string{"a.b.group"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sets {
		if s.Base != base {
			continue
		}
		if !s.Collided() {
			t.Errorf("4 articles spanning %d article numbers did not read as a "+
				"collision — this set can never complete and nothing else says so",
				s.Span())
		}
		return
	}
	t.Fatal("the collided set never reached the readout")
}

// The reaper's cursor must survive across calls. SSCAN from 0 walks the same
// deterministic bucket order every time, so when the queue is deeper than the
// per-pass budget a restarting reaper re-verifies the same head window forever
// and never examines the millions of entries beyond it — the production queue
// shape (7.4M entries, ~20% live) jammed exactly this way: the live survivors
// filled the window and the sweep removed nothing while fossils beyond it
// diluted the draw. TestReapReadyQueueIsBounded cannot see this because its
// fixture is 100% dead — removal itself drives the window forward.
func TestReapReadyQueueResumesAcrossCalls(t *testing.T) {
	rdb := testRedis(t)
	r := newTestStaging(rdb)
	ctx := context.Background()

	// 1,000 live entries (metadata present) and 200 dead, against a 500-entry
	// budget: most of the queue is beyond any single call's window, and the
	// live entries never leave, so only a persisted cursor can carry the sweep
	// past them.
	live := 1000
	for i := 0; i < live; i++ {
		e := fmt.Sprintf("a.b.group:live%04d", i)
		if err := rdb.HSet(ctx, "grp:"+e, "base_subject", "Release "+e).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.SAdd(ctx, readyKey, e).Err(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		if err := rdb.SAdd(ctx, readyKey, fmt.Sprintf("a.b.group:dead%04d", i)).Err(); err != nil {
			t.Fatal(err)
		}
	}

	// Six budgeted calls cover the 1,200-entry circuit at least twice with the
	// cursor persisted; a reaper restarting at 0 spends every call on the same
	// mostly-live prefix and leaves dead entries beyond it untouched.
	removed := 0
	for i := 0; i < 6; i++ {
		_, rm, err := r.reapReadyQueue(ctx, 500)
		if err != nil {
			t.Fatal(err)
		}
		removed += rm
	}
	if removed != 200 {
		t.Errorf("removed %d of 200 dead entries across six budgeted calls — "+
			"the sweep is not resuming past the live prefix", removed)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != int64(live) {
		t.Errorf("queue holds %d, want exactly the %d live entries", n, live)
	}
}

// Hopeless-set eviction, end to end through stageArticles: a set that stopped
// growing while far short of its need is deleted on its SECOND stale touch.
// Two war stories shaped this. First the check was dead in production — the
// staleness judgment read the touched_at the same pipeline had just written,
// so age was always zero, no set was ever evicted, and the "evicted"
// telemetry counter (documented as proof the machinery works) read 0 because
// the machinery was broken; continuously-touched base-collision garbage
// therefore never left, its TTL refreshing on every touch. The fix
// (pre-batch touched_at) then over-corrected: the check only runs on batches
// that ADD to a set, so one stale gap condemned exactly the batch that ended
// the silence, and a staging-pressure pause routinely evicted a resuming
// release WITH the articles it had already accumulated. Hence the strike:
// first stale touch accuses, second convicts, and any in-window touch clears.
func TestHopelessEvictionFiresOnStragglerTouch(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	evicted := 0
	r := newRedisStaging(rdb, func(context.Context) int { return 2 },
		func(n int) { evicted += n }, nil)

	art := func(part int) stagedArticle {
		return stagedArticle{
			Group: "a.b.group", BaseSubject: "Collision.Base", Subject: "Collision.Base",
			MessageID: fmt.Sprintf("<p%d@x>", part), Poster: "p", Bytes: 1000,
			Posted: time.Now(), PartNum: part, TotalParts: 100, SegTotal: 100,
		}
	}

	if _, err := r.stageArticles(ctx, []stagedArticle{art(1)}); err != nil {
		t.Fatal(err)
	}
	hash := groupHashKey("a.b.group", "Collision.Base")
	gk, ak := grpKey("a.b.group", hash), artKey("a.b.group", hash)

	// A fresh set must never be evicted, however incomplete: large releases
	// arrive over many batches and judging them at birth deleted them mid-fill
	// (the ~128-sets-a-minute prod incident).
	if _, err := r.stageArticles(ctx, []stagedArticle{art(2)}); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, gk, ak).Result(); n != 2 {
		t.Fatalf("still-growing set was evicted (%d of 2 keys left)", n)
	}
	if evicted != 0 {
		t.Fatalf("onEvict fired %d times for a growing set", evicted)
	}

	// Backdate the last touch past the staleness bar: the set has now "stopped
	// growing". The next straggler re-touches it — 3 of 100 parts is far under
	// the completeness bar — but the FIRST stale touch only records a strike:
	// evicting here is the resume-destruction bug (the batch that ends a
	// pause was condemned with the articles it carried).
	if err := rdb.HSet(ctx, gk, "touched_at", time.Now().Unix()-400).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stageArticles(ctx, []stagedArticle{art(3)}); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, gk, ak).Result(); n != 2 {
		t.Fatalf("first stale touch evicted the set (%d of 2 keys left) — the resuming batch died with its articles", n)
	}
	if strike, _ := rdb.HGet(ctx, gk, "evict_strike").Result(); strike == "" {
		t.Error("first stale touch recorded no strike")
	}
	if evicted != 0 {
		t.Fatalf("onEvict fired %d times on the first stale touch", evicted)
	}

	// A second stale gap is the verdict: garbage that touches, goes silent,
	// and touches again is still shed — one staleness window later than
	// before, which is the price of not destroying resuming releases.
	if err := rdb.HSet(ctx, gk, "touched_at", time.Now().Unix()-400).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stageArticles(ctx, []stagedArticle{art(4)}); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, gk, ak).Result(); n != 0 {
		t.Errorf("twice-struck hopeless set still staged (%d of its keys exist)", n)
	}
	if member, _ := rdb.SIsMember(ctx, activeKey("a.b.group"), hash).Result(); member {
		t.Error("evicted set still referenced from active_groups")
	}
	if evicted != 1 {
		t.Errorf("onEvict reported %d, want 1", evicted)
	}
}

// The other half of the strike design: a resuming backfill flood takes its
// strike, keeps its articles, and the flood's next in-window batch CLEARS the
// strike — so every pause gets a fresh pass and a slowly-accumulating release
// can never be worn down two strikes at a time across pauses.
func TestEvictionSparesTheResumingFlood(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	evicted := 0
	r := newRedisStaging(rdb, func(context.Context) int { return 2 },
		func(n int) { evicted += n }, nil)

	art := func(part int) stagedArticle {
		return stagedArticle{
			Group: "a.b.flood", BaseSubject: "Big.Release", Subject: "Big.Release",
			MessageID: fmt.Sprintf("<f%d@x>", part), Poster: "p", Bytes: 1000,
			Posted: time.Now(), PartNum: part, TotalParts: 100, SegTotal: 100,
		}
	}
	batch := func(lo, hi int) []stagedArticle {
		var out []stagedArticle
		for i := lo; i <= hi; i++ {
			out = append(out, art(i))
		}
		return out
	}
	if _, err := r.stageArticles(ctx, batch(1, 20)); err != nil {
		t.Fatal(err)
	}
	hash := groupHashKey("a.b.flood", "Big.Release")
	gk, ak := grpKey("a.b.flood", hash), artKey("a.b.flood", hash)

	stale := func() {
		if err := rdb.HSet(ctx, gk, "touched_at", time.Now().Unix()-400).Err(); err != nil {
			t.Fatal(err)
		}
	}

	// Pause, then the resume batch: strike, but every article survives.
	stale()
	if _, err := r.stageArticles(ctx, batch(21, 25)); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.HLen(ctx, ak).Result(); n != 25 {
		t.Fatalf("resume batch destroyed articles: %d staged, want 25", n)
	}
	// The flood continues in-window: the strike clears.
	if _, err := r.stageArticles(ctx, batch(26, 28)); err != nil {
		t.Fatal(err)
	}
	if strike, _ := rdb.HGet(ctx, gk, "evict_strike").Result(); strike != "" {
		t.Error("an in-window batch did not clear the strike")
	}
	// A second pause gets the same fresh pass — proof the clear was real.
	stale()
	if _, err := r.stageArticles(ctx, batch(29, 30)); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.HLen(ctx, ak).Result(); n != 30 {
		t.Fatalf("second pause's resume batch destroyed articles: %d staged, want 30", n)
	}
	if evicted != 0 {
		t.Fatalf("onEvict fired %d times against a live release", evicted)
	}
}

// One failed write inside the stage pipeline must fail the batch. Exec
// reports only the FIRST failed command, and a batch touching any brand-new
// set queues a guaranteed-benign redis.Nil (its prevTouch HGet) ahead of
// every write behind it — so a server-side write failure (the live case is
// crossing maxmemory: reads answer, denyoom writes refuse) hid behind the
// Nil, the batch reported ok, the watermark advanced over a half-applied
// stage, and a set completed by that batch was lost for good (completeness
// fires only on a batch that ADDS).
func TestStagePipelineErrorNotMaskedByNewSetNil(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	r := newRedisStaging(rdb, func(context.Context) int { return 2 }, nil, nil)

	// Corrupt the active set so the pipelined SAdd fails WRONGTYPE server-side
	// while the transport stays clean.
	if err := rdb.Set(ctx, activeKey("a.b.mask"), "junk", 0).Err(); err != nil {
		t.Fatal(err)
	}
	arts := []stagedArticle{
		{Group: "a.b.mask", BaseSubject: "Two.Parter", Subject: "Two.Parter",
			MessageID: "<m1@x>", Poster: "p", Bytes: 100, Posted: time.Now(),
			PartNum: 1, TotalParts: 2, SegTotal: 2},
		{Group: "a.b.mask", BaseSubject: "Two.Parter", Subject: "Two.Parter",
			MessageID: "<m2@x>", Poster: "p", Bytes: 100, Posted: time.Now(),
			PartNum: 2, TotalParts: 2, SegTotal: 2},
	}
	if _, err := r.stageArticles(ctx, arts); err == nil {
		t.Fatal("a batch with a failed pipelined write reported success — the watermark would advance over it")
	}

	// The whole-batch retry recovers the release once the failure clears.
	if err := rdb.Del(ctx, activeKey("a.b.mask")).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stageArticles(ctx, arts); err != nil {
		t.Fatalf("clean retry failed: %v", err)
	}
	hash := groupHashKey("a.b.mask", "Two.Parter")
	if member, _ := rdb.SIsMember(ctx, readyKey, "a.b.mask:"+hash).Result(); !member {
		t.Error("re-staged complete set never reached nzb:ready")
	}
}

// The tolerance the sweep must keep: a brand-new set's prevTouch HGet answers
// redis.Nil on a healthy server, and that must stay benign — tightening the
// sweep to reject Nil would fail every batch that discovers a set.
func TestStageNewSetPrevTouchNilIsBenign(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	r := newRedisStaging(rdb, func(context.Context) int { return 2 }, nil, nil)

	arts := []stagedArticle{{
		Group: "a.b.fresh", BaseSubject: "New.Set", Subject: "New.Set",
		MessageID: "<n1@x>", Poster: "p", Bytes: 100, Posted: time.Now(),
		PartNum: 1, TotalParts: 5, SegTotal: 5,
	}}
	if _, err := r.stageArticles(ctx, arts); err != nil {
		t.Fatalf("staging a brand-new set failed: %v", err)
	}
	hash := groupHashKey("a.b.fresh", "New.Set")
	if n, _ := rdb.HLen(ctx, artKey("a.b.fresh", hash)).Result(); n != 1 {
		t.Errorf("new set's article did not land (HLen=%d)", n)
	}
}

// Meta totals must only ever RISE. Plain HSet made them per-batch maxima: a
// smaller re-post merged under the same base subject overwrote total_files /
// seg_total / st:N downward, staging's completeness check then demanded LESS
// than the builder (which recomputes the max from the loaded articles), the
// set queued as complete and the builder refused it — forever, re-loading and
// re-refusing it every pass. 2026-07-30 prod: 157 such sets, ~7 GB pinned,
// backfill paused against a drain that could never drain.
func TestStageMetaTotalsNeverRegress(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	art := func(fn, part, segTotal, totalFiles int, id string) stagedArticle {
		return stagedArticle{
			Group: "a.b.group", BaseSubject: "Show.S01.REMUX",
			Subject:   fmt.Sprintf("Show.S01.REMUX [%02d/%02d] (%d/%d)", fn, totalFiles, part, segTotal),
			MessageID: id, Poster: "p", Bytes: 1000, Posted: time.Now(),
			PartNum: part, TotalParts: segTotal, SegTotal: segTotal,
			FileNum: fn, TotalFiles: totalFiles, FileParts: true,
		}
	}

	// Batch 1: the larger variant — file 1 claims 3 segments, 2 files total.
	if _, err := r.stageArticles(ctx, []stagedArticle{
		art(1, 1, 3, 2, "<big-1-1@x>"),
		art(1, 2, 3, 2, "<big-1-2@x>"),
	}); err != nil {
		t.Fatal(err)
	}
	// Batch 2: a smaller re-post under the SAME subject claims file 1 has only
	// 2 segments; its parts land in the same field slots. Under the old
	// overwrite, st:1 dropped 3→2 and the set counted complete right here.
	if _, err := r.stageArticles(ctx, []stagedArticle{
		art(1, 1, 2, 2, "<small-1-1@x>"),
		art(1, 2, 2, 2, "<small-1-2@x>"),
		art(2, 1, 1, 2, "<small-2-1@x>"),
	}); err != nil {
		t.Fatal(err)
	}
	gk := grpKey("a.b.group", groupHashKey("a.b.group", "Show.S01.REMUX"))
	if st1, _ := rdb.HGet(ctx, gk, "st:1").Result(); st1 != "3" {
		t.Errorf("st:1 = %q after the smaller re-post, want 3 — totals must never regress", st1)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 0 {
		t.Fatalf("set queued as ready while file 1 is short of its largest claim (%d in queue) — the exact shape of the prod jam", n)
	}

	// The larger variant's third part arrives: NOW the set is complete by the
	// same rule the builder applies.
	if _, err := r.stageArticles(ctx, []stagedArticle{
		art(1, 3, 3, 2, "<big-1-3@x>"),
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Fatalf("complete set not queued (%d in queue)", n)
	}
	// And the builder AGREES — the invariant the jam broke: staging-complete
	// must imply builder-complete.
	arts, err := r.groupArticles(ctx, "a.b.group", "Show.S01.REMUX")
	if err != nil {
		t.Fatal(err)
	}
	if !isComplete(arts) {
		t.Error("staging queued a set the builder's isComplete refuses — the two checks have diverged again")
	}
}

// file_parts must be sticky-true, mirroring isComplete's trigger (ANY article
// with file numbering makes the set multi-file). It used to be whatever
// article led the latest batch, and one false overwrite dropped a multi-file
// set to the single-file completeness arm — where a fraction of its articles
// can satisfy total_parts.
func TestFilePartsFlagIsSticky(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	if _, err := r.stageArticles(ctx, []stagedArticle{{
		Group: "a.b.group", BaseSubject: "Multi.Release",
		Subject: "Multi.Release [01/02] (1/3)", MessageID: "<m1@x>", Poster: "p",
		Bytes: 1, Posted: time.Now(),
		PartNum: 1, TotalParts: 3, SegTotal: 3, FileNum: 1, TotalFiles: 2, FileParts: true,
	}}); err != nil {
		t.Fatal(err)
	}
	// A stray unnumbered companion (no file numbering) lands in a later batch.
	if _, err := r.stageArticles(ctx, []stagedArticle{{
		Group: "a.b.group", BaseSubject: "Multi.Release",
		Subject: "Multi.Release (1/5)", MessageID: "<m2@x>", Poster: "p",
		Bytes: 1, Posted: time.Now(),
		PartNum: 1, TotalParts: 5, SegTotal: 5,
	}}); err != nil {
		t.Fatal(err)
	}
	gk := grpKey("a.b.group", groupHashKey("a.b.group", "Multi.Release"))
	if fp, _ := rdb.HGet(ctx, gk, "file_parts").Result(); fp != "true" {
		t.Errorf("file_parts = %q after a non-file-parts batch, want true — one stray article must not drop "+
			"a multi-file set to the single-file completeness arm", fp)
	}
}

// demoteReady is a withdrawal, not a delete: the entry leaves nzb:ready, the
// staged articles survive, and a set that later truly completes re-queues
// itself — the property that makes demoting builder-refused candidates safe.
func TestDemoteReadyWithdrawsWithoutDestroying(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	stage := func(part int, id string) {
		t.Helper()
		if _, err := r.stageArticles(ctx, []stagedArticle{{
			Group: "a.b.group", BaseSubject: "Two.Parter",
			Subject:   fmt.Sprintf("Two.Parter (%d/2)", part),
			MessageID: id, Poster: "p", Bytes: 1, Posted: time.Now(),
			PartNum: part, TotalParts: 2, SegTotal: 2,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	stage(1, "<tp1@x>")
	stage(2, "<tp2@x>")
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Fatalf("fixture: complete set not queued (%d)", n)
	}

	hash := groupHashKey("a.b.group", "Two.Parter")
	// Simulate the divergence population: an entry in nzb:ready whose articles
	// no longer satisfy the builder (here a lost field; in prod, merged
	// re-posts and meta staged before totals were monotonic).
	if err := rdb.HDel(ctx, artKey("a.b.group", hash), formatFieldKey(0, 2)).Err(); err != nil {
		t.Fatal(err)
	}

	ok, err := r.demoteReady(ctx, "a.b.group", "Two.Parter")
	if err != nil || !ok {
		t.Fatalf("demote: ok=%v err=%v, want a removal", ok, err)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 0 {
		t.Fatal("entry still in nzb:ready after demote")
	}
	if n, _ := rdb.Exists(ctx, artKey("a.b.group", hash), grpKey("a.b.group", hash)).Result(); n != 2 {
		t.Fatalf("staged keys destroyed by demote (%d of 2 left)", n)
	}
	if ok, err := r.demoteReady(ctx, "a.b.group", "Two.Parter"); err != nil || ok {
		t.Errorf("re-demote of an absent entry: ok=%v err=%v, want false/nil", ok, err)
	}

	// The missing part arriving re-queues the set.
	stage(2, "<tp2b@x>")
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != 1 {
		t.Fatal("re-completed set not re-queued after demotion")
	}
}

// A sample that could not be taken must never masquerade as an empty pipeline:
// "0 forming" during a Redis outage is a false zero in exactly the readout an
// operator checks when staging is at its ceiling — which is also exactly when
// Redis is most likely to fail the sample.
func TestIncompleteSetsSurfacesTotalFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dead := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1,
	})
	defer dead.Close()
	r := newRedisStaging(dead, func(context.Context) int { return 2 }, nil, nil)

	if _, err := r.incompleteSets(ctx, 15, []string{"a.b.group"}); err == nil {
		t.Error("every group's sample failed and incompleteSets returned a nil error — a Redis outage reads as \"0 forming\"")
	}

	// An empty group list is NOT an error: with no active groups there is
	// legitimately nothing in flight.
	live := testRedis(t)
	rl := newTestStaging(live)
	if sets, err := rl.incompleteSets(ctx, 15, nil); err != nil || sets != nil {
		t.Errorf("empty group list: sets=%v err=%v, want nil/nil", sets, err)
	}
}
