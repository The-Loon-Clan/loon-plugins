// cosmetics.go declares the cosmetic capability: the CATALOGUE of effects a
// site can sell, and the resolver a host asks "who is wearing what".
//
// WHY THE CATALOGUE IS A CONTRACT AND NOT PLUGIN-PRIVATE. A cosmetic is two
// halves that live in different repositories: the plugin sells and records it,
// the HOST draws it, because drawing it means CSS in the host's stylesheet and
// a class on the host's own components. Neither half is any use alone, and a
// slug they disagree about fails silently — the plugin happily sells
// "glow-gold" and the name renders plain. So the list of slugs is the seam, and
// it lives here where both sides import it and a host test can assert its
// stylesheet covers every one.
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

// The slots a cosmetic can occupy — the four places a member is drawn.
//
// A SLOT IS A PLACE, NOT A KIND OF EFFECT, which is why the text effects fit
// two of them: an aura is an aura whether it is on a username or on the title
// under it, and a member who owns one should be able to put it on either
// without buying it twice. What a slot really decides is what the class is
// attached to.
const (
	SlotName    = "name"    // the username, wherever it is drawn
	SlotTitle   = "title"   // the member's own approved title, under the name
	SlotAvatar  = "avatar"  // a frame around the picture
	SlotProfile = "profile" // the ground behind their profile card
)

// Slots in the order a chooser lists them: the two text slots first, because
// they share a catalogue and reading them together is how somebody notices they
// can differ.
var Slots = []string{SlotName, SlotTitle, SlotAvatar, SlotProfile}

// SlotLabel names a slot for a page. Unknown slots come back as themselves
// rather than blank, so a stored value nobody recognises is still legible.
func SlotLabel(slot string) string {
	switch slot {
	case SlotName:
		return "Username"
	case SlotTitle:
		return "Title"
	case SlotAvatar:
		return "Avatar frame"
	case SlotProfile:
		return "Profile background"
	}
	return slot
}

// Effect is one thing a member can wear.
type Effect struct {
	// Slug is the stable key, stored in a purchase and written into the
	// rendered class as fx-<slug>. Never reuse one for a different look.
	Slug string
	// Label is what the shop and the member's own page call it.
	Label string
	// Description is one line saying what it looks like, for somebody choosing
	// between a dozen of them in a list.
	Description string

	// Slots are where this effect may be worn. More than one for the text
	// effects, which work identically on a username and on a title.
	Slots []string

	// Tinted says the effect carries its OWN colour and therefore overrides
	// whatever colour the thing already had.
	//
	// This distinction is the whole reason the catalogue has a field for it. A
	// rank can tint a username (pluginapi.Badge.TitleColor), and a bought
	// effect that always painted itself gold would quietly delete that — a
	// staff colour, an earned rank colour, gone because somebody bought a
	// glow. So the untinted effects work in whatever colour is already there
	// and compose; the tinted ones are the deliberate exception, and the member
	// choosing one can see in the preview what it does.
	Tinted bool

	// Animated marks an effect that moves. The host must not run it under
	// prefers-reduced-motion — see the stylesheet — and a member's own page
	// says which ones move, because "it does nothing on my machine" otherwise
	// looks like a broken purchase.
	Animated bool
}

// FitsSlot reports whether this effect may be worn in a slot.
func (e Effect) FitsSlot(slot string) bool {
	for _, s := range e.Slots {
		if s == slot {
			return true
		}
	}
	return false
}

// textSlots is the pair every text effect fits.
var textSlots = []string{SlotName, SlotTitle}

