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
