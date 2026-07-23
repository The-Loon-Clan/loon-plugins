package usenet

import (
	"strings"
	"testing"
	"time"
)

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
