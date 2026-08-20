// cosmetics.go declares the name-effect capability: the CATALOGUE of effects a
// site can sell, and the resolver a host asks "who is wearing one".
//
// WHY THE CATALOGUE IS A CONTRACT AND NOT PLUGIN-PRIVATE. An effect is two
// halves that live in different repositories: the plugin sells and records it,
// the HOST draws it, because drawing it means CSS in the host's stylesheet and
// a class on the host's user-tag. Neither half is any use alone, and a slug
// they disagree about fails silently — the plugin happily sells "glow-gold" and
// the name renders plain. So the list of slugs is the seam, and it lives here
// where both sides import it and a host test can assert its stylesheet covers
// every one.
//
// See anidb.go for the package-level contract discipline this follows.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// CosmeticsName is the Core extension-registry key under which the cosmetics
// plugin publishes its CosmeticResolver.
const CosmeticsName = "cosmetics.effects"

// Effect is one thing a name can wear.
type Effect struct {
	// Slug is the stable key, stored in a purchase and written into the
	// rendered class as fx-<slug>. Never reuse one for a different look.
	Slug string
	// Label is what the shop and the member's own page call it.
	Label string
	// Description is one line saying what it looks like, for somebody choosing
	// between eight of them in a list.
	Description string

	// Tinted says the effect carries its OWN colour and therefore overrides
	// whatever colour the name already had.
	//
	// This distinction is the whole reason the catalogue has a field for it. A
	// rank can tint a username (pluginapi.Badge.TitleColor), and a bought
	// effect that always painted itself gold would quietly delete that — a
	// staff colour, an earned rank colour, gone because somebody bought a
	// glow. So the untinted effects glow in whatever colour the name ALREADY
	// is, and compose; the tinted ones are the deliberate exception, and the
	// member choosing one can see in the preview what it does.
	Tinted bool

	// Animated marks an effect that moves. The host must not run it under
	// prefers-reduced-motion — see the stylesheet — and a member's own page
	// says which ones move, because "it does nothing on my machine" otherwise
	// looks like a broken purchase.
	Animated bool
}

// Effects is the whole catalogue, in the order a shop and a chooser list it:
// the still ones first, then the moving ones, because somebody scanning the
// list is deciding how loud they want to be.
//
// Eight, deliberately. A cosmetic system's failure mode is a hundred
// near-identical glows nobody can tell apart in a list, and every one of these
// is distinguishable at a glance in a table row of usernames — which is the
// only place anybody will ever see it.
var Effects = []Effect{
	{
		Slug:        "aura",
		Label:       "Aura",
		Description: "A soft halo in whatever colour your name already is.",
	},
	{
		Slug:        "glow-gold",
		Label:       "Gold aura",
		Description: "A warm gold halo. Replaces your name's colour.",
		Tinted:      true,
	},
	{
		Slug:        "glow-ice",
		Label:       "Ice aura",
		Description: "A cold blue halo. Replaces your name's colour.",
		Tinted:      true,
	},
	{
		Slug:        "glow-ember",
		Label:       "Ember aura",
		Description: "A red halo with heat in it. Replaces your name's colour.",
		Tinted:      true,
	},
	{
		Slug:        "pulse",
		Label:       "Pulse",
		Description: "The halo breathes, slowly.",
		Animated:    true,
	},
	{
		Slug:        "shimmer",
		Label:       "Shimmer",
		Description: "A band of light sweeps across the letters.",
		Tinted:      true,
		Animated:    true,
	},
	{
		Slug:        "rainbow",
		Label:       "Rainbow",
		Description: "Cycles through the spectrum. As subtle as it sounds.",
		Tinted:      true,
		Animated:    true,
	},
	{
		Slug:        "sparkle",
		Label:       "Sparkle",
		Description: "Motes drift up off the letters.",
		Animated:    true,
	},
}

// EffectBySlug resolves one entry. The second return is what a caller checks
// before rendering a class: a slug that is not in the catalogue must draw
// NOTHING, because the alternative is a stored typo becoming a class name on
// every page the member appears on.
func EffectBySlug(slug string) (Effect, bool) {
	for _, e := range Effects {
		if e.Slug == slug {
			return e, true
		}
	}
	return Effect{}, false
}

// EffectClass is the class the host puts on a rendered name.
//
// Built HERE rather than in a host template so the plugin selling an effect and
// the stylesheet drawing it cannot disagree about the spelling. Returns empty
// for anything not in the catalogue.
func EffectClass(slug string) string {
	if _, ok := EffectBySlug(slug); !ok {
		return ""
	}
	return "fx-" + slug
}

// CosmeticResolver answers who is wearing what.
type CosmeticResolver interface {
	// EquippedNames returns username -> effect slug for every member currently
	// wearing one.
	//
	// THE WHOLE MAP, not a per-name lookup, and that is the design rather than
	// laziness. A listing page renders forty names and a forum page a hundred;
	// a lookup per name would be a hundred round trips for a feature that is
	// decoration. The map is also naturally tiny — it holds only members who
	// have EQUIPPED something, which on any real site is a fraction of a
	// percent — so the common case is a handful of rows and a miss is free.
	//
	// Keyed by USERNAME because the rendering site has a name and nothing else:
	// the user-tag template is called with a display name from forty different
	// view models, most of which never carried an id.
	EquippedNames(ctx context.Context) (map[string]string, error)
}

// Cosmetics resolves the registered implementation. Absent is normal — a site
// without the plugin renders plain names, which is exactly right.
func Cosmetics(c *core.Core) (CosmeticResolver, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(CosmeticsName)
	if !ok {
		return nil, false
	}
	r, ok := v.(CosmeticResolver)
	return r, ok
}
