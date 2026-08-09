package seedlock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/tracker"
	"github.com/the-loon-clan/loon/core"
)

// fakeStore stands in for Redis so the admit path can be exercised on its
// decisions rather than on a server.
type fakeStore struct {
	held      map[string]Claim
	failWith  error
	released  []string
	refreshed []string
}

func newFake() *fakeStore { return &fakeStore{held: map[string]Claim{}} }

func (f *fakeStore) key(u int64, h string) string { return itoa(u) + ":" + h }

func (f *fakeStore) Acquire(_ context.Context, u int64, h, host string, _ time.Duration) (Claim, error) {
	if f.failWith != nil {
		return Claim{}, f.failWith
	}
	k := f.key(u, h)
	if cur, ok := f.held[k]; ok {
		return cur, nil
	}
	f.held[k] = Claim{Host: host, LastSeen: time.Now()}
	return Claim{}, nil // claimed
}

func (f *fakeStore) Refresh(_ context.Context, u int64, h string, _ time.Duration) error {
	f.refreshed = append(f.refreshed, f.key(u, h))
	return nil
}

func (f *fakeStore) Release(_ context.Context, u int64, h string) error {
	f.released = append(f.released, f.key(u, h))
	delete(f.held, f.key(u, h))
	return nil
}

func (f *fakeStore) Held(_ context.Context, u int64, h string) (Claim, error) {
	return f.held[f.key(u, h)], nil
}

func (f *fakeStore) HeldBy(context.Context, int64) (map[string]Claim, error) {
	return map[string]Claim{}, nil
}

func armed(st Store) *Plugin {
	p := &Plugin{core: &core.Core{}, st: st}
	p.cfg = DefaultPolicy()
	p.cfg.Enabled = true
	p.cfg = p.cfg.normalise()
	return p
}

func req(user int64, hash, ip, event string) tracker.GuardRequest {
	return tracker.GuardRequest{UserID: user, InfoHash: hash, IP: ip, PeerID: "-LT1000-x", Event: event}
}

// The first host claims it; a second host is refused; the first keeps working.
func TestFirstHostClaimsAndSecondIsRefused(t *testing.T) {
	f := newFake()
	p := armed(f)
	ctx := context.Background()

	if ok, _ := p.admit(ctx, req(1, "aa", "10.0.0.1", "started")); !ok {
		t.Fatal("the first host was refused its own claim")
	}
	ok, reason := p.admit(ctx, req(1, "aa", "203.0.113.7", ""))
	if ok {
		t.Error("a second host was admitted while the first held the claim")
	}
	if reason == "" {
		t.Error("refused with no reason — the client shows a blank error")
	}
	// The holder keeps announcing, and each one pushes the window out.
	if ok, _ := p.admit(ctx, req(1, "aa", "10.0.0.1", "")); !ok {
		t.Error("the holder was refused its own torrent")
	}
	if len(f.refreshed) == 0 {
		t.Error("the holder's announce did not extend the claim — it would lapse mid-session")
	}
}

// Stopping releases, so a member can move machines. This is the case that
// would otherwise trap people on the host they started from.
func TestStoppingReleasesSoAMemberCanMove(t *testing.T) {
	f := newFake()
	p := armed(f)
	ctx := context.Background()

	p.admit(ctx, req(1, "aa", "10.0.0.1", "started"))
	if ok, _ := p.admit(ctx, req(1, "aa", "10.0.0.1", "stopped")); !ok {
		t.Fatal("a stop was refused")
	}
	if len(f.released) != 1 {
		t.Fatalf("released %d claims on stop, want 1", len(f.released))
	}
	// The other machine can now take it.
	if ok, _ := p.admit(ctx, req(1, "aa", "203.0.113.7", "started")); !ok {
		t.Error("the second host could not claim a released torrent")
	}
}

// One member's claim must not touch another's, nor one torrent another.
func TestClaimsAreScopedToAMemberAndATorrent(t *testing.T) {
	f := newFake()
	p := armed(f)
	ctx := context.Background()

	p.admit(ctx, req(1, "aa", "10.0.0.1", "started"))
	if ok, _ := p.admit(ctx, req(2, "aa", "203.0.113.7", "started")); !ok {
		t.Error("one member's claim blocked another member on the same torrent")
	}
	if ok, _ := p.admit(ctx, req(1, "bb", "203.0.113.7", "started")); !ok {
		t.Error("a claim on one torrent blocked the same member on another")
	}
}

// A tracker that refuses every announce because its cache is down has turned
// an outage into a site-wide ban.
func TestRedisFailureAdmitsRatherThanRefuses(t *testing.T) {
	f := newFake()
	f.failWith = errors.New("connection refused")
	p := armed(f)
	if ok, _ := p.admit(context.Background(), req(1, "aa", "10.0.0.1", "")); !ok {
		t.Error("an unreachable store blocked the announce")
	}
}

// Not armed — no store — must be transparent, not a refusal.
func TestUnarmedPluginAdmitsEverything(t *testing.T) {
	p := armed(nil)
	p.st = nil
	if ok, _ := p.admit(context.Background(), req(1, "aa", "10.0.0.1", "")); !ok {
		t.Error("an unarmed plugin refused an announce")
	}
}

// A member behind something that hides their address should not be locked out
// by a rule that cannot see them.
func TestUnidentifiableHostIsAdmitted(t *testing.T) {
	f := newFake()
	p := armed(f)
	in := tracker.GuardRequest{UserID: 1, InfoHash: "aa"} // no IP, no peer id
	if ok, _ := p.admit(context.Background(), in); !ok {
		t.Error("an announce with no identifiable host was refused")
	}
}

// The clear action takes back a member's OWN torrent and nothing else.
func TestClearReleasesOnlyTheCallersClaim(t *testing.T) {
	f := newFake()
	p := armed(f)
	ctx := context.Background()

	p.admit(ctx, req(1, "aa", "10.0.0.1", "started"))
	p.admit(ctx, req(2, "aa", "10.0.0.5", "started"))

	if err := p.ClearClaim(ctx, 1, "aa"); err != nil {
		t.Fatal(err)
	}
	// Member 1's claim is gone, so their other machine can take it.
	if ok, _ := p.admit(ctx, req(1, "aa", "203.0.113.7", "started")); !ok {
		t.Error("clearing did not release the caller's own claim")
	}
	// Member 2's is untouched.
	if _, ok := f.held[f.key(2, "aa")]; !ok {
		t.Error("clearing one member's claim removed another's")
	}
}
