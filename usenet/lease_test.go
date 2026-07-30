package usenet

import (
	"errors"
	"strings"
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
