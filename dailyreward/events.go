package dailyreward

import "github.com/the-loon-clan/loon/core"

// EventClaimed fires when a member claims their daily reward.
//
// Countable, and the natural achievement is a streak — which is why the
// payload carries the streak length rather than making a subscriber ask.
const EventClaimed = "dailyreward.claimed"

// Claimed is the Data payload of EventClaimed.
type Claimed struct {
	// Streak is how many consecutive days this claim makes, AFTER this one.
	Streak int
	// Reward is the points paid, so a subscriber does not have to
	// re-implement the ladder to know what happened.
	Reward int
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventClaimed, Emitter: "dailyreward",
		Kind: core.EventMember, Countable: true, Stable: true,
		Summary: "a member claimed their daily reward",
		Payload: "dailyreward.Claimed",
	})
}
