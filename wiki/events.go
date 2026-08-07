package wiki

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the wiki announces.
//
// Nothing here is countable YET, and that is a statement about who can write
// the wiki rather than about the value of writing it. Every route that fires
// these lives under /admin/wiki behind the mod gate, so a count of wiki edits
// is a count of staff activity — and the site already learned from
// tickets.staff_replied that a badge for staff work needs to say so in its
// name. When the wiki opens to members (the community-governance direction),
// the honest change is to make EventPostCreated countable at that point, not
// to pre-declare a metric nobody can move today.
//
// EventPostUpdated stays uncountable even then, and for a different reason: a
// count of edits rewards editing the same page fifty times. The contribution
// worth recognising is "pages touched", which is a DISTINCT count — an
// absolute metric a counter can compute and an event stream structurally
// cannot.
const (
	EventTopicCreated = "wiki.topic.created"
	EventPostCreated  = "wiki.post.created"
	EventPostUpdated  = "wiki.post.updated"
)

// TopicCreated is the Data payload of EventTopicCreated.
type TopicCreated struct {
	TopicID int
	Title   string
	Slug    string
}

// PostCreated is the Data payload of EventPostCreated.
type PostCreated struct {
	PostID  int
	TopicID int
	Title   string
	Slug    string
}

// PostUpdated is the Data payload of EventPostUpdated.
type PostUpdated struct {
	PostID int
	Title  string
	Slug   string
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventTopicCreated, Emitter: "wiki", Kind: core.EventMember, Stable: true,
			Summary: "a staff member created a wiki topic",
			Payload: "wiki.TopicCreated"},
		{Name: EventPostCreated, Emitter: "wiki", Kind: core.EventMember, Stable: true,
			Summary: "a staff member wrote a wiki page (admin-only today; countable when the wiki opens up)",
			Payload: "wiki.PostCreated"},
		{Name: EventPostUpdated, Emitter: "wiki", Kind: core.EventMember, Stable: true,
			Summary: "a staff member edited a wiki page",
			Payload: "wiki.PostUpdated"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// WithCore attaches the mediator. Separate from NewHandlers so the existing
// tests keep compiling and simply announce nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces, after the write has committed.
func (h *Handlers) emit(ctx context.Context, name string, userID int, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: int64(userID), Data: data})
}
