package chat

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the shoutbox announces.
//
// Not countable, though the reason is narrower than it first looks and worth
// stating precisely, because the obvious version of it is wrong.
//
// THIS PLUGIN stores nothing — the message goes straight to a Discord webhook.
// But on a host with the relay wired, it comes back through the bridge and
// lands in a chat_messages row, so the messages generally ARE persisted
// somewhere; they are just not persisted here, and not with a user id.
//
// That round trip is the actual hazard. One site message produces both this
// event and a stored row that arrives labelled as having come from Discord, so
// anything counting both counts every site message twice. Add the rate limiter
// (one message per two seconds, forty-three thousand a day) and a count off
// this event is farmable as well as doubled.
//
// The honest counter is a query over those rows once they carry a user id, at
// which point this event's job is to say "look again" and nothing more.
//
// Announced anyway because "is this member active in chat" is a genuine
// signal, and a one-off reward hung off the first message is a reasonable
// thing for an operator to want.
const EventMessageSent = "chat.message.sent"

// MessageSent is the Data payload of EventMessageSent.
//
// Length rather than the text. This plugin keeps no copy, and handing the body
// to arbitrary in-process subscribers would put a durable record of it
// somewhere nobody chose. The relay's own row is a separate decision, made by
// the host that wired it, and visible to the people in the channel.
type MessageSent struct {
	Length int
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventMessageSent, Emitter: "chat", Kind: core.EventMember, Stable: true,
		Summary: "a member sent a shoutbox message (delivered to Discord; nothing stored here)",
		Payload: "chat.MessageSent",
	})
}

// WithCore attaches the mediator. Separate from NewHandlers so a hand-built
// Handlers keeps compiling and simply announces nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces, after Discord accepted the message. A webhook that returned
// 5xx has not delivered anything, and announcing it would credit a member for
// a message nobody saw.
func (h *Handlers) emit(ctx context.Context, name string, userID int, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: int64(userID), Data: data})
}
