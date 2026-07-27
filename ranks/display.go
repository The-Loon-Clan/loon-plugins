package ranks

import (
	"context"
	"sort"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The display half of the groups model (ENTITLEMENTS.md Stage 2.4): what badge
// to draw, as opposed to what a member may do. Access decisions go through
// core.Entitlements and never come near this.
//
// Nothing consumes it yet. Stage 3 moves the five display readers — the profile
// badge, the admin user page, the comment-author colours, and the discord/irc
// chat enrichment — off the legacy tables and onto this, at which point the
// mirror can go.
type groupDisplay struct {
	store Store
}

var _ pluginapi.GroupDisplay = (*groupDisplay)(nil)

func (d *groupDisplay) BadgesFor(ctx context.Context, userID int64) ([]pluginapi.Badge, error) {
	byUser, err := d.BadgesForBatch(ctx, []int64{userID})
	if err != nil {
		return nil, err
	}
	return byUser[userID], nil
}

func (d *groupDisplay) BadgesForBatch(ctx context.Context, userIDs []int64) (map[int64][]pluginapi.Badge, error) {
	if len(userIDs) == 0 {
		return map[int64][]pluginapi.Badge{}, nil
	}
	ids := make([]int, 0, len(userIDs))
	for _, u := range userIDs {
		ids = append(ids, int(u))
	}
	// One store call, not two: this runs on a page render resolving every
	// comment author, and each separate PGStore read would open its own
	// transaction to set the schema search_path. See Store.BadgeData.
	members, groups, err := d.store.BadgeData(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*Group, len(groups))
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
	}

	out := map[int64][]pluginapi.Badge{}
	order := map[int64][]int{} // parallel sort keys: the group's sort_order
	for _, m := range members {
		g, ok := byID[m.GroupID]
		// The contract's one hard rule: an invisible group grants but never
		// shows. Filtering here means no consumer has to know the flag exists.
		if !ok || !g.Visible {
			continue
		}
		uid := int64(m.UserID)
		out[uid] = append(out[uid], pluginapi.Badge{
			Slug: g.Slug, Name: g.Name, Color: g.Color,
			TitleColor: g.TitleColor, Icon: g.Icon,
			// The membership's own expiry, not the group's duration: the
			// profile and admin pages render "expires <date>" beside the badge,
			// and a permanent membership carries nil rather than a zero time.
			ExpiresAt: m.ExpiresAt,
		})
		order[uid] = append(order[uid], g.SortOrder)
	}
	// Most prominent first, so a caller rendering a single badge can take the
	// head without re-deriving precedence. This matches what the legacy
	// GetActiveSubscription did with `ORDER BY sort_order DESC LIMIT 1`.
	for uid, badges := range out {
		keys := order[uid]
		idx := make([]int, len(badges))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] > keys[idx[b]] })
		sorted := make([]pluginapi.Badge, len(badges))
		for i, j := range idx {
			sorted[i] = badges[j]
		}
		out[uid] = sorted
	}
	return out, nil
}

// Catalog is the group set rather than one user's badges — what the host's
// Discord bot maps onto guild roles. Visible only, for the same reason
// BadgesForBatch filters: an entitlement-only group has no badge, and
// projecting it into an external role system would hand it the visibility it
// was hidden to avoid.
//
// ExpiresAt stays nil: a catalog entry describes a group, not a membership.
func (d *groupDisplay) Catalog(ctx context.Context) ([]pluginapi.Badge, error) {
	// The empty user set is BadgeData's catalog-only shape, so this pays for
	// one transaction and no entitlement rows.
	_, groups, err := d.store.BadgeData(ctx, nil)
	if err != nil {
		return nil, err
	}
	// The store returns the catalog by (sort_order, id) ascending, and this
	// contract is most-prominent-first like BadgesFor, so walking backwards is
	// the whole sort — no comparison needed.
	out := make([]pluginapi.Badge, 0, len(groups))
	for i := len(groups) - 1; i >= 0; i-- {
		g := &groups[i]
		if !g.Visible {
			continue
		}
		out = append(out, pluginapi.Badge{
			Slug: g.Slug, Name: g.Name, Color: g.Color,
			TitleColor: g.TitleColor, Icon: g.Icon,
		})
	}
	return out, nil
}
