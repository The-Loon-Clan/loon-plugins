// groups.go declares the membership-DISPLAY capability: the read side of the
// groups/ranks plugin, published via Core.Register and consumed via
// Core.Lookup. See rewards.go for the write side (RankGranter) and anidb.go for
// the package-level contract discipline.
//
// Why display is its own capability, separate from entitlements: a group
// answers two different questions, and conflating them is what made the ranks
// domain impossible to move. "May this user do X?" is an access DECISION and
// belongs to core.Entitlements, which every reader can ask without knowing
// groups exist. "What badge do I draw next to their name?" is a display LABEL
// that only the owning plugin can answer. This contract is the second half —
// deliberately narrow, so a consumer that wants a badge cannot accidentally
// start making authorization decisions from it.
package pluginapi

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// GroupDisplayName is the Core extension-registry key under which the
// groups/ranks plugin publishes its GroupDisplay.
const GroupDisplayName = "groups.display"

// Badge is one group's visual identity for a user. Flat and model-free: the
// host renders it, the plugin owns what it says.
type Badge struct {
	// Slug is the stable key. Names can be edited; slugs are preserved across
	// a rename, so anything persisting a reference should persist this.
	Slug string
	// Name is the label to show.
	Name string
	// Color is the badge class (a Bootstrap contextual name in the current
	// host: "primary", "success"...).
	Color string
	// TitleColor is a hex colour for the username itself, empty when the group
	// does not tint it.
	TitleColor string
	// Icon is an optional glyph identifier; empty for most groups.
	Icon string
	// ExpiresAt is when the membership behind this badge lapses; nil means it
	// does not expire on its own (an assigned staff group, or a permanent
	// grant). Present because the host renders "expires Jan 02, 2006" beside
	// the badge on a profile, and a badge is already per-membership — a user in
	// two groups has two badges with two expiries, so there is nothing to
	// reconcile.
	//
	// Zero on a Catalog() badge: a catalog entry describes the group, not
	// anyone's membership of it.
	ExpiresAt *time.Time
}

// GroupDisplay resolves the badges a user should be shown.
//
// IMPLEMENTATIONS MUST RETURN ONLY VISIBLE GROUPS. A group flagged invisible
// grants its entitlements but deliberately shows no badge — that is what makes
// staff and entitlement-only groups possible — so filtering is part of this
// contract rather than the caller's job. A consumer must never have to know
// the flag exists.
//
// Order is most-prominent first, so a caller rendering a single badge can take
// the head of the slice without re-deriving precedence.
type GroupDisplay interface {
	// BadgesFor returns one user's visible badges. An unknown user is not an
	// error: absence is the empty slice.
	BadgesFor(ctx context.Context, userID int64) ([]Badge, error)

	// BadgesForBatch resolves many users at once, keyed by user id. Users with
	// no visible groups may be absent from the map rather than mapped to an
	// empty slice.
	//
	// This is NOT optional convenience. The host's comment-author query
	// resolves badge colours for every author on a release page in a single
	// round trip; a per-user-only contract would turn that into an N+1 on a
	// page-render path, which is precisely the regression the capability is
	// meant to avoid when the legacy join goes away.
	BadgesForBatch(ctx context.Context, userIDs []int64) (map[int64][]Badge, error)

	// Catalog returns every VISIBLE group as a badge, with no membership
	// attached — what badges exist, rather than who wears one. ExpiresAt is
	// always nil here.
	//
	// It exists for consumers that map groups onto some external notion of a
	// role: the host's Discord bot builds a group -> Discord-role-ID table and
	// needs the set of groups, not one user's. Invisible groups are excluded
	// for the same reason they are excluded everywhere else in this contract —
	// a group with no badge is entitlement-only, and projecting it into an
	// external role system would give it exactly the visibility it was hidden
	// to avoid.
	//
	// Key persisted references off Slug, never Name: names are editable and
	// slugs survive a rename.
	Catalog(ctx context.Context) ([]Badge, error)
}

// GroupAuditName is the Core extension-registry key under which the
// groups/ranks plugin publishes its GroupAudit.
const GroupAuditName = "groups.audit"

// AuditEntry is one membership-history row: what happened to a user's
// membership of a group, and when.
type AuditEntry struct {
	// At is when it happened.
	At time.Time
	// Action is the event kind — "purchased", "extended", "expired",
	// "admin_grant", "admin_revoke". Open-ended on purpose: a consumer that
	// styles known actions should fall through to showing the raw string
	// rather than dropping the row, since the owning plugin may add kinds.
	Action string
	// Group is the group's display name, or a placeholder when the group has
	// since been deleted — history outlives the catalog.
	Group string
	// GroupSlug is the stable key, empty when the group is gone.
	GroupSlug string
	// Details is the human-readable note recorded with the event.
	Details string
}

// GroupAudit exposes membership history for one user.
//
// Separate from GroupDisplay rather than a method on it, and the split is the
// same one that runs through this whole model: GroupDisplay answers "what
// badge do I draw", which every page asks, and is scoped narrowly so a badge
// consumer cannot reach further. Reading a user's membership history is an
// administrative concern with a different audience — it belongs behind its own
// capability so that wanting a badge does not come with the ability to audit.
//
// Absence is a degraded surface, not a boot failure: a host without the owning
// plugin simply has no history to show.
type GroupAudit interface {
	// HistoryFor returns the newest entries first, at most limit of them. A
	// limit <= 0 means the implementation's own default. A user with no
	// history is the empty slice, not an error.
	HistoryFor(ctx context.Context, userID int64, limit int) ([]AuditEntry, error)
}

// LookupGroupAudit resolves the published capability, mirroring
// LookupGroupDisplay.
func LookupGroupAudit(c *core.Core) (GroupAudit, bool) {
	v, ok := c.Lookup(GroupAuditName)
	if !ok {
		return nil, false
	}
	s, ok := v.(GroupAudit)
	return s, ok
}

// LookupGroupDisplay resolves the published capability, mirroring the other
// Lookup helpers so a host resolves it the same typed way rather than
// hand-asserting the Core.Lookup result.
//
// Consumers must treat absence as a degraded capability rather than a boot
// failure: a host running without the groups plugin is legitimate, and a
// missing badge is a cosmetic loss, never an access decision.
func LookupGroupDisplay(c *core.Core) (GroupDisplay, bool) {
	v, ok := c.Lookup(GroupDisplayName)
	if !ok {
		return nil, false
	}
	s, ok := v.(GroupDisplay)
	return s, ok
}
