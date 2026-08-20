// adminnav.go lets a plugin put its own admin surface in the host's admin nav.
//
// WHY IT EXISTS. A host builds its admin nav from the views plugins register —
// one entry per SlotAdminPage — plus a hardcoded list of its own pages. That
// covers a plugin whose admin surface is ONE page, which is most of them.
//
// It does not cover a plugin whose admin surface is a route GROUP. SlotAdminPage
// mounts exactly one GET (`/admin/p/<slug>`) plus POST actions; there is no
// nested GET. So wiki, which has topic and post editors, donations, which has
// costs/log/points, and news, which has an edit page, cannot be views — and
// being unable to be views, they were in no nav at all. On 20 Aug 2026,
// seventeen admin routes were served and unreachable from the admin nav: an
// operator found them by knowing the URL.
//
// The fix is NOT to force those pages into the view slot. A route group is the
// right shape for them; what was missing is a way to say "I exist, link me".
// This is that, and it is the CHECKLIST §1 question — "can another plugin
// append to this?" — asked of the admin nav itself, which was a closed set.
//
// It deliberately carries no render function and no access rules. The page is
// already mounted and already gated by whatever mounted it; this contributes a
// LINK and nothing else, so a plugin cannot accidentally widen its own access
// by appearing in a menu.
package pluginapi

import (
	"sort"

	"github.com/the-loon-clan/loon/core"
)

// AdminNavPrefix is the registry prefix a plugin registers its entries under.
// One key per plugin: `admin.nav.<plugin>`.
const AdminNavPrefix = "admin.nav."

// AdminNavEntry is one link in the host's admin nav.
type AdminNavEntry struct {
	// Href is the path, absolute and host-relative ("/admin/wiki"). It must be
	// a route the plugin has already mounted — this contributes a link, not a
	// route, and a link to nothing is worse than no link.
	Href string
	// Label is what the operator reads. Short: it sits in a bar with a dozen
	// others.
	Label string
	// Group is a placement hint, exactly as core.NavHint.Group is for site
	// pages: the plugin suggests, the host decides.
	//
	// It always affects ORDER — entries sort by group, then weight. Whether it
	// also becomes a visible heading is the host's choice, and the reference
	// host currently renders one flat bar and ignores it as a label. Do not
	// rely on it to hide anything.
	Group string
	// Weight orders entries within a group; lower first, ties keep registration
	// order. Zero is fine and common.
	Weight int
	// Feature, when set, names the core.Feature this entry belongs to. The host
	// hides the link when that feature is off — the same rule a view's Feature
	// field gets, so a plugin behind a toggle does not leave a dead link in the
	// bar when it is switched off.
	Feature string
}

// AdminNavSource is what a plugin registers. A func type rather than an
// interface because the common case is one closure returning a fixed slice,
// and an interface would make every plugin declare a method on a type that
// exists for no other reason.
type AdminNavSource func() []AdminNavEntry

// RegisterAdminNav publishes a plugin's admin nav entries.
//
// Call it in Provision, next to the routes it describes, so the link and the
// route it points at are added in the same place and stay in step.
func RegisterAdminNav(c *core.Core, plugin string, fn AdminNavSource) error {
	return c.Register(AdminNavPrefix+plugin, fn)
}

// AdminNavEntries collects every contributed entry, sorted by group, then
// weight, then the order they were registered in.
//
// Entries with an empty Href or Label are DROPPED rather than rendered: a
// blank item in a nav bar is a bug the operator cannot diagnose, and a plugin
// that half-filled its entry should get nothing rather than a mystery.
func AdminNavEntries(c *core.Core) []AdminNavEntry {
	type ordered struct {
		e AdminNavEntry
		i int
	}
	var all []ordered
	for _, fn := range ContributedValues[AdminNavSource](c, AdminNavPrefix) {
		if fn == nil {
			continue
		}
		for _, e := range fn() {
			if e.Href == "" || e.Label == "" {
				continue
			}
			all = append(all, ordered{e, len(all)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].e.Group != all[j].e.Group {
			return all[i].e.Group < all[j].e.Group
		}
		if all[i].e.Weight != all[j].e.Weight {
			return all[i].e.Weight < all[j].e.Weight
		}
		return all[i].i < all[j].i
	})
	out := make([]AdminNavEntry, 0, len(all))
	for _, o := range all {
		out = append(out, o.e)
	}
	return out
}
