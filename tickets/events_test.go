package tickets

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
		if !strings.HasPrefix(d.Name, "tickets.") {
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

// The staff-only rule, pinned because it is the easy thing to "simplify" away.
//
// The achievement subscriber does not filter — it adds one for every countable
// event it hears. So a single tickets.replied firing on every reply would hand
// a "50 replies" badge to whoever answered their own ticket fifty times, which
// is the opposite of the contribution it recognises. The name says staff and
// the emit site is guarded by isAdmin; this checks both still agree.
func TestOnlyStaffRepliesAreCountable(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatalf("declare: %v", err)
	}
	byName := map[string]core.EventDef{}
	for _, d := range c.EventDefs() {
		byName[d.Name] = d
	}

	if r := byName[EventStaffReplied]; !r.Countable {
		t.Error("the staff reply event is not countable, so no achievement can recognise it")
	}
	// Opening a ticket must NOT be countable: a badge for it rewards having
	// problems.
	if o := byName[EventTicketCreated]; o.Countable {
		t.Error("opening a ticket is countable — that badges people for needing help")
	}

	// And the emit is actually guarded. A countable event whose name promises
	// staff but whose call site fires for everyone is worse than no guard,
	// because the name is what stops anyone checking.
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "EventStaffReplied")
	if i < 0 {
		t.Fatal("the staff reply is never emitted")
	}
	// Look back a little way for the guard that must precede it.
	window := body[max(0, i-400):i]
	if !strings.Contains(window, "isAdmin") {
		t.Error("EventStaffReplied is emitted without an isAdmin guard nearby — it would " +
			"fire for members replying to their own tickets")
	}
}
