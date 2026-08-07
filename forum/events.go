package forum

import "github.com/the-loon-clan/loon/core"

// What the forum announces.
//
// Declared so the directory can list them before anything fires — which is the
// difficulty with events that services do not have: a service can be read off
// the registry, but an undeclared event is invisible until the moment it
// happens, and a subscriber cannot discover what to listen for by waiting.
//
// Emitted AFTER the write commits, never inside it. The event says a thing
// happened; a subscriber that acted on a post which then rolled away has no
// way to find out.

const (
	// EventThreadCreated fires when a member starts a thread.
	EventThreadCreated = "forum.thread.created"
	// EventPostCreated fires when a member replies to one.
	EventPostCreated = "forum.post.created"
	// EventPostReacted fires when a member reacts to a post. The ADD leg of
	// the toggle only: removing a reaction announces nothing, because a
	// subscriber counting reactions cannot un-count reliably and one that
	// tries drifts the first time an event is missed.
	EventPostReacted = "forum.post.reacted"
)

// ThreadCreated is the Data payload of EventThreadCreated.
type ThreadCreated struct {
	CategoryID int
	ThreadID   int
	Title      string
	Type       string
}

// PostCreated is the Data payload of EventPostCreated.
type PostCreated struct {
	ThreadID int
	PostID   int64
}

// PostReacted is the Data payload of EventPostReacted.
//
// Event.UserID is the REACTOR. AuthorID is who was reacted to, and it lives
// here rather than in UserID because only one member can be the subject of an
// event and the reactor is the one who acted.
type PostReacted struct {
	PostID   int64
	ThreadID int64
	AuthorID int
	Emoji    string
}

// declareEvents registers the above with the directory. Errors are returned so
// Provision can fail on a duplicate: two plugins both believing they own
// "forum.post.created" is a wiring bug worth stopping for, not a warning.
func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventThreadCreated, Emitter: "forum", Countable: true, Stable: true,
			Summary: "a member started a thread",
			Payload: "forum.ThreadCreated"},
		{Name: EventPostCreated, Emitter: "forum", Countable: true, Stable: true,
			Summary: "a member replied to a thread",
			Payload: "forum.PostCreated"},
		{Name: EventPostReacted, Emitter: "forum", Countable: true, Stable: true,
			Summary: "a member reacted to a post (UserID is the reactor, not the author)",
			Payload: "forum.PostReacted"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}
