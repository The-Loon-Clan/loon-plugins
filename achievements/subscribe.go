package achievements

import (
	"context"
	"log"

	"github.com/the-loon-clan/loon/core"
)

// Achievements listen to the site.
//
// This is the link everything else was built for. The forum announces a post
// without knowing this plugin exists; this plugin adds one to whatever
// achievements are scored by that event, and completes the ones that crossed.
// Neither imports the other.
//
// The subscription is by EVENT NAME, and both halves of an achievement's
// criterion hold that same name: `metric` for counting, `trigger` for
// completing outright. One vocabulary, so there is no mapping table between
// "what happened" and "what is counted" — and therefore nothing for the two
// to drift apart about. A metric with no matching event still works; it
// simply gets its progress from the job reading a MetricSource instead.

// subscribe listens to EVERY declared event.
//
// All of them, not only countable ones, because the trigger half completes on
// any declared event — "when X happens, the badge is earned" is a
// fires-shaped question, precisely the one Countable was never about.
// Countable still decides the METRIC half, in the handler: it is about
// whether a per-member running total is meaningful, that judgement was
// already made at declaration, and re-deciding it here would put it in two
// places. "Member deleted their account" carries a UserID and is a terrible
// thing to count a badge toward; an operator who explicitly names it as a
// TRIGGER has made a deliberate configuration choice, which is different.
func (p *Plugin) subscribe(c *core.Core) {
	for _, d := range c.EventDefs() {
		countable := d.Countable
		c.On(d.Name, "achievements", func(ctx context.Context, e core.Event) {
			p.onEvent(ctx, e, countable)
		})
	}
}

// onEvent moves progress and fires triggers for one member on one event.
//
// Errors are logged and swallowed, deliberately. The member's post has already
// happened; failing here cannot un-happen it, and a handler that could fail
// the emitter would make the forum depend on this plugin's database being up.
// A dropped increment is recoverable — the job reconciles from the absolute
// total — whereas a forum that will not accept posts because achievements is
// unhappy is not.
func (p *Plugin) onEvent(ctx context.Context, e core.Event, countable bool) {
	// The system did it, not a member. Crediting user 0 would build a
	// phantom member with every achievement on the site. And a non-positive
	// count is a retraction or a "nothing happened", which neither counts
	// toward a threshold nor constitutes the trigger having fired.
	if e.UserID == 0 || e.Count <= 0 {
		return
	}
	if p.store == nil {
		return
	}
	if countable {
		p.scoreEvent(ctx, e)
	}
	p.fireTriggers(ctx, e)
}

// scoreEvent moves progress for one member on one countable event — the old
// rewards onCountableEvent, transposed.
func (p *Plugin) scoreEvent(ctx context.Context, e core.Event) {
	defs, err := p.store.AchievementDefsByMetric(ctx, e.Name)
	if err != nil {
		log.Printf("achievements: defs for %q: %v", e.Name, err)
		return
	}
	if len(defs) == 0 {
		// The common case by far: an event nothing is scored on. Returning
		// here keeps everything below off the hot path of every post.
		return
	}
	for _, d := range defs {
		reached, err := p.store.IncrementProgress(ctx, d.ID, e.UserID, e.Count)
		if err != nil {
			log.Printf("achievements: progress on %q for user %d: %v", d.Slug, e.UserID, err)
			continue
		}
		if !reached {
			continue
		}
		if err := p.completeAchievement(ctx, d, e.UserID); err != nil {
			log.Printf("achievements: completing %q for user %d: %v", d.Slug, e.UserID, err)
		}
	}
}

// fireTriggers completes every achievement whose trigger names this event.
//
// Once — the completed_at latch makes the second firing a no-op, which is
// what "a trigger completes once when that declared event fires" means.
func (p *Plugin) fireTriggers(ctx context.Context, e core.Event) {
	defs, err := p.store.AchievementDefsByTrigger(ctx, e.Name)
	if err != nil {
		log.Printf("achievements: trigger defs for %q: %v", e.Name, err)
		return
	}
	if len(defs) == 0 {
		// Same hot-path shortcut as the metric half: most events trigger
		// nothing.
		return
	}
	for _, d := range defs {
		// NEVER silent, deliberately diverging from the metric path's
		// backfill rule. Silence exists so a badge earned before the
		// achievement existed is not announced as news — but no scoring pass
		// can retroactively fire a declared event, so a trigger completion
		// is always live: the member did the thing just now, and they should
		// hear about it. (backfilled_at stays NULL on trigger defs forever;
		// reading it as "silence me" here would mute every one of them.)
		if err := p.completeAchievementSilent(ctx, d, e.UserID, false); err != nil {
			log.Printf("achievements: trigger %q completing %q for user %d: %v",
				e.Name, d.Slug, e.UserID, err)
		}
	}
}

