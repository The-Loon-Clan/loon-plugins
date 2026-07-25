package forum

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Per-category access gates.
//
// Each category carries three gates — See (appears in listings), Read (open
// the category and its threads), Write (create threads / reply) — and each
// gate has two requirement axes composed with OR, mirroring the host's
// permission model ("staff-role OR earned-tier"):
//
//   - a minimum ROLE name ("all" = everyone including anonymous; else the
//     viewer must be logged in at or above it), and
//   - a minimum rank TIER (the user_display contract's reputation_tier;
//     0 disables the axis — otherwise everyone would pass).
//
// So see_role='admin' + see_tier=2 reads "admins OR rank ≥ 2". The defaults
// (all/all/user, tiers 0) reproduce the pre-gating behaviour exactly:
// everyone sees and reads, any logged-in user writes.

// gateRoles orders the role names a gate may require. "all" admits anonymous
// viewers; the rest map onto core.Role so AtLeast semantics carry over.
var gateRoles = map[string]core.Role{
	"user":        core.RoleUser,
	"contributor": core.RoleContributor,
	"mod":         core.RoleMod,
	"admin":       core.RoleAdmin,
}

// gateRoleList feeds the admin form's selects, ordered.
var gateRoleList = []string{"all", "user", "contributor", "mod", "admin"}

// viewerCaps is what gate checks need to know about the requester.
type viewerCaps struct {
	loggedIn bool
	role     core.Role
	tier     int
}

// passes evaluates one gate. Unknown role names (a hand-edited row) fail
// closed to admin-only rather than open.
func (v viewerCaps) passes(minRole string, minTier int) bool {
	switch minRole {
	case "", "all":
		return true
	default:
		need, ok := gateRoles[minRole]
		if !ok {
			need = core.RoleAdmin
		}
		if v.loggedIn && v.role >= need {
			return true
		}
	}
	return minTier > 0 && v.tier >= minTier
}

// canSee / canRead / canWrite evaluate a category's gates for a viewer.
func (v viewerCaps) canSee(c *ForumCategory) bool   { return v.passes(c.SeeRole, c.SeeTier) }
func (v viewerCaps) canRead(c *ForumCategory) bool  { return v.passes(c.ReadRole, c.ReadTier) }
func (v viewerCaps) canWrite(c *ForumCategory) bool { return v.passes(c.WriteRole, c.WriteTier) }

// viewer resolves the requester's caps: session user from core.Auth, rank
// tier from the user_display contract (one PK lookup; anonymous viewers are
// tier 0 without a query). Tier lookup failure degrades to 0 — the role axis
// still applies, so a DB blip narrows access rather than widening it.
func (h *Handlers) viewer(c *gin.Context) viewerCaps {
	u, ok := h.auth.CurrentUser(c)
	if !ok {
		return viewerCaps{}
	}
	caps := viewerCaps{loggedIn: true, role: u.Role}
	if t, err := h.store.ViewerTier(c.Request.Context(), u.ID); err == nil {
		caps.tier = t
	}
	return caps
}

// visibleCategories filters a listing to the categories the viewer may see.
func visibleCategories(cats []*ForumCategory, v viewerCaps) []*ForumCategory {
	out := cats[:0]
	for _, c := range cats {
		if v.canSee(c) {
			out = append(out, c)
		}
	}
	return out
}

// categoryByID is a lookup helper for gate checks that start from a thread.
func (h *Handlers) categoryFor(ctx context.Context, id int) (*ForumCategory, bool) {
	cat, err := h.store.GetForumCategory(ctx, id)
	return cat, err == nil && cat != nil
}
