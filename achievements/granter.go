package achievements

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The reverse crossing: a REWARD (or admin tooling) awarding an achievement,
// through pluginapi.AchievementGranter. The contract's own comment explains
// the rule this implements — it marks the badge held and does NOT run the
// achievement's reward, because a reward paying an achievement that pays a
// reward is a loop nobody configured on purpose.

// GrantAchievement completes slug for userID if not already held.
//
// paid_at is stamped with the completion: nothing is owed afterwards by
// design (see above), and leaving it NULL would put the row on the repair
// sweep's list, which would then pay the reward this call exists to avoid.
func (p *Plugin) GrantAchievement(ctx context.Context, userID int64, slug string) error {
	if userID <= 0 {
		return fmt.Errorf("achievements: GrantAchievement needs a member, got user %d", userID)
	}
	d, err := p.store.AchievementBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if d == nil {
		// An error, never a guess: a payout line naming a deleted
		// achievement should fail its settle loudly rather than mint
		// nothing in silence.
		return fmt.Errorf("achievements: no achievement %q", slug)
	}
	err = p.store.CompleteAchievement(ctx, d.ID, userID, true)
	if err == ErrAlreadyCompleted {
		// Granting a held achievement is a no-op, not an error — the
		// contract says so, and an idempotent payout handler depends on it.
		return nil
	}
	if err != nil {
		return err
	}
	// A direct award is live, not history: announce it like any other
	// completion. Paid=true because paid_at is stamped — the "payment" of a
	// directly-granted badge is definitionally settled.
	p.announce(ctx, *d, userID, true)
	return nil
}

var _ pluginapi.AchievementGranter = (*Plugin)(nil)

// registerGranter publishes the capability, described so /admin/plugins can
// answer "what is this and am I meant to call it or supply it" without
// reading source.
func (p *Plugin) registerGranter(c *core.Core) error {
	return c.RegisterDef(core.ExtensionDef{
		Name:    pluginapi.AchievementGranterName,
		Summary: "complete a named achievement for a member directly; the achievement's own reward is deliberately NOT paid (loop avoidance — see pluginapi.AchievementGranter)",
		Kind:    core.ExtService,
		Stable:  true,
	}, pluginapi.AchievementGranter(p))
}
