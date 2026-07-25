//go:build integration

package usenet

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// Integration tests for the coordination SQL — the part that decides whether two
// crawlers on two hosts collide. The pure logic (partitioning, term maths) is
// unit-tested; this file is here because the ATOMIC CLAIM GUARD cannot be tested
// without a real database, and it is the piece that fails silently and
// expensively if it is wrong.
//
//	go test -tags=integration -count=1 ./usenet/
//
// with USENET_TEST_DSN pointing at a throwaway Postgres.

func testStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("USENET_TEST_DSN")
	if dsn == "" {
		t.Skip("USENET_TEST_DSN not set")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Each test gets a private schema so they cannot interfere with each other.
	schema := fmt.Sprintf("usenet_t%d", time.Now().UnixNano()%1e9)
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	// Apply the plugin's own migrations, in order, into that schema.
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		body, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`SET search_path TO ` + schema + `; ` + string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return NewPGStore(core.NewStorage(db).SchemaDB(schema))
}

// TestLeaseClaimIsExclusive is the core guarantee: one key, one holder.
func TestLeaseClaimIsExclusive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.anime"

	// Exclusivity is heartbeat-backed: a claim by a worker that never
	// heartbeats is treated as a dead worker's (see leaseOwnerDeadAfter).
	// Real workers heartbeat before their first pass.
	if err := s.heartbeat(ctx, "worker-a"); err != nil {
		t.Fatal(err)
	}
	got, err := s.claimLease(ctx, leaseScopeGroup, key, "worker-a", time.Minute)
	if err != nil || !got {
		t.Fatalf("first claim: got=%v err=%v", got, err)
	}
	// A second worker must be refused while the lease is live.
	got, err = s.claimLease(ctx, leaseScopeGroup, key, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("worker-b took a lease worker-a already holds — two crawlers would crawl one group")
	}
	// The holder may renew.
	got, err = s.claimLease(ctx, leaseScopeGroup, key, "worker-a", time.Minute)
	if err != nil || !got {
		t.Fatalf("renew by holder: got=%v err=%v", got, err)
	}
}

// TestLeaseExpiryHandsOver: a killed worker's groups must not stay locked
// forever — the whole reason this is a lease and not an advisory lock.
func TestLeaseExpiryHandsOver(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.hdtv"

	if err := s.heartbeat(ctx, "dead-worker"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.claimLease(ctx, leaseScopeGroup, key, "dead-worker", time.Second); err != nil || !got {
		t.Fatalf("claim: got=%v err=%v", got, err)
	}
	if got, _ := s.claimLease(ctx, leaseScopeGroup, key, "live-worker", time.Second); got {
		t.Fatal("took a live lease")
	}
	time.Sleep(1500 * time.Millisecond) // let it lapse

	got, err := s.claimLease(ctx, leaseScopeGroup, key, "live-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expired lease was not reclaimable — a dead worker's groups would never be crawled again")
	}
}

// TestLeaseDeadOwnerTakeover: a worker that stopped heartbeating must lose
// its leases to a replacement IMMEDIATELY, not at TTL expiry — container
// recreation renames the worker, and a deploy that kills a mid-pass crawl
// otherwise idles every held group for lease_ttl_min (observed on prod:
// "every group already claimed by another worker" for 15 minutes per deploy).
func TestLeaseDeadOwnerTakeover(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.orphaned"

	// The old worker claims with a long TTL and then "dies": it has no
	// heartbeat row at all (its replacement can never refresh it).
	if got, err := s.claimLease(ctx, leaseScopeGroup, key, "old-container", time.Hour); err != nil || !got {
		t.Fatalf("claim: got=%v err=%v", got, err)
	}
	if err := s.heartbeat(ctx, "new-container"); err != nil {
		t.Fatal(err)
	}
	got, err := s.claimLease(ctx, leaseScopeGroup, key, "new-container", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("replacement could not take a dead worker's unexpired lease — every deploy idles the crawler until the TTL lapses")
	}
}

