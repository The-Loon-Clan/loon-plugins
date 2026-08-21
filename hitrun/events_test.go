package hitrun

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
	if len(defs) == 0 {
		t.Fatal("nothing declared")
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		if !strings.HasPrefix(d.Name, "hitrun.") {
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

func TestEmitToleratesNoMediator(t *testing.T) {
	var p *Plugin
	p.emit(nil, "x", 1, nil)
	(&Plugin{}).emit(nil, "x", 1, nil)
}

// TestWarnedIsNotCountable. core's documentation names this exact case: a
// badge for accumulating hit-and-run warnings would be a site rewarding the
// behaviour it is trying to stop.
func TestWarnedIsNotCountable(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatal(err)
	}
	for _, d := range c.EventDefs() {
		if d.Name == EventWarned && d.Countable {
			t.Error("hitrun.warned is countable; nothing should be scored on being warned")
		}
	}
}
