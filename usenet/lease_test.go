package usenet

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The renewal-loss policy. Cancelling a pass is expensive — in the backfill it
// discards every fetched-but-unrecorded batch — so it must happen exactly
// twice: when the row definitively belongs to someone else, and when errors
// have eaten the TTL margin and expiry is one tick away. Cancelling on the
// FIRST transient error (the old behavior) converted every DB blip into a
// lost multi-hour pass, with two full renewal ticks of retry margin unused.
func TestRenewalLostPolicy(t *testing.T) {
	ttl := 15 * time.Minute
	blip := errors.New("connection reset")

	if !renewalLost(false, nil, time.Second, ttl) {
		t.Error("a definitive loss (someone else holds the row) must cancel immediately")
	}
	if renewalLost(true, blip, time.Second, ttl) {
		t.Error("one transient error cancelled the pass — the lease is still valid for two ticks")
	}
	if renewalLost(true, blip, 2*ttl/3-time.Second, ttl) {
		t.Error("errors within the TTL margin must keep retrying")
	}
	if !renewalLost(true, blip, 2*ttl/3, ttl) {
		t.Error("errors past two-thirds of the TTL must cancel before the lease lapses under a sibling")
	}
	if renewalLost(true, nil, time.Hour, ttl) {
		t.Error("a successful renewal is never a loss, whatever the clock says")
	}
}

// stubLeaseStore implements just the two lease methods over a map, in the
// fakeHealthStore mold: the embedded nil Store panics loudly if anything else
// is touched. releaseHook (optional) runs at the top of releaseLease so the
// race test below can hold a DELETE in flight.
type stubLeaseStore struct {
	Store
	mu          sync.Mutex
	rows        map[string]string // scope|key -> worker
	releaseHook func()
}

func newStubLeaseStore() *stubLeaseStore {
	return &stubLeaseStore{rows: map[string]string{}}
}

func (s *stubLeaseStore) claimLease(_ context.Context, scope, key, worker string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scope + "|" + key
	if owner, held := s.rows[k]; held && owner != worker {
		return false, nil
	}
	s.rows[k] = worker // reentrant for our own worker, like the real upsert
	return true, nil
}

