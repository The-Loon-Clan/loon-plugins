package polls

import (
	"reflect"
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
		if !strings.HasPrefix(d.Name, "polls.") {
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

// TestVotedCarriesNoOption. The results policy exists to control exactly when
// a tally becomes visible; an event stream carrying each member's choice would
// route around all of it and turn a ballot into a public record.
//
// By reflection, so adding an Option field fails this rather than passing a
// hand-written list nobody updated.
func TestVotedCarriesNoOption(t *testing.T) {
	rt := reflect.TypeOf(Voted{})
	for i := 0; i < rt.NumField(); i++ {
		switch n := rt.Field(i).Name; n {
		case "PollID", "Slug":
		default:
			t.Errorf("Voted carries %q — a ballot's contents are not the event's business", n)
		}
	}
}
