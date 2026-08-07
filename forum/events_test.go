package forum

import (
	"context"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// Every event this plugin declares must be acceptable to the directory, and
// declaring must be idempotent-hostile: a second declaration is a wiring bug
// worth failing Provision for.
func TestDeclaredEventsAreValidAndUnique(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatalf("declare: %v", err)
	}
	defs := c.EventDefs()
	if len(defs) != 3 {
		t.Fatalf("%d events declared, want 3", len(defs))
	}
	for _, d := range defs {
		if d.Emitter != "forum" {
			t.Errorf("%s: emitter = %q, want forum", d.Name, d.Emitter)
		}
		if !strings.HasPrefix(d.Name, "forum.") {
			t.Errorf("%s does not carry the plugin's namespace, so two plugins could collide", d.Name)
		}
		if d.Payload == "" {
			t.Errorf("%s carries Data but names no type for a subscriber to assert to", d.Name)
		}
	}
	if err := declareEvents(c); err == nil {
		t.Error("declaring twice was accepted; two emitters would both think they own these")
	}
}

// The reaction event has TWO members in it and only one can be the subject.
// UserID is the reactor — they did the thing — and the author goes in Data.
// Backwards, this builds a "100 reactions" achievement that credits whoever
// was reacted TO, which looks right until somebody checks whose count moved.
func TestReactionEventCreditsTheReactorNotTheAuthor(t *testing.T) {
	c := &core.Core{}
	var got core.Event
	c.On(EventPostReacted, "test", func(_ context.Context, e core.Event) { got = e })

	const reactor, author = int64(42), 7
	c.Emit(context.Background(), core.Event{
		Name: EventPostReacted, UserID: reactor, Subject: "9",
		Data: PostReacted{PostID: 9, ThreadID: 3, AuthorID: author, Emoji: "👍"},
	})

	if got.UserID != reactor {
		t.Fatalf("UserID = %d, want the reactor %d", got.UserID, reactor)
	}
	d, ok := got.Data.(PostReacted)
	if !ok {
		t.Fatalf("Data is %T, want forum.PostReacted — the payload type the def promises", got.Data)
	}
	if d.AuthorID != author {
		t.Errorf("AuthorID = %d, want %d", d.AuthorID, author)
	}
	if int64(d.AuthorID) == got.UserID {
		t.Error("the reactor and the author are the same field; one of them is being credited wrongly")
	}
}

// A plugin with no Core attached must emit nothing rather than panic. Every
// existing caller of NewHandlers gets this, and so does every test.
func TestEmitIsInertWithoutCore(t *testing.T) {
	h := NewHandlers(nil, nil, nil, nil)
	if h.core != nil {
		t.Fatal("NewHandlers attached a Core by itself")
	}
	// The guard is inside emit; reaching it with a nil gin.Context proves it
	// returns before touching anything.
	h.emit(nil, EventPostCreated, 1, "1", nil)
}