// Effects is the whole catalogue, grouped by slot and within a slot listed
// still-before-moving, because somebody scanning it is deciding how loud they
// want to be.
//
// Sixteen: eight text effects, four frames, four grounds. A cosmetic system's
// failure mode is a hundred near-identical glows nobody can tell apart in a
// list, and every one of these is distinguishable at a glance at the size it is
// actually seen — a table row, a 30px avatar — which is the only test that
// matters.
var Effects = []Effect{
	// ---- text: a username, or the title under it -------------------------
	{
		Slug:        "aura",
		Label:       "Aura",
		Description: "A soft halo in whatever colour the text already is.",
		Slots:       textSlots,
	},
	{
		Slug:        "glow-gold",
		Label:       "Gold aura",
		Description: "A warm gold halo. Replaces the colour.",
		Slots:       textSlots,
		Tinted:      true,
	},
	{
		Slug:        "glow-ice",
		Label:       "Ice aura",
		Description: "A cold blue halo. Replaces the colour.",
		Slots:       textSlots,
		Tinted:      true,
	},
	{
		Slug:        "glow-ember",
		Label:       "Ember aura",
		Description: "A red halo with heat in it. Replaces the colour.",
		Slots:       textSlots,
		Tinted:      true,
	},
	{
		Slug:        "pulse",
		Label:       "Pulse",
		Description: "The halo breathes, slowly.",
		Slots:       textSlots,
		Animated:    true,
	},
	{
		Slug:        "shimmer",
		Label:       "Shimmer",
		Description: "A band of light sweeps across the letters.",
		Slots:       textSlots,
		Tinted:      true,
		Animated:    true,
	},
	{
		Slug:        "rainbow",
		Label:       "Rainbow",
		Description: "Cycles through the spectrum. As subtle as it sounds.",
		Slots:       textSlots,
		Tinted:      true,
		Animated:    true,
	},
	{
		Slug:        "sparkle",
		Label:       "Sparkle",
		Description: "Motes drift up off the letters.",
		Slots:       textSlots,
		Animated:    true,
	},

	// ---- avatar frames ---------------------------------------------------
	{
		Slug:        "frame-gold",
		Label:       "Gold frame",
		Description: "A gold ring around your picture.",
		Slots:       []string{SlotAvatar},
		Tinted:      true,
	},
	{
		Slug:        "frame-ice",
		Label:       "Ice frame",
		Description: "A pale blue ring around your picture.",
		Slots:       []string{SlotAvatar},
		Tinted:      true,
	},
	{
		Slug:        "frame-ember",
		Label:       "Ember frame",
		Description: "A hot ring, brighter at the bottom.",
		Slots:       []string{SlotAvatar},
		Tinted:      true,
	},
	{
		Slug:        "frame-prism",
		Label:       "Prism frame",
		Description: "The ring turns through gold, ice and violet.",
		Slots:       []string{SlotAvatar},
		Tinted:      true,
		Animated:    true,
	},

	// ---- profile grounds -------------------------------------------------
	{
		Slug:        "bg-aurora",
		Label:       "Aurora",
		Description: "Green and violet light behind your profile card.",
		Slots:       []string{SlotProfile},
		Tinted:      true,
	},
	{
		Slug:        "bg-ember",
		Label:       "Embers",
		Description: "A warm glow rising from the bottom of the card.",
		Slots:       []string{SlotProfile},
		Tinted:      true,
	},
	{
		Slug:        "bg-deep",
		Label:       "Deep",
		Description: "A cold gradient, darkest at the top.",
		Slots:       []string{SlotProfile},
		Tinted:      true,
	},
	{
		Slug:        "bg-drift",
		Label:       "Drift",
		Description: "Soft colour moving slowly behind the card.",
		Slots:       []string{SlotProfile},
		Tinted:      true,
		Animated:    true,
	},
}

