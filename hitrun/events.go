package hitrun

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What hitrun announces.
//
// One event, and it was missing: this plugin's README listed "declares no
// events" as a real gap, because nothing downstream could react to a warning.
// A site that wants to message a member, drop a rank, or simply count how
// often the rule fires had no way to hear about it.

// EventWarned fires when the sweep issues a hit-and-run warning.
const EventWarned = "hitrun.warned"

// Warned is the Data payload of EventWarned.
//
// Reason is the words shown to the member, carried rather than derived for the
// same reason the column stores them: a rule change must not rewrite history,
// and a subscriber reading this later should see what the member was told.
type Warned struct {
	InfoHash    string
	TorrentName string
	Reason      string
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventWarned, Emitter: "hitrun",
		// A MEMBER event, because it is about one member and a subscriber
		// wants to act on them — even though the member is not the one who
		// acted. Kind says who the event concerns; the sweep is the actor and
		// the sweep is not a member.
		Kind: core.EventMember,
		// NOT COUNTABLE, and this is the case core's own documentation names:
		// "an achievement can be scored on a countable event; 'member deleted
		// their account' is an event nobody should build a threshold on."
		// A badge for accumulating hit-and-run warnings would be a site
		// rewarding the behaviour it is trying to stop.
		Countable: false, Stable: true,
		Summary: "a member was warned for not seeding",
		Payload: "hitrun.Warned",
	})
}

// emit announces, after the warning is on the record.
func (p *Plugin) emit(ctx context.Context, name string, userID int64, data any) {
	if p == nil || p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{Name: name, UserID: userID, Count: 1, Data: data})
}
