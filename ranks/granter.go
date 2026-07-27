package ranks

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// rankGranter implements pluginapi.RankGranter over the groups store.
// Published on the core extension registry at Provision (under
// pluginapi.RankGranterName) so the store — and any future donation-reward
// orchestrator — can award ranks without importing this package. Grant-only:
// the caller debits currency first.
//
// The (userID, rankID) contract is unchanged by the groups move because group
// ids were preserved from user_ranks: store.items.reward_ref holds those ids as
// unvalidated free text, so renumbering would have silently granted the wrong
// thing. rankID is a group id.
type rankGranter struct {
	store Store
	ents  *entSync
	errs  core.ErrorReporter
}

var _ pluginapi.RankGranter = (*rankGranter)(nil)

func (g *rankGranter) GrantRank(ctx context.Context, userID, rankID int, dur time.Duration) (string, error) {
	grp, err := g.store.Group(ctx, rankID)
	if err != nil {
		return "", fmt.Errorf("rank %d not found: %w", rankID, err)
	}
	// A non-positive duration would subscribe the user to a rank that has
	// already expired: the caller has taken their points and they receive
	// nothing. Fall back to the group's own configured duration, and to 30
	// days if that is unset too.
	//
	// This guard used to live in the profile's buy-rank handler, which is
	// gone now that the store owns the purchase path — the store passes an
	// item's reward_days, which a mis-typed catalog row can leave at 0. It
	// belongs here rather than in the store: this capability owns what a
	// rank subscription means, and every caller gets it.
	if dur <= 0 {
		days := grp.DurationDays
		if days < 1 {
			days = 30
		}
		dur = time.Duration(days) * 24 * time.Hour
	}

	// "extended" when the user already holds this exact rank, mirroring
	// the label the old profile buy-flow recorded.
	action := "purchased"
	if existing, err := g.store.ActiveMembership(ctx, userID); err == nil && existing != nil && existing.GroupID == rankID {
		action = "extended"
	}
	if err := g.store.AddMember(ctx, userID, rankID, dur); err != nil {
		return "", fmt.Errorf("subscribe: %w", err)
	}
	// Grant the group's entitlements to match the new membership. This is how
	// the purchase actually takes effect for the buyer: since Stage 3.2 the
	// daily download and API quotas are READ from these rows, so a miss means
	// the user paid and stayed on the free tier. It is still best-effort — the
	// membership is written and the boot rebuild repairs a gap, so failing the
	// whole grant here would be worse — but it is reported, because a silent
	// miss is invisible until the user complains.
	//
	// Granting also invalidates core's cached resolution for this user, which
	// is what replaced the host limits-cache invalidation that used to live
	// here.
	if g.ents != nil && g.ents.ents != nil {
		if err := g.syncGrants(ctx, userID, rankID); err != nil {
			g.errs.Report(ctx, "ranks/grant-entitlements", err)
		}
	}
	_ = g.store.RecordHistory(ctx, userID, &rankID, action,
		fmt.Sprintf("%s rank granted (%s)", grp.Name, dur))
	return grp.Name, nil
}

// syncGrants writes the entitlements the new membership confers, reading the
// authoritative expiry back from the store because AddMember stacks onto an
// unexpired subscription rather than replacing it — the caller's duration is
// not the resulting expiry.
func (g *rankGranter) syncGrants(ctx context.Context, userID, rankID int) error {
	m, err := g.store.ActiveMembership(ctx, userID)
	if err != nil {
		return fmt.Errorf("read back membership: %w", err)
	}
	// A higher-sort_order membership outranks the one just bought, so
	// ActiveMembership returns that instead. Its grants are already in place
	// and are the more generous ones; the new membership's own grants land at
	// the next boot rebuild. Not an error, and not silent either — this is the
	// documented gap in granting from a single active membership.
	if m == nil || m.GroupID != rankID {
		return nil
	}
	return g.ents.grantMembership(ctx, userID, rankID, m.ExpiresAt)
}