// completeAchievement completes one from the metric path.
func (p *Plugin) completeAchievement(ctx context.Context, d AchievementDef, userID int64) error {
	// An achievement that has never been scored is being backfilled right
	// now, so this member earned it before it existed. Award it, do not
	// announce it. See MarkBackfilled for when that stops being true.
	return p.completeAchievementSilent(ctx, d, userID, d.BackfilledAt == nil)
}

// completeAchievementSilent records the completion, then pays it.
//
// Two steps where the old design had one transaction, and the order is the
// design: the completion commits FIRST, as this plugin's own atomic fact (the
// conditional update in CompleteAchievement is the race arbiter), and the
// reward is paid AFTER through the idempotent granter. A crash between the
// two leaves a completed row with paid_at NULL, which the scoring job's
// repair sweep finds and pays — GrantOneOff dedupes on (reward, user,
// reference), so calling it again is safe whether or not the first call's
// grant landed. Nothing here validates the reward: the granter refuses an
// unknown, disabled, payout-less or wrong-kind reward itself, and unlike the
// old shared-schema path a refused payment no longer needs to unwind a
// completion — the badge is earned either way, and the payment is repairable
// state, not half of an invariant.
func (p *Plugin) completeAchievementSilent(ctx context.Context, d AchievementDef, userID int64, silent bool) error {
	badge := d.RewardSlug == ""
	// A pure badge owes nothing, so paid_at stamps with the completion —
	// leaving it NULL would park the row in "pending" forever with no
	// payment ever coming.
	err := p.store.CompleteAchievement(ctx, d.ID, userID, badge)
	if err == ErrAlreadyCompleted {
		// Someone else completed it first, or it was already done. Not a
		// failure: the latch arbitrated and the answer is "no".
		return nil
	}
	if err != nil {
		return err
	}

	paid := badge
	if !badge {
		paid = p.payReward(ctx, d, userID)
	}
	if !silent {
		p.announce(ctx, d, userID, paid)
	}
	return nil
}

// payReward pays one completion through the rewards plugin, and reports
// whether the payment is settled.
//
// Failures are logged, never propagated: the completion has already committed
// and is the member's to keep; paid_at stays NULL and the repair sweep
// retries with the same idempotent call. That replaces the old path's rule
// that a broken reward must not consume the entitlement — there is no
// entitlement to consume any more, only a payment to catch up on.
func (p *Plugin) payReward(ctx context.Context, d AchievementDef, userID int64) bool {
	if p.granter == nil {
		// Absence was reported once at Start; per-completion noise would
		// drown the log without adding anything an operator can act on.
		return false
	}
	if _, err := p.granter.GrantOneOff(ctx, userID, d.RewardSlug, d.Slug); err != nil {
		log.Printf("achievements: paying %q for user %d via reward %q: %v",
			d.Slug, userID, d.RewardSlug, err)
		return false
	}
	// granted=false is also success: the member already holds this reward
	// under this reference — exactly what a repair retry after a half-crash
	// looks like — and "already paid" is paid.
	if err := p.store.MarkPaid(ctx, d.ID, userID); err != nil {
		// The grant landed; only the stamp is missing. The sweep re-grants
		// (idempotent no-op) and re-stamps.
		log.Printf("achievements: stamping paid_at on %q for user %d: %v", d.Slug, userID, err)
	}
	return true
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
// moved — the same shape as rewards' GrantUnits, and for the same reason: a
// per-member read to discover that almost nobody changed is thousands of
// queries to learn nothing. A member ABSENT from the returned map is left
// alone, never treated as zero: absence is "no data", and a half-returned
// counter must not stall the whole membership's badges at whatever it last
// said.
func (p *Plugin) scoreMetric(ctx context.Context, metric string, src MetricSource) (completed int, err error) {
	if p.store == nil {
		return 0, nil
	}
	defs, err := p.store.AchievementDefsByMetric(ctx, metric)
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
	// Which of these defs are being scored for the FIRST time. Captured
	// before the loop, because MarkBackfilled below flips it and a member
	// scored late in the same pass must be treated the same as one scored
	// early.
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
			reached, err := p.store.RecordProgress(ctx, d.ID, userID, v)
			if err != nil {
				return completed, err
			}
			if !reached {
				continue
			}
			if err := p.completeAchievementSilent(ctx, d, userID, backfilling[d.ID]); err != nil {
				// One member's failure must not abandon the rest of the
				// membership for this tick.
				log.Printf("achievements: completing %q for user %d: %v", d.Slug, userID, err)
				continue
			}
			completed++
		}
	}

	// The backfill is over. Stamped AFTER the whole pass, so every member the
	// counter named is treated alike — stamping per completion would announce
	// to whoever happened to be scored second.
	//
	// Stamped even when nobody qualified: an achievement nobody meets yet has
	// still had its backfill, and the first person to earn it later should
	// hear about it.
	for id, wasBackfilling := range backfilling {
		if !wasBackfilling {
			continue
		}
		if err := p.store.MarkBackfilled(ctx, id); err != nil {
			log.Printf("achievements: marking %d backfilled: %v", id, err)
		}
	}
	return completed, nil
}
