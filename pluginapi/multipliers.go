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
// reading for anything bonus-shaped.
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
