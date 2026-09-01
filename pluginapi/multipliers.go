// multipliers.go is the USER MULTIPLIER system: one vocabulary for every
// temporary or standing factor applied to what a member earns — announce
// credit, points, and whatever dimension comes next — and ONE place where
// the combining rules live.
//
// The shape is many sources, one resolver. Each system that grants
// multipliers (a magic cast, a worn medal, a future event window) registers
// a MultiplierSource under the discoverable prefix; a consumer (the
// tracker's crediting, the rewards engine's points payouts) calls
// ResolveMultiplier and never learns who answered. Adding a source is a
// registration; adding a consumer is a call; neither touches the other —
// which is what keeps all the rules together instead of scattered through
// every pairing.
//
// THE COMBINING RULES, per dimension, and deliberately here rather than in
// any consumer:
//
//	upload    MAX   — promotions: the best offer wins, they do not stack
//	download  MIN   — same rule from the other side; 0 is full freeleech
//	points    SUM   — standing bonuses: 1 + Σ(factor−1), so a +5% medal
//	                  and a +10% event are +15%, the way every tracker
//	                  tradition stacks them
//
// An unknown dimension combines like points, which is the conservative
// reading for anything bonus-shaped — and that qualifier is load-bearing.
// EVERY rule above assumes the source is OFFERING the member something, so
// "the best offer wins" settles the argument. A RESTRICTION cannot be
// expressed here at all, and fails silently in the generous direction if you
// try: neutral leech (download free, upload earns nothing) needs upload×0,
// but `up` starts at 1 and only ever rises, so a source answering 0 loses to
// the floor and neutral silently becomes ordinary freeleech — strictly more
// generous than asked, with nothing reporting a problem. Measured against a
// bare core by the demo host, 2026-09-01, before it reached anyone's ratio.
//
// So restrictions live in the mirror seam below — POLICY FLAGS, combined by
// ANY — and the two algebras are stated together on purpose:
//
//	multipliers  best-of  — an OFFER competes; the most generous one wins
//	flags        ANY      — a RESTRICTION does not compete; one is enough
//
// Modelling a restriction as a multiplier is the category error that hid
// this. If a new dimension is a thing the member LOSES, it is a flag.
package pluginapi

import (
	"context"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// Multiplier dimensions.
const (
	MultUpload   = "upload"
	MultDownload = "download"
	MultPoints   = "points"
)

// MultiplierSourcePrefix is where sources register:
// "multipliers.source.<system>" — magic, medals, whatever comes next.
const MultiplierSourcePrefix = "multipliers.source."

// MultiplierContext is what a source may condition on. InfoHash is empty
// for dimensions that are not about a torrent.
type MultiplierContext struct {
	UserID   int64
	InfoHash string
}

// MultiplierSource answers one member's factor for one dimension.
// ok=false is "no opinion" — neutral, not zero. Called on hot paths
// (announces, payouts): implementations must be one cheap read, and an
// error is treated as no opinion — earning must never fail over a bonus.
type MultiplierSource interface {
	Factor(ctx context.Context, dim string, mc MultiplierContext) (factor float64, ok bool, err error)
}

// ResolveMultiplier combines every registered source's answer for one
// dimension by that dimension's rule. 1 with no sources, always ≥ 0.
func ResolveMultiplier(ctx context.Context, c *core.Core, dim string, mc MultiplierContext) float64 {
	if c == nil {
		return 1
	}
	up, down, bonus := 1.0, 1.0, 0.0
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, MultiplierSourcePrefix) {
			continue
		}
		v, ok := c.Lookup(name)
		if !ok {
			continue
		}
		src, ok := v.(MultiplierSource)
		if !ok {
			continue
		}
		f, has, err := src.Factor(ctx, dim, mc)
		if err != nil || !has || !(f >= 0) { // the NaN/negative guard, same as the tracker's
			continue
		}
		switch dim {
		case MultUpload:
			if f > up {
				up = f
			}
		case MultDownload:
			if f < down {
				down = f
			}
		default:
			bonus += f - 1
		}
	}
	switch dim {
	case MultUpload:
		return up
	case MultDownload:
		return down
	default:
		f := 1 + bonus
		if f < 0 {
			return 0
		}
		return f
	}
}

// ── Policy flags: the mirror of the multiplier seam ──────────────────────
//
// A flag is a RESTRICTION a member is under, not a bonus they have earned.
// The distinction is not stylistic: it decides the combining rule, and
// getting it wrong is unsafe in one specific direction. An offer that loses
// its argument costs the member a bonus they might have had; a restriction
// that loses its argument hands out credit the operator meant to withhold,
// and the accounting looks entirely normal afterwards.
//
// Hence ANY: one source asserting a flag is enough, and no source can
// out-bid it. There is no "best" restriction to pick.

// PolicyFlagPrefix is where restriction sources register:
// "policy.flag.<system>" — the same discoverable shape as multiplier
// sources, so a reader finds both in one listing of the registry.
const PolicyFlagPrefix = "policy.flag."

// Policy flags.
const (
	// FlagNeutral is neutral leech: the download does not count against the
	// member AND the upload earns them nothing. Both halves, together —
	// that pairing is the whole point, and it is what no multiplier can say,
	// because the upload half is a restriction wearing a promotion's clothes.
	FlagNeutral = "neutral"
)

// PolicySource answers whether one member is under one flag.
//
// ok=false is "no opinion", exactly as in MultiplierSource. Called on the
// same hot paths — an announce runs this per peer per few minutes — so an
// implementation must be one cheap read, and an error is treated as no
// opinion: an economy that cannot answer must not fail an announce.
//
// Note the asymmetry with MultiplierSource, and that it is deliberate: an
// error here means the restriction is NOT applied, i.e. it fails generous.
// That is the safe direction for the member and the honest one for the
// operator, who can see a flag that never fires; the alternative silently
// withholds credit somebody earned, which nobody can see at all.
type PolicySource interface {
	Flag(ctx context.Context, flag string, mc MultiplierContext) (on bool, ok bool, err error)
}

// ResolvePolicyFlag reports whether ANY registered source asserts flag.
//
// False with no sources, which is the dormant default every seam here keeps:
// a host that installs no economy plugin sees no behavioural change.
func ResolvePolicyFlag(ctx context.Context, c *core.Core, flag string, mc MultiplierContext) bool {
	if c == nil {
		return false
	}
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, PolicyFlagPrefix) {
			continue
		}
		v, ok := c.Lookup(name)
		if !ok {
			continue
		}
		src, ok := v.(PolicySource)
		if !ok {
			continue
		}
		on, has, err := src.Flag(ctx, flag, mc)
		if err != nil || !has {
			continue
		}
		if on {
			return true
		}
	}
	return false
}
