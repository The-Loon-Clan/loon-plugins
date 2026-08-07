package chat

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the shoutbox announces.
//
// Not countable. The rate limiter allows one message every two seconds, which
// is forty-three thousand a day, and the plugin stores nothing — the message
// goes straight to a Discord webhook and is gone from here. So a count would
// be both farmable and unauditable: there is no row anywhere on this site to
// check a badge against afterwards.
//
// Announced anyway because "is this member active in chat" is a genuine
// signal, and a one-off reward hung off the first message is a reasonable
// thing for an operator to want.
const EventMessageSent = "chat.message.sent"

// MessageSent is the Data payload of EventMessageSent.
//
// Length rather than the text. The message has already left for Discord and
// this plugin keeps no copy; handing the body to arbitrary in-process
// subscribers would create the first durable record of it, somewhere the
// author has no idea exists.
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
