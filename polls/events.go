package polls

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What polls announce.

// EventVoted fires when a member casts a vote.
const EventVoted = "polls.voted"

// Voted is the Data payload of EventVoted.
//
// The OPTION is deliberately absent. A subscriber that could see how somebody
// voted turns a ballot into a public record, and the poll's whole
// results-policy machinery exists to control exactly when a tally becomes
// visible — an event stream that leaked it would route around all of it.
// That a member voted is the notable fact; what they chose is the poll's.
type Voted struct {
	PollID int64
	Slug   string
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventVoted, Emitter: "polls", Kind: core.EventMember,
		// COUNTABLE, and unusually safe to be. A member gets one vote per
		// poll and cannot create polls, so the ceiling on this count is set
		// by staff rather than by the member — the same property that makes
		// dailyreward's claim countable, arrived at differently.
		Countable: true, Stable: true,
		Summary: "a member voted in a poll",
		Payload: "polls.Voted",
	})
}

// emit announces, after the vote has committed. Nil-core tolerant: a missing
// mediator must not be what stops a ballot being counted.
func (p *Plugin) emit(ctx context.Context, name string, userID int64, data any) {
	if p == nil || p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{Name: name, UserID: userID, Count: 1, Data: data})
}
