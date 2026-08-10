package pluginapi

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Temporary boosts to a member's daily quotas.
//
// The host owns quotas — it is the thing that refuses a request when one is
// spent — so the host answers what the current multiplier is. Plugins consume
// this to SHOW the number: a ranks table that lists "10,000 API calls a day"
// during a week when everyone actually has 100,000 is worse than showing
// nothing, because a member reads it and files a bug.
//
// Same shape of seam as the tracker's Multiplier, and for the same reason: the
// thing being multiplied should not have to learn why. Here the direction is
// reversed — the host computes and plugins read — because the quota and the
// event window that boosts it are both host-side facts, and a plugin asking
// "am I allowed more today" would be asking about someone else's policy.

// LimitBoostName is the extension-registry key. Consumers Lookup it and
// type-assert to LimitBooster; absence means no boost machinery is installed,
// which a display MUST treat as "no boost" rather than as an error.
const LimitBoostName = "limits.boost"

// APIBoost is the multiplier currently applied to every member's daily API
// quota, and enough context to say so on a page.
//
// Factor is a whole number rather than a float on purpose: a quota is a count
// of requests, "×2.5" of 10,000 invites a rounding argument nobody needs, and
// every real promotion is an integer multiple.
type APIBoost struct {
	// Factor multiplies the resolved daily API limit. 1 means no boost — the
	// zero value of this struct is deliberately NOT valid as a factor, so a
	// consumer that forgets to check Active cannot silently multiply by zero
	// and report that nobody may call the API. Producers must never return 0.
	Factor int
	// Slug and Name identify the event driving the boost, for display. Empty
	// when nothing is active.
	Slug string
	Name string
	// Ends is when the boost stops. Zero means "no end announced" — either
	// perpetual or a window whose close is not worth printing.
	Ends time.Time
}

// Active reports whether a boost is really in effect.
func (b APIBoost) Active() bool { return b.Factor > 1 }

// Apply multiplies one limit, leaving it untouched when no boost is active.
// Centralised so the host's enforcement and a plugin's display cannot disagree
// about the arithmetic — the failure mode being a page that promises a number
// the API then refuses.
func (b APIBoost) Apply(limit int) int {
	if !b.Active() || limit <= 0 {
		return limit
	}
	return limit * b.Factor
}

// LimitBooster is published by the host under LimitBoostName.
type LimitBooster interface {
	// APIBoostNow returns the boost in force. MUST be cheap and MUST NOT
	// block on a network call: it is consulted on the API request path, where
	// it is asked once per request per member.
	APIBoostNow(ctx context.Context) APIBoost
}

// LookupAPIBoost resolves the boost through the registry, degrading to "no
// boost" when nothing publishes one — mirroring the other Lookup helpers, but
// returning a usable value rather than (iface, bool) because every caller
// wants the same fallback and getting that fallback wrong is how a display
// ends up multiplying by zero.
func LookupAPIBoost(ctx context.Context, c *core.Core) APIBoost {
	none := APIBoost{Factor: 1}
	if c == nil {
		return none
	}
	v, ok := c.Lookup(LimitBoostName)
	if !ok {
		return none
	}
	b, ok := v.(LimitBooster)
	if !ok {
		return none
	}
	got := b.APIBoostNow(ctx)
	if got.Factor < 1 {
		// A producer that answered 0 or negative is broken; refusing to honour
		// it is better than reporting that the API is closed.
		got.Factor = 1
	}
	return got
}
