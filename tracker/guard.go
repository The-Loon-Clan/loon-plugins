package tracker

import (
	"context"
	"sync"
)

// The admission seam.
//
// Sibling of multiplier.go and the same shape: the tracker asks, before it
// accepts an announce, whether this one may proceed. What counts as a reason to
// refuse — a member seeding the same torrent from three hosts at once, a
// banned client, a rate limit — is a site's policy and changes constantly, so
// the tracker does not know any of it.
//
// The two seams are separate on purpose. Credit adjusts NUMBERS and cannot fail
// an announce; this DECIDES, and a decision needs a reason a client can show a
// human. Folding them together would mean every economy plugin also had the
// power to refuse.
//
// Dormant by default. With nothing wired every announce is allowed, which is
// what happened before this file existed.

// AnnounceGuard decides whether an announce may proceed.
//
// Returns false with a short reason, which the tracker returns to the client as
// the bencoded failure reason — so it is read by a person looking at their
// torrent client, and should say what to do rather than what went wrong.
//
// Called on the announce path after the passkey and entitlement checks and
// BEFORE any accounting, so a refused announce changes nothing. Must be cheap
// and must not block on a network call.
type AnnounceGuard func(ctx context.Context, in GuardRequest) (allow bool, reason string)

// GuardRequest is what a guard is told about an announce.
//
// Deliberately more than the tracker itself needs: a policy about WHERE a
// member is seeding from cannot be written without the peer's address, and
// asking the tracker to grow a new field per policy would defeat the seam.
type GuardRequest struct {
	UserID   int64
	InfoHash string
	PeerID   string
	IP       string
	Port     int
	// Event is the announce event ("started", "stopped", "completed", or empty
	// for a periodic announce). A guard that locks a torrent to one host needs
	// this: "stopped" is a member leaving, which should release a claim rather
	// than be refused by it.
	Event string
	// Left is bytes remaining, so a guard can tell a seeder from a leecher
	// without a database lookup.
	Left int64
}

var (
	guardMu sync.RWMutex
	guard   AnnounceGuard
)

// SetAnnounceGuard installs the admission rule. A nil value clears it,
// restoring "every announce is allowed".
func SetAnnounceGuard(g AnnounceGuard) {
	guardMu.Lock()
	defer guardMu.Unlock()
	guard = g
}

// Admit asks the installed guard whether an announce may proceed.
//
// Allows when nothing is wired, and allows when a guard refuses without saying
// why: a refusal a member cannot read is indistinguishable from a broken
// tracker, and the safe failure here is to let the announce through rather than
// to strand somebody with a blank error.
func Admit(ctx context.Context, in GuardRequest) (bool, string) {
	guardMu.RLock()
	g := guard
	guardMu.RUnlock()
	if g == nil {
		return true, ""
	}
	allow, reason := g(ctx, in)
	if !allow && reason == "" {
		return true, ""
	}
	return allow, reason
}
