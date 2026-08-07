package messages

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What messaging announces.
//
// Neither of these is countable, and the reason is the same one twice: a
// count of private messages is both trivially farmable and impossible to
// moderate. Two accounts can exchange a thousand DMs in an afternoon and
// nobody else can see any of it — so a "1000 messages" badge measures
// persistence at a keyboard, not a contribution to the site. Every countable
// event so far (forum posts, uploads, list items, staff replies) is public and
// costly to fake; DMs are neither.
//
// They are still worth declaring. The rewards engine fires on any event, so an
// operator can hang a one-off "sent your first DM" reward off this without a
// counter, and the directory is how a subscriber discovers the name exists.
const (
	// EventDMSent fires when a member sends a direct message. UserID is the
	// SENDER. The body is deliberately absent from the payload — see DMSent.
	EventDMSent = "messages.dm.sent"
	// EventBroadcastSent fires when an admin sends a site announcement.
	EventBroadcastSent = "messages.broadcast.sent"
)

// DMSent is the Data payload of EventDMSent.
//
// It carries no message text, on purpose. Subscribers are arbitrary plugins,
// event delivery is synchronous and in-process, and anything one of them logs
// or persists is a copy of a private conversation living somewhere the sender
// never agreed to. Everything a subscriber legitimately needs — who, to whom,
// which thread — is here; the content is not its business.
type DMSent struct {
	ThreadID    int64
	RecipientID int
}

// BroadcastSent is the Data payload of EventBroadcastSent. Target names the
// audience the admin chose ("all", a role, a single username).
type BroadcastSent struct {
	Title  string
	Target string
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventDMSent, Emitter: "messages", Kind: core.EventMember, Stable: true,
			Summary: "a member sent a direct message (UserID is the sender; no body in the payload)",
			Payload: "messages.DMSent"},
		{Name: EventBroadcastSent, Emitter: "messages", Kind: core.EventMember, Stable: true,
			Summary: "an admin sent a site announcement",
			Payload: "messages.BroadcastSent"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// WithCore attaches the mediator. Separate from the struct literal in
// Provision so tests that build a Handlers by hand keep compiling and simply
// announce nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces, after the write has committed.
func (h *Handlers) emit(ctx context.Context, name string, userID int, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: int64(userID), Data: data})
}
