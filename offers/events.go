package offers

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the offer system announces.
//
// EventRequestDelivered is the one event in this batch that DESERVES to be
// countable and deliberately is not. Handing another member a file they asked
// for is a real contribution, hard to fake — you must actually possess the
// thing and upload it — and every other event that passed the countable bar
// looks exactly like this.
//
// It stays uncountable because of a decision that has already been taken about
// this surface: fulfilment is meant to become anonymous. The requester gets
// their file, a temporary record exists while the work is in flight, and
// afterwards there is no trace of who served it. A countable event is the
// opposite of that — it is a permanent, per-member, monotonic ledger of
// exactly the attribution that is supposed to disappear, and a badge minted
// from it cannot be taken back without taking the badge back too.
//
// So the seam is declared now (a subscriber can still react in the moment, and
// the rewards engine can still fire a one-off), and the durable count is left
// unbuilt rather than built and later dismantled.
const (
	EventRequestCreated   = "offers.request.created"
	EventRequestDelivered = "offers.request.delivered"
)

// RequestCreated is the Data payload of EventRequestCreated. UserID is the
// member who asked.
type RequestCreated struct {
	RequestID int
	BucketID  int
	Points    int
}

// RequestDelivered is the Data payload of EventRequestDelivered.
//
// Event.UserID is the member whose agent served the file. RequesterID is
// deliberately absent: the requester did not act, and putting them here would
// invite a subscriber to credit the wrong person — while also being the exact
// pairing (who served whom) that anonymous fulfilment exists to forget.
type RequestDelivered struct {
	RequestID int
	NzbID     int64
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventRequestCreated, Emitter: "offers", Kind: core.EventMember, Stable: true,
			Summary: "a member requested a file from the offer system",
			Payload: "offers.RequestCreated"},
		{Name: EventRequestDelivered, Emitter: "offers", Kind: core.EventMember, Stable: true,
			Summary: "an offerer delivered a requested file (UserID is the offerer; uncountable by design)",
			Payload: "offers.RequestDelivered"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// WithCore attaches the mediator. Separate from the struct literal in
// Provision so a hand-built Handlers keeps compiling and announces nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces, after the write has committed.
func (h *Handlers) emit(ctx context.Context, name string, userID int, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: int64(userID), Data: data})
}