// EffectsFor returns the catalogue entries a slot can wear, in catalogue order.
func EffectsFor(slot string) []Effect {
	out := make([]Effect, 0, len(Effects))
	for _, e := range Effects {
		if e.FitsSlot(slot) {
			out = append(out, e)
		}
	}
	return out
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

// EffectClass is the class the host puts on a rendered element.
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

// Worn is what one member has on: slot -> effect slug.
type Worn map[string]string

// Title is a member's own words, after staff have passed them.
//
// Its own type rather than a string on Worn because it is a different KIND of
// thing: an effect comes from a fixed catalogue and a title is text somebody
// typed, which is why one is bought and worn and the other is bought,
// submitted, and approved.
type Title struct {
	Text string
	// FX is the effect worn in the title slot, resolved alongside so a caller
	// rendering the title has everything in one place.
	FX string
}

// CosmeticResolver answers who is wearing what.
type CosmeticResolver interface {
	// Equipped returns username -> Worn for every member wearing anything.
	//
	// THE WHOLE MAP, not a per-name lookup, and that is the design rather than
	// laziness. A listing page renders forty names and a forum page a hundred;
	// a lookup per name would be a hundred round trips for a feature that is
	// decoration. The map is also naturally tiny — it holds only members who
	// have EQUIPPED something, which on any real site is a fraction of a
	// percent — so the common case is a handful of rows and a miss is free.
	//
	// Keyed by USERNAME because the rendering site has a name and nothing else:
	// the user-tag and avatar templates are called with a display name from
	// forty different view models, most of which never carried an id.
	Equipped(ctx context.Context) (map[string]Worn, error)

	// ApprovedTitles returns username -> the words staff have passed. Only
	// approved ones: a pending title is not published anywhere, which is the
	// whole point of there being a queue.
	ApprovedTitles(ctx context.Context) (map[string]string, error)
}

// Cosmetics resolves the registered implementation. Absent is normal — a site
// without the plugin draws everything plain, which is exactly right.
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
// Drawing a member
//
// The cache lives HERE, beside the contract, rather than in the host that first
// needed it — because the host is not the only place a member is drawn. The
// host's user-tag and avatar cover listings, profiles and the forum's activity
// panels; the comments plugin draws its own authors; the next plugin with a
// member list will draw its own too. Every one of them wants the same answer to
// the same question, and a second copy of this cache is a second staleness
// window and a second query load.
//
// WHY A CACHE AT ALL, which is not this codebase's usual instinct. The lookup
// happens once per SLOT per member per page — forty names on a listing, plus
// their avatars — and the call sites are templates and template helpers, which
// get no request context to memoise against. So: one small map on a short
// timer.
//
// The map is naturally tiny. It holds only members currently wearing something,
// which on any real site is a fraction of a percent of the user table, and a
// site without the plugin caches an empty map where every lookup is a miss
// against nothing.

// fxTTL is how stale a rendered member may be.
//
// Five seconds, which is the honest trade rather than a tuned number: a member
// who has just equipped something sees their own page immediately (it renders
// from the database, not from here), and everybody else seeing it within five
// seconds is indistinguishable from instant. Longer starts to read as "it did
// not work" to the person watching their own name in the header.
const fxTTL = 5 * time.Second

var fxCache struct {
	mu       sync.RWMutex
	worn     map[string]Worn
	titles   map[string]string
	loadedAt time.Time
}

// SlotClass returns the effect class for one member's one slot, or "".
//
// The hot path is a read lock, a time comparison and a map miss.
func SlotClass(c *core.Core, slot, name string) string {
	if name == "" || slot == "" {
		return ""
	}
	fxCache.mu.RLock()
	fresh := time.Since(fxCache.loadedAt) < fxTTL
	slug := fxCache.worn[name][slot]
	fxCache.mu.RUnlock()
	if !fresh {
		refreshCosmeticCache(c)
		fxCache.mu.RLock()
		slug = fxCache.worn[name][slot]
		fxCache.mu.RUnlock()
	}
	e, ok := EffectBySlug(slug)
	// The slot is checked at render as well as at equip. A row written when an
	// effect fitted a slot it no longer fits would otherwise keep drawing in
	// the wrong place forever, and the catalogue is the thing that moved.
	if !ok || !e.FitsSlot(slot) {
		return ""
	}
	return EffectClass(slug)
}

// NameClass is the common case: the effect on a username.
func NameClass(c *core.Core, name string) string { return SlotClass(c, SlotName, name) }

// MemberTitle returns a member's approved title and the effect worn on it.
// Empty text means they have none, which is almost everybody.
func MemberTitle(c *core.Core, name string) Title {
	if name == "" {
		return Title{}
	}
	fxCache.mu.RLock()
	fresh := time.Since(fxCache.loadedAt) < fxTTL
	text := fxCache.titles[name]
	fxCache.mu.RUnlock()
	if !fresh {
		refreshCosmeticCache(c)
		fxCache.mu.RLock()
		text = fxCache.titles[name]
		fxCache.mu.RUnlock()
	}
	if text == "" {
		return Title{}
	}
	return Title{Text: text, FX: SlotClass(c, SlotTitle, name)}
}

// refreshCosmeticCache reloads both maps.
//
// The double-check under the write lock is what stops a burst of names on one
// page each triggering a reload: the first through does the work, the rest find
// it already fresh and read the maps they were going to read anyway.
func refreshCosmeticCache(c *core.Core) {
	fxCache.mu.Lock()
	defer fxCache.mu.Unlock()
	if time.Since(fxCache.loadedAt) < fxTTL {
		return
	}
	res, ok := Cosmetics(c)
	if !ok {
		// No plugin. Stamp the clock anyway so a site without cosmetics does a
		// registry lookup every five seconds rather than once per name on
		// every page.
		fxCache.loadedAt = time.Now()
		fxCache.worn, fxCache.titles = nil, nil
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	worn, wErr := res.Equipped(ctx)
	titles, tErr := res.ApprovedTitles(ctx)
	// The clock moves either way. A failed read must not make every subsequent
	// render retry the same failing query for every name on the page — being
	// wrong here costs decoration, and hammering a struggling database costs
	// the site. Each half is kept independently, so one failing does not
	// discard the other's good copy.
	fxCache.loadedAt = time.Now()
	if wErr == nil {
		fxCache.worn = worn
	}
	if tErr == nil {
		fxCache.titles = titles
	}
}
