package tracker

import (
	"context"
	"math"
	"testing"
)

// With nothing wired, Credit must be the identity function.
//
// This is the test that matters most in this file. The seam was added to a
// tracker that had none, and every host running it today wires nothing — so if
// the default is anything other than "hand the deltas straight back", the
// change silently rewrites every member's ratio on a site that never asked for
// an economy.
func TestUnwiredCreditChangesNothing(t *testing.T) {
	SetMultiplier(nil)
	for _, tc := range [][2]int64{{0, 0}, {1, 1}, {1 << 30, 5 << 30}, {123456789, 987654321}} {
		up, down := Credit(context.Background(), 1, "aa", tc[0], tc[1])
		if up != tc[0] || down != tc[1] {
			t.Errorf("Credit(%d,%d) = (%d,%d), want them unchanged", tc[0], tc[1], up, down)
		}
	}
}

// The two perks this exists for.
func TestCreditAppliesTheFactors(t *testing.T) {
	defer SetMultiplier(nil)

	// Freeleech: upload counts, download does not.
	SetMultiplier(func(context.Context, int64, string) (float64, float64) { return 1, 0 })
	if up, down := Credit(context.Background(), 1, "aa", 500, 900); up != 500 || down != 0 {
		t.Errorf("freeleech = (%d,%d), want (500,0)", up, down)
	}

	// Double upload: upload doubles, download is untouched.
	SetMultiplier(func(context.Context, int64, string) (float64, float64) { return 2, 1 })
	if up, down := Credit(context.Background(), 1, "aa", 500, 900); up != 1000 || down != 900 {
		t.Errorf("double upload = (%d,%d), want (1000,900)", up, down)
	}
}

// The multiplier comes from a plugin the tracker did not write, and this is
// arithmetic on somebody's ratio. A negative factor would run their account
// backwards; NaN would produce a garbage int64. Both are ignored rather than
// trusted, and the delta passes through unchanged.
func TestNonsenseFactorsAreIgnored(t *testing.T) {
	defer SetMultiplier(nil)
	for _, f := range []float64{-1, -0.5, math.NaN()} {
		SetMultiplier(func(context.Context, int64, string) (float64, float64) { return f, f })
		up, down := Credit(context.Background(), 1, "aa", 400, 700)
		if up != 400 || down != 700 {
			t.Errorf("factor %v gave (%d,%d), want the deltas unchanged", f, up, down)
		}
	}
}

// Rounding is DOWN on both sides, deliberately: a member is never credited a
// byte they did not upload, and never charged one they did not take.
func TestCreditRoundsDown(t *testing.T) {
	defer SetMultiplier(nil)
	SetMultiplier(func(context.Context, int64, string) (float64, float64) { return 1.5, 1.5 })
	up, down := Credit(context.Background(), 1, "aa", 3, 3)
	if up != 4 || down != 4 {
		t.Errorf("Credit = (%d,%d), want (4,4) — 3*1.5 truncated", up, down)
	}
}

// The factors are asked per (member, torrent), because that is the grain a
// token is spent at: freeleech on ONE download, not on everything.
func TestCreditPassesWhoAndWhat(t *testing.T) {
	defer SetMultiplier(nil)
	var gotUser int64
	var gotHash string
	SetMultiplier(func(_ context.Context, u int64, h string) (float64, float64) {
		gotUser, gotHash = u, h
		return 1, 1
	})
	Credit(context.Background(), 42, "abc123", 1, 1)
	if gotUser != 42 || gotHash != "abc123" {
		t.Errorf("multiplier saw (%d,%q), want (42,\"abc123\")", gotUser, gotHash)
	}
}

// ── The admission seam ──────────────────────────────────────────────────────

// With nothing wired every announce is allowed, which is what happened before
// guard.go existed. Same property the multiplier needed, and for the same
// reason: every host running this today wires nothing.
func TestUnwiredGuardAllowsEverything(t *testing.T) {
	SetAnnounceGuard(nil)
	if ok, reason := Admit(context.Background(), GuardRequest{UserID: 1, InfoHash: "aa"}); !ok {
		t.Errorf("unwired guard refused an announce: %q", reason)
	}
}

func TestGuardCanRefuseWithAReason(t *testing.T) {
	defer SetAnnounceGuard(nil)
	SetAnnounceGuard(func(context.Context, GuardRequest) (bool, string) {
		return false, "already seeding from another host"
	})
	ok, reason := Admit(context.Background(), GuardRequest{UserID: 1, InfoHash: "aa"})
	if ok || reason != "already seeding from another host" {
		t.Errorf("Admit = (%v,%q), want a refusal with the reason", ok, reason)
	}
}

// A refusal nobody can read is indistinguishable from a broken tracker. The
// safe failure is to let the announce through rather than strand a member with
// a blank error in their client.
func TestSilentRefusalIsTreatedAsAllow(t *testing.T) {
	defer SetAnnounceGuard(nil)
	SetAnnounceGuard(func(context.Context, GuardRequest) (bool, string) { return false, "" })
	if ok, _ := Admit(context.Background(), GuardRequest{UserID: 1}); !ok {
		t.Error("a reasonless refusal blocked the announce")
	}
}

// A guard deciding WHERE somebody may seed from cannot be written without the
// peer's address, and one that releases a claim on "stopped" needs the event.
func TestGuardIsToldEnoughToDecide(t *testing.T) {
	defer SetAnnounceGuard(nil)
	var got GuardRequest
	SetAnnounceGuard(func(_ context.Context, in GuardRequest) (bool, string) {
		got = in
		return true, ""
	})
	want := GuardRequest{
		UserID: 7, InfoHash: "abc", PeerID: "-LT1000-x", IP: "203.0.113.9",
		Port: 6881, Event: "started", Left: 1234,
	}
	Admit(context.Background(), want)
	if got != want {
		t.Errorf("guard saw %+v, want %+v", got, want)
	}
}
