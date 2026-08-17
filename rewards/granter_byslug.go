package rewards

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The by-slug granter: how the achievements plugin pays an achievement
// without importing this package.
//
// This is the seam that made splitting achievements out possible. The old
// completeAchievementSilent built a grant here IN the completion's
// transaction; across plugins that transaction cannot exist, so the contract
// is idempotence instead — the engine's UNIQUE (reward, user, reference)
// arbitrates, granted=false reports "already held", and a caller that crashed
// between its own commit and this call simply calls again. The reference is
// the CALLER's dedup key (the achievements plugin passes the achievement
// slug), which also retires the old rule that every achievement must own its
// reward: two achievements paying the SAME reward are two references, two
// grants, no race for one entitlement.

// byslugGranter is a small type over the store and engine rather than a
// method set on Plugin, so what the registry hands out carries exactly this
// capability and nothing else Plugin happens to export.
type byslugGranter struct {
	store  Store
	engine *Engine
}

var _ pluginapi.RewardBySlugGranter = byslugGranter{}

// GrantOneOff grants the named enabled one_off reward to userID under
// reference.
//
// The refusals below are the ones the old achievement-completion path made,
// kept with their reasoning: each was a way a reward could look perfectly
// healthy and hand over nothing.
func (g byslugGranter) GrantOneOff(ctx context.Context, userID int64, slug, reference string) (bool, error) {
	r, err := g.store.RewardBySlug(ctx, slug)
	if err != nil {
		return false, err
	}
	if r == nil {
		return false, fmt.Errorf("reward %q does not exist", slug)
	}
	if !r.Enabled {
		// An error rather than the old path's silent skip: the old caller
		// shared this schema and had a validator to report unpayable
		// achievements eagerly; the new caller is in another plugin and this
		// error IS its report.
		return false, fmt.Errorf("reward %q is disabled, so it could be earned but not paid", r.Slug)
	}
	// The same refusal Engine.grant makes, and it has to be made here too
	// because this path does not go through it. A reward with no payout lines
	// pays nothing while looking perfectly healthy — found by the validator
	// on the first real achievement, whose payout row was lost while
	// splitting a repair file.
	if len(r.Payouts) == 0 {
		return false, fmt.Errorf("reward %q has no payout lines, so it hands over nothing", r.Slug)
	}
	if r.Kind != KindOneOff {
		return false, fmt.Errorf("reward %q is %s; only one_off is supported, because the caller's "+
			"reference names one entitlement ever and that is only what one_off means", r.Slug, r.Kind)
	}

	grant := Grant{
		// The reference is the caller's dedup key: "this entitlement, ever".
		// The pay-once UNIQUE on (reward, user, reference) is what makes the
		// idempotence contract true rather than hopeful.
		RewardID: r.ID, UserID: userID, Reference: reference,
		State: StatePending, Reason: "achievement: " + reference,
	}
	if r.ExpiresAfter != nil {
		exp := time.Now().Add(*r.ExpiresAfter)
		grant.ExpiresAt = &exp
	}

	// The existing grant-insert path, so the UNIQUE arbitration, the frozen
	// payout lines and the id assignment are the same code every other grant
	// uses — a second copy of that SQL is how two paths drift on which
	// columns a grant needs.
	grant, err = g.store.CreateGrant(ctx, grant, r.Payouts)
	if err != nil {
		if errors.Is(err, ErrAlreadyGranted) {
			// The member already holds this entitlement — the ordinary answer
			// on a repair retry, and the constraint arbitrating a race. Not
			// an error; "already held" is what granted=false means.
			return false, nil
		}
		return false, err
	}

	// An auto-delivery reward pays immediately; a claim-delivery one waits
	// for the member. Settling AFTER the insert on purpose — the payout
	// handlers are other people's code, and a slow or failing one must not
	// fail a grant that has already been written: the grant exists and is
	// pending, and the expiry sweep or a manual settle can both still pay it.
	// Worth logging, not worth undoing a completion the member earned.
	if r.Delivery == DeliveryAuto && g.engine != nil {
		if err := g.engine.Settle(ctx, grant.ID); err != nil {
			log.Printf("rewards: settling by-slug grant %d (%s for user %d): %v",
				grant.ID, r.Slug, userID, err)
		}
	}
	return true, nil
}

// registerByslugGranter publishes the capability, described so
// /admin/plugins can answer "what is this and am I meant to call it or
// supply it" without reading source.
func (p *Plugin) registerByslugGranter(c *core.Core) error {
	return c.RegisterDef(core.ExtensionDef{
		Name:    pluginapi.RewardBySlugGranterName,
		Summary: "grant a named enabled one_off reward to a member under a caller-chosen dedup reference; idempotent — the achievements plugin pays badges through this",
		Kind:    core.ExtService,
		Stable:  true,
	}, byslugGranter{store: p.store, engine: p.engine})
}
