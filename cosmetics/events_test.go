package cosmetics

import (
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

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
		if !strings.HasPrefix(d.Name, "cosmetics.") {
			t.Errorf("%s is not namespaced to this plugin", d.Name)
		}
		if d.Payload == "" {
			t.Errorf("%s carries Data but names no type to assert to", d.Name)
		}
		if d.Countable && d.Kind != core.EventMember {
			t.Errorf("%s is countable but not a member event", d.Name)
		}
	}
	if err := declareEvents(c); err == nil {
		t.Error("declaring twice was accepted")
	}
}

// TestOnlyUnlockingIsCountable is the point of this pair, and the reason to
// pin it: an unlock costs points, so a total measures something spent.
// Equipping is a free toggle a member can work all afternoon, so a total
// measures fiddling with a dropdown — worth announcing, not worth totalling.
func TestOnlyUnlockingIsCountable(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatal(err)
	}
	for _, d := range c.EventDefs() {
		switch d.Name {
		case EventUnlocked:
			if !d.Countable {
				t.Error("unlocking is not countable; it costs points and cannot be manufactured")
			}
		case EventEquipped:
			if d.Countable {
				t.Error("equipping is countable; a member can toggle it all afternoon and the total would measure clicking")
			}
		default:
			t.Errorf("unexpected event %q", d.Name)
		}
	}
}

func TestEmitToleratesNoMediator(t *testing.T) {
	p := &Plugin{}
	p.emit(nil, EventUnlocked, 7, Unlocked{Slug: "glow"})
	var nilP *Plugin
	nilP.emit(nil, EventEquipped, 7, Equipped{})
}
