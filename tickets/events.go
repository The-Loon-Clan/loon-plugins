package tickets

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What support announces.
//
// The interesting one is the split. A reply is countable only when STAFF wrote
// it, so the event fires only then and its name says so — rather than firing
// on every reply and hoping subscribers filter. The achievement subscriber
// does not filter: it listens to every countable event and adds one. A single
// `tickets.replied` would therefore hand a "50 replies" badge to whoever
// answered their own ticket fifty times, which is the opposite of the
// contribution it was meant to recognise.
//
// Opening a ticket is NOT countable, for the same kind of reason from the
// other direction: nobody should be rewarded for needing help.
const (
	EventTicketCreated = "tickets.created"
	EventStaffReplied  = "tickets.staff_replied"
)

// TicketCreated is the Data payload of EventTicketCreated.
type TicketCreated struct {
	TicketID int64
	Subject  string
	Priority string
}

// StaffReplied is the Data payload of EventStaffReplied. Event.UserID is the
// staff member who answered; OwnerID is whose ticket it was.
type StaffReplied struct {
	TicketID int64
	OwnerID  int
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventTicketCreated, Emitter: "tickets", Kind: core.EventMember, Stable: true,
			// Not countable: a badge for opening support tickets rewards
			// having problems.
			Summary: "a member opened a support ticket",
			Payload: "tickets.TicketCreated"},
		{Name: EventStaffReplied, Emitter: "tickets", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a staff member answered a ticket (fires only for staff; UserID is the responder)",
			Payload: "tickets.StaffReplied"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, when a host wired the bus.
func (h *Handlers) emit(ctx context.Context, name string, userID int, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: int64(userID), Data: data})
}