func (s *stubLeaseStore) releaseLease(_ context.Context, scope, key, worker string) error {
	if s.releaseHook != nil {
		s.releaseHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scope + "|" + key
	if s.rows[k] == worker {
		delete(s.rows, k)
	}
	return nil
}

func (s *stubLeaseStore) owner(scope, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.rows[scope+"|"+key]
	return w, ok
}

// The in-process refcount that makes lease RELEASE job-aware. Claim is
// deliberately reentrant per worker id (crawl and backfill overlap on group
// keys by design), but release deleted the DB row job-blind: the backfill's
// per-round release deleted rows the crawl still worked under, and in
// multi-worker mode a sibling claimed the vacated key instantly — chronic
// mid-pass cancellations. Only the LAST in-process holder may delete, and
// since the fence the claim/increment and check/DELETE pairs are single
// steps under leaseHeldMu.
func TestLeaseRefcountReleasesOnlyOnLastHolder(t *testing.T) {
	st := newStubLeaseStore()
	p := &Plugin{st: st}
	ctx := context.Background()
	me := workerID()
	ttl := time.Minute

	for i := 0; i < 2; i++ { // the crawl's claim, then the backfill's reentrant one
		got, err := p.claimLeaseHeld(ctx, leaseScopeGroup, "bb|g1", me, ttl)
		if err != nil || !got {
			t.Fatalf("claim %d: got=%v err=%v", i, got, err)
		}
	}
	if err := p.releaseLeaseHeld(ctx, leaseScopeGroup, "bb|g1", me); err != nil {
		t.Fatal(err)
	}
	if _, held := st.owner(leaseScopeGroup, "bb|g1"); !held {
		t.Fatal("the first release deleted the DB row under the job still working the group")
	}
	if err := p.releaseLeaseHeld(ctx, leaseScopeGroup, "bb|g1", me); err != nil {
		t.Fatal(err)
	}
	if _, held := st.owner(leaseScopeGroup, "bb|g1"); held {
		t.Fatal("the last release did not delete the row — it would leak until TTL")
	}

	// Distinct scopes never interfere, even on the same key text.
	if _, err := p.claimLeaseHeld(ctx, leaseScopeGroup, "bb|g2", me, ttl); err != nil {
		t.Fatal(err)
	}
	if _, err := p.claimLeaseHeld(ctx, leaseScopeJob, "bb|g2", me, ttl); err != nil {
		t.Fatal(err)
	}
	if err := p.releaseLeaseHeld(ctx, leaseScopeGroup, "bb|g2", me); err != nil {
		t.Fatal(err)
	}
	if _, held := st.owner(leaseScopeJob, "bb|g2"); !held {
		t.Error("group-scope release took the job-scope row with it")
	}
	if err := p.releaseLeaseHeld(ctx, leaseScopeJob, "bb|g2", me); err != nil {
		t.Fatal(err)
	}

	// An unpaired release (early-return paths) is a safe no-op DELETE.
	if err := p.releaseLeaseHeld(ctx, leaseScopeGroup, "bb|never-acquired", me); err != nil {
		t.Error(err)
	}
}

// The two-step window the fence closes: a starting pass's claim landing
// between a peer's refcount-drop and its DELETE used to leave the row deleted
// under the new pass. With the DELETE inside the mutex, the re-claim
// serializes behind it and re-inserts afterwards — whatever the goroutine
// interleaving, the reclaimer ends up holding a live row. Run with -race.
func TestReleaseDeleteCannotRaceAReclaim(t *testing.T) {
	st := newStubLeaseStore()
	p := &Plugin{st: st}
	ctx := context.Background()
	me := workerID()
	ttl := time.Minute
	const key = "bb|contested"

	if got, err := p.claimLeaseHeld(ctx, leaseScopeGroup, key, me, ttl); err != nil || !got {
		t.Fatalf("initial claim: got=%v err=%v", got, err)
	}

	deleteEntered := make(chan struct{}, 1)
	deleteBlock := make(chan struct{})
	st.releaseHook = func() {
		deleteEntered <- struct{}{}
		<-deleteBlock
	}

	relDone := make(chan error, 1)
	go func() { relDone <- p.releaseLeaseHeld(ctx, leaseScopeGroup, key, me) }()
	<-deleteEntered // the DELETE is in flight (and, post-fix, holds the mutex)
	st.releaseHook = nil

	claimDone := make(chan bool, 1)
	go func() {
		got, err := p.claimLeaseHeld(ctx, leaseScopeGroup, key, me, ttl)
		claimDone <- got && err == nil
	}()

	close(deleteBlock)
	if err := <-relDone; err != nil {
		t.Fatal(err)
	}
	if !<-claimDone {
		t.Fatal("re-claim failed outright")
	}
	if w, held := st.owner(leaseScopeGroup, key); !held || w != me {
		t.Fatalf("the release's DELETE removed the row under the live re-claim (held=%v owner=%q)", held, w)
	}
	// Leave the refcount clean for -count=2 runs.
	if err := p.releaseLeaseHeld(ctx, leaseScopeGroup, key, me); err != nil {
		t.Fatal(err)
	}
}

// TestGroupLeaseKey: the lease unit is one BACKBONE'S view of one GROUP, because
// crawl state is keyed (backbone, group_name). Two workers on the same backbone
// but different groups must be able to run at once.
func TestGroupLeaseKey(t *testing.T) {
	a := groupLeaseKey("omicron", "alt.binaries.anime")
	b := groupLeaseKey("omicron", "alt.binaries.hdtv")
	if a == b {
		t.Error("different groups on one backbone must not share a lease")
	}
	if groupLeaseKey("abavia", "alt.binaries.anime") == a {
		t.Error("the same group on different backbones must not share a lease")
	}
	if groupLeaseKey("omicron", "alt.binaries.anime") != a {
		t.Error("lease key is not stable")
	}
}

// TestWorkerIDStableAndDistinctive: the id must be stable within a process (or a
// worker could not renew its own lease) and carry enough to identify the holder
// when diagnosing "who has this".
func TestWorkerIDStable(t *testing.T) {
	a, b := workerID(), workerID()
	if a != b {
		t.Fatalf("workerID not stable within a process: %q vs %q", a, b)
	}
	if !strings.Contains(a, "/") {
		t.Errorf("workerID %q should carry host/pid for diagnosis", a)
	}
}

// TestLeaseRenewInterval: renewal must be comfortably inside the TTL, or a slow
// pass lets its own lease lapse and a second worker starts the same group.
func TestLeaseRenewInterval(t *testing.T) {
	cases := []time.Duration{time.Minute, 15 * time.Minute, time.Hour}
	for _, ttl := range cases {
		got := leaseRenewInterval(ttl)
		if got >= ttl {
			t.Errorf("ttl %v: renew interval %v must be shorter than the TTL", ttl, got)
		}
		if got > ttl/2 {
			t.Errorf("ttl %v: renew interval %v leaves no margin for a missed tick", ttl, got)
		}
	}
	// Never so short that renewal becomes its own load.
	if got := leaseRenewInterval(time.Second); got < 5*time.Second {
		t.Errorf("tiny ttl produced a %v renew interval", got)
	}
}
