package rewards

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Achievements listen to the site.
//
// This is the link everything else was built for. The forum announces a post
// without knowing this plugin exists; this plugin adds one to whatever
// achievements are scored by that event, and completes the ones that crossed.
// Neither imports the other.
//
// The subscription is by EVENT NAME, and an achievement's `metric` holds that
// same name. One vocabulary, so there is no mapping table between "what
// happened" and "what is counted" — and therefore nothing for the two to drift
// apart about. A metric with no matching event still works; it simply gets its
// progress from the job reading a MetricSource instead.

// subscribeAchievements listens to every countable event the host declared.
//
// Only countable ones. "Member deleted their account" carries a UserID and is
// a terrible thing to award a badge for, and the flag is where that judgement
// was already made — re-deciding it here would put it in two places.
func (p *Plugin) subscribeAchievements(c *core.Core) {
	for _, d := range c.EventDefs() {
		if !d.Countable {
			continue
		}
		c.On(d.Name, "rewards", p.onCountableEvent)
	}
}

// onCountableEvent moves progress for one member on one event.
//
// Errors are logged and swallowed, deliberately. The member's post has already
// happened; failing here cannot un-happen it, and a handler that could fail
// the emitter would make the forum depend on this plugin's database being up.
// A dropped increment is recoverable — the job reconciles from the absolute
// total — whereas a forum that will not accept posts because achievements is
// unhappy is not.
func (p *Plugin) onCountableEvent(ctx context.Context, e core.Event) {
	// The system did it, not a member. Crediting user 0 would build a
	// phantom member with every achievement on the site.
	if e.UserID == 0 || e.Count <= 0 {
		return
	}
	if p.admin == nil {
		return
	}
	defs, err := p.admin.AchievementDefsByMetric(ctx, e.Name)
	if err != nil {
		log.Printf("achievements: defs for %q: %v", e.Name, err)
		return
	}
	if len(defs) == 0 {
		// The common case by far: an event nothing is scored on. Returning
		// here keeps the store assertion off the hot path of every post.
		return
	}
	// Only now is a real store needed. Asserting earlier made this handler
	// unreachable in any test without a database — which is how a test for
	// the UserID guard came to pass for the wrong reason.
	st, ok := p.store.(*PGStore)
	if !ok {
		return
	}
	for _, d := range defs {
		reached, err := st.IncrementProgress(ctx, d.ID, e.UserID, e.Count)
		if err != nil {
			log.Printf("achievements: progress on %q for user %d: %v", d.Slug, e.UserID, err)
			continue
		}
		if !reached {
			continue
		}
		if err := p.completeAchievement(ctx, st, d, e.UserID); err != nil {
			log.Printf("achievements: completing %q for user %d: %v", d.Slug, e.UserID, err)
		}
	}
}

// completeAchievement pays one.
//
// The grant is built here rather than through Engine.Claim because the
// completion and the grant have to land in ONE transaction — that is the whole
// point of CompleteAchievement — and Claim writes the grant on its own.
//
// reference is 0 because achievements are restricted to one_off rewards, where
// 0 is the reference the engine's UNIQUE (reward, user, reference) uses to mean
// "ever". Validation enforces the restriction; this would silently pay twice
// per window if it were ever relaxed without revisiting the line below, so the
// kind is re-checked here rather than trusted.
func (p *Plugin) completeAchievement(ctx context.Context, st *PGStore, d AchievementDef, userID int64) error {
	r, err := p.store.RewardByID(ctx, d.RewardID)
	if err != nil {
		return err
	}
	if r == nil || !r.Enabled {
		// Earnable but unpayable. The validator reports this on the admin
		// page; here it is simply not something to act on.
		return nil
	}
	if r.Kind != KindOneOff {
		return fmt.Errorf("achievement %q pays reward %q of kind %s; only one_off is supported "+
			"and reference=0 below would be wrong for anything else", d.Slug, r.Slug, r.Kind)
	}

	g := Grant{
		RewardID: r.ID, UserID: userID, Reference: 0,
		State: StatePending, Reason: "achievement: " + d.Slug,
	}
	if r.ExpiresAfter != nil {
		exp := time.Now().Add(*r.ExpiresAfter)
		g.ExpiresAt = &exp
	}

	g, err = st.CompleteAchievement(ctx, d.ID, g, r.Payouts)
	if err != nil {
		if err == ErrAlreadyGranted {
			// Someone else completed it first, or it was already done. Not a
			// failure: the constraint arbitrated and the answer is "no".
			return nil
		}
		return err
	}

	// An auto-delivery reward pays immediately; a claim-delivery one waits for
	// the member. Settling AFTER the transaction on purpose — the payout
	// handlers are other people's code, and a slow or failing one must not
	// hold open the transaction that owns the completion.
	if r.Delivery == DeliveryAuto && p.engine != nil {
		if err := p.engine.Settle(ctx, g.ID); err != nil {
			// The grant exists and is pending; the expiry sweep and a manual
			// settle can both still pay it. Worth logging, not worth undoing
			// a completion the member earned.
			log.Printf("achievements: settling %q for user %d: %v", d.Slug, userID, err)
		}
	}
	return nil
}
