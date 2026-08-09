package seedlock

import (
	"strings"
	"testing"
	"time"
)

func on() Policy {
	p := DefaultPolicy()
	p.Enabled = true
	return p
}

// Every check before the refusal is a reason NOT to lock somebody out of their
// own torrent. A rule that refuses the wrong announce is worse than one that
// misses a cheat: the cheat costs the site some ratio, the false positive
// costs a member their download with an error they cannot fix.
func TestNobodyIsLockedOutWithoutCause(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	held := Claim{Host: "10.0.0.1", LastSeen: now.Add(-time.Minute)}

	for _, tc := range []struct {
		name   string
		policy Policy
		host   string
		event  string
		want   Verdict
	}{
		{"the rule is off", DefaultPolicy(), "10.0.0.9", "", Allow},
		{"the same host announcing again", on(), "10.0.0.1", "", Allow},
		{"a member stopping releases the claim", on(), "10.0.0.9", "stopped", Release},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := Decide(tc.policy, held, tc.host, tc.event, now)
			if got != tc.want {
				t.Errorf("verdict = %v, want %v", got, tc.want)
			}
		})
	}

	// An unclaimed torrent is free to anyone.
	if got, _ := Decide(on(), Claim{}, "10.0.0.9", "", now); got != Allow {
		t.Errorf("unclaimed torrent refused: %v", got)
	}
}

// The cheat this exists for: the same member, the same torrent, a second
// machine, while the first is still going.
func TestASecondHostIsRefused(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	held := Claim{Host: "10.0.0.1", LastSeen: now.Add(-time.Minute)}

	got, reason := Decide(on(), held, "203.0.113.7", "", now)
	if got != Refuse {
		t.Fatalf("verdict = %v, want Refuse", got)
	}
	// The message is read by a person looking at their torrent client, so it
	// has to say what to DO.
	for _, want := range []string{"another host", "Stop it there", "clear the lock"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
	// And it must not print the other machine's address in full — a torrent
	// client is not a private place.
	if strings.Contains(reason, "10.0.0.1") {
		t.Errorf("reason leaks the full address: %q", reason)
	}
}

// A claim outlives its last announce by the lock window and no longer. Past
// that the torrent is free, which is what stops a crashed client locking
// somebody out of their own torrent forever.
func TestAClaimLapses(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := on()

	fresh := Claim{Host: "10.0.0.1", LastSeen: now.Add(-29 * time.Minute)}
	if got, _ := Decide(p, fresh, "10.0.0.9", "", now); got != Refuse {
		t.Errorf("inside the window = %v, want Refuse", got)
	}
	stale := Claim{Host: "10.0.0.1", LastSeen: now.Add(-31 * time.Minute)}
	if got, _ := Decide(p, stale, "10.0.0.9", "", now); got != Allow {
		t.Errorf("past the window = %v, want Allow", got)
	}
}

// Stopping RELEASES rather than refuses, and the distinction matters: a member
// who stops on host A must be able to start on host B, and a "stopped" from
// the losing host must not be treated as another attempt to seed.
func TestStoppingAlwaysReleases(t *testing.T) {
	now := time.Now()
	held := Claim{Host: "10.0.0.1", LastSeen: now}
	for _, host := range []string{"10.0.0.1", "203.0.113.7"} {
		if got, _ := Decide(on(), held, host, "stopped", now); got != Release {
			t.Errorf("stopped from %s = %v, want Release", host, got)
		}
	}
}

// What counts as "the same host" is a policy choice, because the two answers
// behave very differently: a peer_id is minted per torrent session, so a client
// restart looks like a new machine.
func TestIdentifyByIsAChoice(t *testing.T) {
	p := on()
	if got := p.Host("10.0.0.1", "-LT1000-aaa"); got != "10.0.0.1" {
		t.Errorf("default identity = %q, want the IP", got)
	}
	p.IdentifyBy = "peer"
	if got := p.Host("10.0.0.1", "-LT1000-aaa"); got != "-LT1000-aaa" {
		t.Errorf("peer identity = %q, want the peer id", got)
	}
	// Anything unrecognised falls back to the honest default rather than
	// locking on an empty string, which would make every host identical.
	p.IdentifyBy = "nonsense"
	if got := p.Host("10.0.0.1", "-LT1000-aaa"); got != "10.0.0.1" {
		t.Errorf("unknown identity mode = %q, want the IP", got)
	}
}

// A zero is an unset config key, not a request for a zero-minute lock — which
// would refuse nobody while looking enabled.
func TestUnsetSettingsFallBackToDefaults(t *testing.T) {
	p := Policy{Enabled: true}
	if got := p.LockWindow(); got != 30*time.Minute {
		t.Errorf("LockWindow = %v, want 30m", got)
	}
	now := time.Now()
	held := Claim{Host: "10.0.0.1", LastSeen: now.Add(-time.Minute)}
	if got, _ := Decide(p, held, "203.0.113.7", "", now); got != Refuse {
		t.Errorf("a zero-minute window let a second host in: %v", got)
	}
}
