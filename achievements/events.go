package achievements

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// EventCompleted is the one event this plugin emits: a member finished an
// achievement.
const EventCompleted = "achievements.completed"

// Completed is EventCompleted's payload, named by the declaration so a
// subscriber knows what to assert Event.Data to.
type Completed struct {
	// Slug names which achievement completed.
	Slug string
	// Paid says whether the payment half is settled: true for a pure badge
	// (nothing owed) and for a reward that landed; false when the granter
	// failed or is absent, in which case the scoring job's repair sweep pays
	// it later. A subscriber that celebrates on this event should not care;
	// one that audits payments does.
	Paid bool
}

// declareEvents announces what this plugin emits. Declared at Provision so
// the directory lists it before anything fires.
func (p *Plugin) declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name:    EventCompleted,
		Summary: "a member completed an achievement (silently-backfilled history is not announced)",
		Emitter: "achievements",
		// A member did the completing, and counting completions per member is
		// meaningful — "finish 10 achievements" is itself a legitimate
		// criterion, which is exactly what Countable exists to permit.
		Kind:      core.EventMember,
		Countable: true,
		Payload:   "achievements.Completed",
		// Not stable yet: the plugin just split out of rewards and the
		// payload may still grow (an achievement id, a reward slug).
		Stable: false,
	})
}

// announce emits the completion event.
//
// Called AFTER the completion's commit, and never on the already-completed
// path — announcing a completion that did not happen this time is how a
// subscriber's per-member total drifts past reality. The silent-backfill path
// skips this entirely: a badge earned before the achievement existed is
// awarded, not announced.
func (p *Plugin) announce(ctx context.Context, d AchievementDef, userID int64, paid bool) {
	if p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{
		Name:    EventCompleted,
		UserID:  userID,
		Count:   1,
		Subject: d.Slug,
		Data:    Completed{Slug: d.Slug, Paid: paid},
	})
}
