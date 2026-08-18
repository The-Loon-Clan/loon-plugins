// medals.go declares the contracts around the medals plugin — the display
// cabinet: collectible badges a member buys with points or is paid by a
// reward, wears (or does not) on their profile, and which MAY carry a bonus
// a host chooses to honour.
package pluginapi

import "context"

// MedalGranterName is the extension-registry key the medals plugin publishes
// its granter under. The host's rewards.payout.medal handler consumes it
// LAZILY at settle time (the achievements pattern), so registration order
// never matters and a medal-less host settles medal payouts as a no-op.
const MedalGranterName = "medals.granter"

// MedalGranter awards one medal. Idempotent: granting an already-held medal
// is the same nothing, and an unknown slug is the granter's own quiet no-op
// — the tolerance every payout handler in this tree extends to old data.
type MedalGranter interface {
	GrantMedal(ctx context.Context, userID int64, slug string) error
}

// WornMedalsName is where the medals plugin publishes the profile's
// question: which medals is this member displaying? The HOST renders the
// answer (icons in the profile header); the plugin owns the cabinet.
const WornMedalsName = "medals.worn"

// WornMedal is one displayed medal, ready to draw.
//
// Two ways to draw it and exactly one is ever set, so a host template picks
// with an {{if}} rather than having to guess from the string's shape:
//
//	Sprite  a host sprite id, drawn as <svg><use href="#…">
//	Icon    an image URL, drawn as <img src="…">
//
// Sprite exists because most medals never get an upload, and a medal with no
// picture at all is a worse badge than a generic one. The plugin fills it from
// the medal's slug when its operator has not chosen — see medals/icons.go.
type WornMedal struct {
	Name   string
	Icon   string // an image URL
	Sprite string // a host sprite id
}

// WornMedalsFunc answers for one member. Registered AS this type — a bare
// func never survives the registry's type assertion.
type WornMedalsFunc func(ctx context.Context, userID int64) ([]WornMedal, error)

// MedalBonusName is where the medals plugin publishes each member's summed
// bonus percentage across WORN medals. DELIBERATELY consumer-less by
// default: whether medals carry any mechanical benefit is a site-culture
// decision — some sites want a 5% bonus-points medal, some want medals to
// be nothing but medals — so the plugin only ever answers, and a host that
// wants the bonus applies it where its points are earned. Zero for a member
// wearing nothing, or nothing with a bonus.
const MedalBonusName = "medals.bonus"

// MedalBonusFunc reports the summed bonus percent for a member.
type MedalBonusFunc func(ctx context.Context, userID int64) (int, error)
