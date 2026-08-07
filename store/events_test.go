package store

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
	if len(defs) == 0 {
		t.Fatal("nothing declared")
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		if !strings.HasPrefix(d.Name, "store.") {
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
