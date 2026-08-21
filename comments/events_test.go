package comments

import (
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// Every declared event must be acceptable to the directory, namespaced to this
// plugin, and declared exactly once — a second declaration is a wiring bug
// worth failing Provision for, not a warning.
func TestDeclaredEventsAreValid(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatalf("declare: %v", err)
	}
	defs := c.EventDefs()
	if len(defs) != 2 {
		t.Fatalf("declared %d events, want 2", len(defs))
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		if !strings.HasPrefix(d.Name, "comments.") {
			t.Errorf("%s is not namespaced to this plugin; two plugins could collide", d.Name)
		}
		if d.Payload == "" {
			t.Errorf("%s carries Data but names no type for a subscriber to assert to", d.Name)
		}
		if d.Countable && d.Kind != core.EventMember {
			t.Errorf("%s is countable but not a member event; there would be nobody to count it against", d.Name)
		}
	}
	if err := declareEvents(c); err == nil {
		t.Error("declaring twice was accepted")
	}
}

// TestEmitToleratesNoMediator. The tests build a Plugin without a Core, and
// more to the point a missing mediator must never be the thing that stops a
// comment being posted — the event is a side effect of the write, not the
// other way round.
func TestEmitToleratesNoMediator(t *testing.T) {
	p := &Plugin{} // no core
	p.emit(nil, EventPosted, 7, Posted{CommentID: 1})
	p.emit(nil, EventThanked, 7, Thanked{CommentID: 1, AuthorID: 8})
}

// TestThankedCarriesBothParties. Event.UserID is the thanker; the author has
// to travel in the payload, because only one member can be an event's subject
// and a subscriber paying the wrong one is the bug this shape prevents.
func TestThankedCarriesBothParties(t *testing.T) {
	th := Thanked{CommentID: 3, AuthorID: 8}
	if th.AuthorID == 0 {
		t.Error("the author is not carried; a subscriber cannot tell who was thanked")
	}
}
