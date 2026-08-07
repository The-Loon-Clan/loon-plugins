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

// subscribeRewards fires trigger-driven rewards from events.
//
// Before this, a reward's trigger only fired when the HOST called Engine.Fire
// by hand — which the host does exactly once, for "login". Every other trigger
// in the dropdown was a value an operator could pick and nothing would ever
// send. The reward looked configured and simply never paid.
//
// Now a reward whose trigger names a declared event fires when that event
// happens, with no host wiring at all. The two systems finally work the same
// way: an achievement and a reward both name an event in their definition, and
// both are driven by it.
//
// ALL declared events, not only countable ones. Countable is about whether a
// running total is meaningful — a threshold question, and achievements'
// business. A reward is "when X happens, pay", and plenty of things worth
// paying for are not worth counting.
func (p *Plugin) subscribeRewards(c *core.Core) {
	for _, d := range c.EventDefs() {
		c.On(d.Name, "rewards", p.onRewardEvent)
	}
}

// onRewardEvent grants any auto-delivery reward triggered by this event.
//
// Engine.Fire already does the work — resolving what is available, skipping
// what is claimed, letting the UNIQUE constraint arbitrate a race. This is
// only the wire from the bus to it, which is the point: the granting rules did
// not need to change to gain a second way of being triggered.
func (p *Plugin) onRewardEvent(ctx context.Context, e core.Event) {
	if e.UserID == 0 || p.engine == nil {
		return
	}
	p.engine.Fire(ctx, e.UserID, e.Name)
}

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
	// An achievement that has never been scored is being backfilled right now,
	// so this member earned it before it existed. Award it, do not announce
	// it. See MarkBackfilled for when that stops being true.
	return p.completeAchievementSilent(ctx, st, d, userID, d.BackfilledAt == nil)
}

func (p *Plugin) completeAchievementSilent(ctx context.Context, st *PGStore, d AchievementDef, userID int64, silent bool) error {
	r, err := p.store.RewardByID(ctx, d.RewardID)
	if err != nil {
		return err
	}
	if r == nil || !r.Enabled {
		// Earnable but unpayable. The validator reports this on the admin
		// page; here it is simply not something to act on.
		return nil
	}
	// The same refusal Engine.grant makes, and it has to be made here too
	// because this path does not go through it. A reward with no payout lines
	// pays nothing while looking perfectly healthy — and unlike a refused
	// grant, a completion is IRREVERSIBLE: completed_at is stamped once, so
	// the member would hold an achievement that never paid and could never be
	// re-earned when somebody added the missing line.
	//
	// Found by the validator on the first real achievement, whose payout row
	// was lost while splitting a repair file.
	if len(r.Payouts) == 0 {
		return fmt.Errorf("achievement %q pays reward %q, which has no payout lines", d.Slug, r.Slug)
	}
	if r.Kind != KindOneOff {
		return fmt.Errorf("achievement %q pays reward %q of kind %s; only one_off is supported "+
			"and reference=0 below would be wrong for anything else", d.Slug, r.Slug, r.Kind)
	}

	g := Grant{
		RewardID: r.ID, UserID: userID, Reference: 0,
		State: StatePending, Reason: "achievement: " + d.Slug,
		Silent: silent,
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

// scoreMetric reads one counter and settles every achievement it scores.
//
// The counterpart to the event path, and the reason both exist. An event says
// "one more" and is lost if anything drops it; this says "the total is 613"
// and is therefore self-healing — a member whose increment went missing is
// corrected on the next tick rather than being permanently one short.
//
// It is also the ONLY way an achievement on something nothing emits can move.
// Tenure is the example: no plugin announces an anniversary, and none should,
// because the member does not do anything on the day.
//
// One counter read for the whole membership, then one write per member who
// moved — the same shape as GrantUnits, and for the same reason: a per-member
// read to discover that almost nobody changed is thousands of queries to learn
// nothing.
func (p *Plugin) scoreMetric(ctx context.Context, metric string, src MetricSource) (completed int, err error) {
	st, ok := p.store.(*PGStore)
	if !ok || p.admin == nil {
		return 0, nil
	}
	defs, err := p.admin.AchievementDefsByMetric(ctx, metric)
	if err != nil {
		return 0, err
	}
	if len(defs) == 0 {
		// Nothing is scored on it. Do not read the counter — a metric source
		// is a query over the whole membership, and running one per tick for
		// an achievement nobody created is pure cost.
		return 0, nil
	}

	values, err := src.Values(ctx)
	if err != nil {
		return 0, err
	}
	// Which of these defs are being scored for the FIRST time. Captured before
	// the loop, because MarkBackfilled below flips it and a member scored late
	// in the same pass must be treated the same as one scored early.
	backfilling := make(map[int64]bool, len(defs))
	for _, d := range defs {
		backfilling[d.ID] = d.BackfilledAt == nil
	}
	for userID, v := range values {
		if userID == 0 || v <= 0 {
			continue
		}
		for _, d := range defs {
			// SET, not add: this is the absolute total, and adding it every
			// tick would multiply a member's progress by the number of ticks.
			reached, err := st.RecordProgress(ctx, d.ID, userID, v)
			if err != nil {
				return completed, err
			}
			if !reached {
				continue
			}
			if err := p.completeAchievementSilent(ctx, st, d, userID, backfilling[d.ID]); err != nil {
				// One member's failure must not abandon the rest of the
				// membership for this tick.
				log.Printf("achievements: completing %q for user %d: %v", d.Slug, userID, err)
				continue
			}
			completed++
		}
	}

	// The backfill is over. Stamped AFTER the whole pass, so every member the
	// counter named is treated alike -- stamping per completion would announce
	// to whoever happened to be scored second.
	//
	// Stamped even when nobody qualified: an achievement nobody meets yet has
	// still had its backfill, and the first person to earn it later should
	// hear about it.
	for id, wasBackfilling := range backfilling {
		if !wasBackfilling {
			continue
		}
		if err := st.MarkBackfilled(ctx, id); err != nil {
			log.Printf("achievements: marking %d backfilled: %v", id, err)
		}
	}
	return completed, nil
}
