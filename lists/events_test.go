package lists

import (
	"os"
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
	if len(defs) == 0 {
		t.Fatal("nothing declared")
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		if !strings.HasPrefix(d.Name, "lists.") {
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

// FollowList must call deps.Follow EXACTLY once.
//
// Adding the emit here initially duplicated the call: one `if err :=
// deps.Follow(...)` for the event and the original one for the notification.
// Idempotent or not, following twice is wrong, and the second call's error is
// what would then have decided whether the owner was notified. Caught by
// reading the diff rather than by any test, which is why there is now a test.
func TestFollowListCallsFollowOnce(t *testing.T) {
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (h *Handlers) FollowList(")
	if start < 0 {
		t.Fatal("FollowList not found")
	}
	end := strings.Index(body[start+1:], "\nfunc ")
	if end < 0 {
		end = len(body) - start - 1
	}
	fn := body[start : start+1+end]

	if n := strings.Count(fn, "deps.Follow("); n != 1 {
		t.Errorf("FollowList calls deps.Follow %d times, want 1 — following twice is "+
			"wrong even when idempotent, and the second call's error decides the notification", n)
	}
}
