package rewards

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// Rewards listen to the site.
//
// The achievements halves of this file — the countable-event subscriber, the
// completion path and the metric scorer — moved to the achievements plugin
// with the rest of that feature. What stays is the reward half: a reward
// whose trigger names a declared event fires when that event happens, with no
// host wiring at all.

// subscribeRewards fires trigger-driven rewards from events.
//
// Before this, a reward's trigger only fired when the HOST called Engine.Fire
// by hand — which the host does exactly once, for "login". Every other trigger
// in the dropdown was a value an operator could pick and nothing would ever
// send. The reward looked configured and simply never paid.
//
// Now a reward whose trigger names a declared event fires when that event
// happens, with no host wiring at all. An achievement (in its own plugin now)
// works the same way: both name an event in their definition, and both are
// driven by it.
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
