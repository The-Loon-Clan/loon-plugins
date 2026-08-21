package mediainfo

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
		if !strings.HasPrefix(d.Name, "mediainfo.") {
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
