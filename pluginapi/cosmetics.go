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
	"sync"
	"time"

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

// ---------------------------------------------------------------------------
// Rendering a name
//
// The cache lives HERE, beside the contract, rather than in the host that first
// needed it — because the host is not the only place a username is drawn. The
// host's user-tag covers listings, profiles and the forum's activity panels;
// the comments plugin draws its own authors; the next plugin with a member list
// will draw its own too. Every one of them wants the same answer to the same
// question, and a second copy of this cache is a second staleness window and a
// second query load.
//
// WHY A CACHE AT ALL, which is not this codebase's usual instinct. The lookup
// happens once per NAME per page — forty on a listing, a hundred on a forum
// page — and the call sites are templates and template helpers, which get no
// request context to memoise against. So: one small map on a short timer.
//
// The map is naturally tiny. It holds only members currently WEARING an effect,
// which on any real site is a fraction of a percent of the user table, and a
// site without the plugin caches an empty map where every lookup is a miss
// against nothing.

// fxTTL is how stale a rendered name may be.
//
// Five seconds, which is the honest trade rather than a tuned number: a member
// who has just equipped something sees their own page immediately (it renders
// from the database, not from here), and everybody else seeing it within five
// seconds is indistinguishable from instant. Longer starts to read as "it did
// not work" to the person watching their own name in the header.
const fxTTL = 5 * time.Second

var fxCache struct {
	mu       sync.RWMutex
	names    map[string]string
	loadedAt time.Time
}

// NameClass returns the effect class for a username, or "" — for anybody
// wearing nothing, for every name on a site without the plugin, and for a
// stored slug the catalogue no longer has.
//
// The hot path is a read lock, a time comparison and a map miss.
func NameClass(c *core.Core, name string) string {
	if name == "" {
		return ""
	}
	fxCache.mu.RLock()
	fresh := time.Since(fxCache.loadedAt) < fxTTL
	slug := fxCache.names[name]
	fxCache.mu.RUnlock()
	if fresh {
		return EffectClass(slug)
	}
	return EffectClass(refreshNameCache(c, name))
}

// refreshNameCache reloads the map and returns this name's slug from the fresh
// copy.
//
// The double-check under the write lock is what stops a burst of names on one
// page each triggering a reload: the first through does the work, the rest find
// it already fresh and read the map they were going to read anyway.
func refreshNameCache(c *core.Core, name string) string {
	fxCache.mu.Lock()
	defer fxCache.mu.Unlock()
	if time.Since(fxCache.loadedAt) < fxTTL {
		return fxCache.names[name]
	}
	res, ok := Cosmetics(c)
	if !ok {
		// No plugin. Stamp the clock anyway so a site without cosmetics does a
		// registry lookup every five seconds rather than once per name on
		// every page.
		fxCache.loadedAt = time.Now()
		fxCache.names = nil
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	names, err := res.EquippedNames(ctx)
	// The clock moves either way. A failed read must not make every subsequent
	// render retry the same failing query for every name on the page — being
	// wrong here costs decoration, and hammering a struggling database costs
	// the site.
	fxCache.loadedAt = time.Now()
	if err != nil {
		return fxCache.names[name]
	}
	fxCache.names = names
	return names[name]
}
