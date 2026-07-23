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

// TestLeaseReleaseIsOwnerOnly: releasing must not let a worker drop someone
// else's lease out from under them.
func TestLeaseReleaseIsOwnerOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const key = "omicron|alt.binaries.movies"

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
