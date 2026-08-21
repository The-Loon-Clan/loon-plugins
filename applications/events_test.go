package applications

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
	if len(defs) != 2 {
		t.Fatalf("declared %d events, want 2", len(defs))
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", d.Name, err)
		}
		if !strings.HasPrefix(d.Name, "applications.") {
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

// TestApplicationEventsAreSystemAndUncountable. An application is made by
// somebody with NO account, so there is no member to attribute it to or total
// it against — a member event with no member is a contradiction the directory
// would carry happily.
func TestApplicationEventsAreSystemAndUncountable(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatal(err)
	}
	for _, d := range c.EventDefs() {
		if d.Kind != core.EventSystem {
			t.Errorf("%s is %q; nobody has an account yet when it fires", d.Name, d.Kind)
		}
		if d.Countable {
			t.Errorf("%s is countable; there is no member to count it against", d.Name)
		}
	}
}

// TestPayloadsCarryNoIdentifyingDetail. An event reaches every subscriber on
// the site. Learning who is trying to join a closed site from a stream several
// plugins read would route around the access gate on the staff queue, which is
// the one surface meant to show it.
func TestPayloadsCarryNoIdentifyingDetail(t *testing.T) {
	s := Submitted{ApplicationID: 1}
	if got := fieldNames(s); len(got) != 1 || got[0] != "ApplicationID" {
		t.Errorf("Submitted carries %v — only the fact should travel", got)
	}
	d := Decided{ApplicationID: 1, Status: StatusAccepted, DecidedBy: 9}
	for _, f := range fieldNames(d) {
		switch f {
		case "ApplicationID", "Status", "DecidedBy":
		default:
			t.Errorf("Decided carries %q; the applicant and the invite code must not travel", f)
		}
	}
}

func fieldNames(v any) []string {
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		out = append(out, rt.Field(i).Name)
	}
	return out
}
