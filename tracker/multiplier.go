package tracker

import (
	"context"
	"sync"
)

// The credit seam.
//
// A private tracker's economy is not the tracker's business. Freeleech, double
// upload, a launch-week promotion, a per-torrent bonus — every site runs a
// different set, they change often, and none of them are the announce
// protocol. So this file is the whole of what the tracker knows about them: it
// asks, before it writes, how much of a delta to credit.
//
// The tracker never learns what a perk IS. It receives two numbers.
//
// Dormant by default. With nothing wired, Credit returns the deltas unchanged,
// which is exactly what happened before this file existed — so a host that
// installs no economy plugin sees no behavioural difference at all.

// Multiplier decides how much of an announce delta to credit to a member.
//
// Returns the factors for uploaded and downloaded bytes. Freeleech is
// (1, 0) — upload counts, download does not. Double upload is (2, 1). A
// promotion applying to everything is (2, 0).
//
// Called on the announce path, which runs every few minutes per peer, so an
// implementation must be cheap and must not block on a network call. Errors
// have no channel here on purpose: an economy that cannot answer should credit
// normally rather than fail an announce, and a torrent client cannot act on the
// distinction anyway.
type Multiplier func(ctx context.Context, userID int64, infoHash string) (up, down float64)

var (
	multMu sync.RWMutex
	mult   Multiplier
)

// SetMultiplier installs the credit rule. Called from the host's main() before
// core.Boot, or by a plugin in Provision.
//
// A nil value clears it, restoring plain 1:1 accounting.
func SetMultiplier(m Multiplier) {
	multMu.Lock()
	defer multMu.Unlock()
	mult = m
}

// Credit applies the installed multiplier to one announce delta.
//
// Rounds DOWN via integer conversion, and that direction is deliberate on both
// sides: a member is never credited a byte they did not upload, and never
// charged a byte they did not take. The error is under a byte per announce
// either way.
//
// Negative or NaN factors are ignored rather than trusted. This is arithmetic
// on somebody's ratio, arriving from a plugin the tracker did not write, and a
// negative multiplier would silently run their account backwards.
func Credit(ctx context.Context, userID int64, infoHash string, up, down int64) (int64, int64) {
	multMu.RLock()
	m := mult
	multMu.RUnlock()
	if m == nil {
		return up, down
	}
	upF, downF := m(ctx, userID, infoHash)
	return scale(up, upF), scale(down, downF)
}

func scale(v int64, f float64) int64 {
	// NaN fails every comparison, so the guard catches it as well as negatives.
	if !(f >= 0) {
		return v
	}
	return int64(float64(v) * f)
}
