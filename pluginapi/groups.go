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