// TestWithLeaseCancelsOnLoss: when a lease is stolen mid-work (dead-owner
// takeover by a heartbeating sibling), the next renewal must CANCEL the work
// context — from that moment the sibling legitimately owns the work, and
// running on risks overlapping writes. Slow by nature: the renewal tick has a
// 5s floor, so detection takes one tick.
func TestWithLeaseCancelsOnLoss(t *testing.T) {
	s := testStore(t)
	p := &Plugin{st: s, core: &core.Core{Errors: core.NewErrorReporter(core.ErrorAdapter{})}}
	ctx := context.Background()
	const key = "steal-target"

	// The holder (this process's workerID) writes no heartbeat, so a
	// heartbeating thief takes the lease via the dead-owner clause — the
	// same path a real deploy takes.
	if err := s.heartbeat(ctx, "thief"); err != nil {
		t.Fatal(err)
	}
	ran := p.withLease(ctx, leaseScopeJob, key, 6*time.Second, func(workCtx context.Context) {
		if got, err := s.claimLease(ctx, leaseScopeJob, key, "thief", time.Minute); err != nil || !got {
			t.Errorf("thief could not steal the lease: got=%v err=%v", got, err)
			return
		}
		select {
		case <-workCtx.Done():
			// Renewal noticed the loss and cancelled the pass.
		case <-time.After(15 * time.Second):
			t.Error("work context not cancelled within 15s of the lease being stolen")
		}
	})
	if !ran {
		t.Fatal("withLease did not run fn despite a free lease")
	}
}

