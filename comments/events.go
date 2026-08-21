package comments

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What comments announce.
//
// Declared so the directory lists them before anything fires. That is the
// difficulty events have and services do not: a service can be read off the
// registry, but an undeclared event is invisible until the moment it happens,
// and a subscriber cannot discover what to listen for by waiting.
//
// Emitted AFTER the write commits, never inside it. The event says a thing
// happened; a subscriber that acted on a comment which then rolled away has no
// way to find out.

const (
	// EventPosted fires when a member posts a comment.
	EventPosted = "comments.posted"
	// EventThanked fires when a member thanks somebody else's comment.
	//
	// The ADD leg of the toggle only. Withdrawing announces nothing, for the
	// reason forum gives about reactions: a subscriber counting thanks cannot
	// un-count reliably, and one that tries drifts the first time an event is
	// missed.
	EventThanked = "comments.thanked"
)

// Posted is the Data payload of EventPosted.
type Posted struct {
	CommentID   int64
	SubjectKind string
	SubjectID   int64
}

// Thanked is the Data payload of EventThanked.
//
// Event.UserID is the THANKER — the member who acted. AuthorID is who was
// thanked, and it lives here rather than in UserID because only one member can
// be the subject of an event, and the one who acted is the thanker. This is
// the same shape as forum's reaction event, deliberately.
type Thanked struct {
	CommentID int64
	AuthorID  int64
}

// declareEvents registers the above. Errors are returned so Provision can fail
// on a duplicate: two plugins both believing they own "comments.posted" is a
// wiring bug worth stopping for, not a warning.
func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		// COUNTABLE, and the reasoning is worth stating because the sibling
		// plugin decided the opposite.
		//
		// playlists declares its two events NOT countable, on the grounds that
		// both are free actions on rows nobody else has to accept, so a count
		// measures clicking. A comment is also free — but it is PUBLIC and it
		// is moderated, so farming one means posting visible rubbish where
		// people and a mod queue can see it. That is the line: forum's
		// thread/post events are countable for the same reason, and a private
		// row's are not.
		{Name: EventPosted, Emitter: "comments", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member posted a comment",
			Payload: "comments.Posted"},
		// Countable, but read the caveat in the plugin's README: only the
		// AUTHOR is paid, never the thanker, because paying somebody to press
		// thanks is how a site grows thanks-farming rings. The count here is
		// of the thanker's ACTIONS, so an achievement scored on it rewards
		// giving thanks, which is the cheap half. A site wanting to reward
		// being thanked should score on the author instead, and that needs a
		// different event than this one.
		{Name: EventThanked, Emitter: "comments", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member thanked somebody else's comment",
			Payload: "comments.Thanked"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, after the write has committed.
//
// Nil-core tolerant because the tests construct a Plugin without one, and a
// missing mediator must not be the thing that stops a comment being posted.
func (p *Plugin) emit(ctx context.Context, name string, userID int64, data any) {
	if p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{
		Name: name, UserID: userID, Count: 1, Data: data,
	})
}