// TestLeaseReleaseIsOwnerOnly: releasing must not let a worker drop someone
// else's lease out from under them.
func TestLeaseReleaseIsOwnerOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.movies"

	if err := s.heartbeat(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.claimLease(ctx, leaseScopeGroup, key, "owner", time.Minute); !got {
		t.Fatal("claim failed")
	}
	if err := s.releaseLease(ctx, leaseScopeGroup, key, "impostor"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.claimLease(ctx, leaseScopeGroup, key, "impostor", time.Minute); got {
		t.Fatal("a non-owner's release freed the lease")
	}
	if err := s.releaseLease(ctx, leaseScopeGroup, key, "owner"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.claimLease(ctx, leaseScopeGroup, key, "next", time.Minute); !got {
		t.Fatal("owner's release did not free the lease")
	}
}

// TestLeaseConcurrentClaimRace: the guard has to hold under real concurrency,
// not just sequential calls. Exactly one of N racing workers may win.
func TestLeaseConcurrentClaimRace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.contested"
	const racers = 12

	// All racers heartbeat first — takeover semantics only free a lease whose
	// owner is NOT alive, and here every racer is.
	for i := 0; i < racers; i++ {
		if err := s.heartbeat(ctx, fmt.Sprintf("w%02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got, err := s.claimLease(ctx, leaseScopeGroup, key, fmt.Sprintf("w%02d", i), time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if got {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d workers won the same lease, want exactly 1", winners)
	}
}

// TestLeaseScopesAreIndependent: a job lease and a group lease of the same name
// must not collide.
func TestLeaseScopesAreIndependent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if got, _ := s.claimLease(ctx, leaseScopeGroup, "shared-name", "a", time.Minute); !got {
		t.Fatal("group claim failed")
	}
	if got, _ := s.claimLease(ctx, leaseScopeJob, "shared-name", "b", time.Minute); !got {
		t.Fatal("job scope was blocked by a group lease of the same key")
	}
}

// TestWorkerPresenceAndTerm covers the membership rules the split depends on:
// a mid-term joiner is excluded (so it waits), and a silent worker drops out.
func TestWorkerPresenceAndTerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.heartbeat(ctx, "worker-old"); err != nil {
		t.Fatal(err)
	}
	// Term boundary in the future => everyone alive counts.
	future := time.Now().Add(time.Minute)
	got, err := s.eligibleWorkers(ctx, future, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "worker-old" {
		t.Fatalf("eligible = %v, want [worker-old]", got)
	}

	// A worker that joined AFTER the term began must not count yet.
	if err := s.heartbeat(ctx, "worker-new"); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	got, err = s.eligibleWorkers(ctx, past, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range got {
		if w == "worker-new" || w == "worker-old" {
			t.Errorf("%s counted despite joining after the term start", w)
		}
	}

	// Staleness: a worker that stopped heartbeating drops out.
	got, err = s.eligibleWorkers(ctx, future, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("eligible = %v with a 1ns staleness window, want none", got)
	}
}

// TestHeartbeatPreservesJoinedAt: re-beating must not reset seniority, or a
// long-running worker would be treated as a newcomer on every tick and never
// count toward the split.
func TestHeartbeatPreservesJoinedAt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.heartbeat(ctx, "steady"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := s.heartbeat(ctx, "steady"); err != nil {
		t.Fatal(err)
	}
	// Term start between the two beats: still eligible iff joined_at was kept.
	between := time.Now().Add(-500 * time.Millisecond)
	got, err := s.eligibleWorkers(ctx, between, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("eligible = %v; heartbeat appears to have reset joined_at, which "+
			"would make a steady worker permanently ineligible", got)
	}
}

// TestProviderCRUD covers the management surface, including the rule that would
// be worst to get wrong: an empty password on update must KEEP the stored one.
// The list view never sends passwords to the browser, so blank means
// "unchanged" — clearing it instead would silently break authentication the
// next time that provider crawled.
func TestProviderCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.upsertServer(ctx, provider{
		Host: "news.eweka.nl", Port: 563, TLS: true, Username: "u", Password: "secret",
		Enabled: true, Role: roleActive, Priority: 10, Connections: 20, Backbone: "omicron",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	list, err := s.listServers(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d err=%v, want 1", len(list), err)
	}
	first := list[0]
	if first.Name != "news.eweka.nl" {
		t.Errorf("name should default to the host, got %q", first.Name)
	}
	if first.Password != "" {
		t.Error("listServers returned a password — it must never reach the browser")
	}

	// Edit everything EXCEPT the password.
	first.Priority = 5
	first.Role = roleBackup
	first.Password = ""
	if err := s.upsertServer(ctx, first); err != nil {
		t.Fatalf("update: %v", err)
	}
	var pw string
	if err := s.db.DB().QueryRowContext(ctx,
		`SELECT password FROM `+s.db.Schema()+`.servers WHERE id = $1`, first.ID).Scan(&pw); err != nil {
		t.Fatal(err)
	}
	if pw != "secret" {
		t.Fatalf("password = %q after an edit with a blank field; it must be preserved", pw)
	}

	// A non-empty password does replace it.
	first.Password = "rotated"
	if err := s.upsertServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.DB().QueryRowContext(ctx,
		`SELECT password FROM `+s.db.Schema()+`.servers WHERE id = $1`, first.ID).Scan(&pw); err != nil {
		t.Fatal(err)
	}
	if pw != "rotated" {
		t.Errorf("password = %q, want the rotated value", pw)
	}

	// Toggle parks a provider without losing it; providers() then skips it.
	if err := s.toggleServer(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Errorf("providers() returned %d disabled server(s)", len(enabled))
	}
	if all, _ := s.listServers(ctx); len(all) != 1 {
		t.Error("a disabled provider vanished from the management list")
	}

	if err := s.deleteServer(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.listServers(ctx); len(all) != 0 {
		t.Error("delete did not remove the provider")
	}
}

// TestUpsertServerRejectsHostless: a provider with no host would be dialled as
// ":119" every pass and bench itself.
func TestUpsertServerRejectsHostless(t *testing.T) {
	s := testStore(t)
	if err := s.upsertServer(context.Background(), provider{Host: "  "}); err == nil {
		t.Fatal("expected a hostless provider to be rejected")
	}
}
