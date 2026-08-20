package pluginapi

import (
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// A contributed thing, shaped like the real ones: an interface whose value
// carries its own idea of its key.
type fakeMode struct{ key string }

func (f fakeMode) ModeKey() string { return f.key }

type keyed interface{ ModeKey() string }

const testPrefix = "test.thing."

func reg(t *testing.T, c *core.Core, name string, v any) {
	t.Helper()
	if err := c.Register(name, v); err != nil {
		t.Fatal(err)
	}
}

// TestContributionsAreOrdered is the rule two of the five hand-rolled scans got
// wrong. These become radio buttons and dropdown entries, and Go randomises map
// iteration on purpose — so an unsorted scan produces a control whose options
// move between page loads, which reads as a bug in the page.
func TestContributionsAreOrdered(t *testing.T) {
	c := &core.Core{}
	for _, k := range []string{"zulu", "alpha", "mike", "bravo"} {
		reg(t, c, testPrefix+k, fakeMode{key: k})
	}
	got := Contributions[keyed](c, testPrefix)
	want := []string{"alpha", "bravo", "mike", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("got %d contributions, want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i].Key != k {
			t.Errorf("position %d is %q, want %q", i, got[i].Key, k)
		}
	}
}

// TestContributionsCarryTheKeyWithoutThePrefix — every consumer needs the bare
// key, because that is what a settings row stores and a form submits.
func TestContributionsCarryTheKeyWithoutThePrefix(t *testing.T) {
	c := &core.Core{}
	reg(t, c, testPrefix+"apply", fakeMode{key: "apply"})
	got := Contributions[keyed](c, testPrefix)
	if len(got) != 1 || got[0].Key != "apply" {
		t.Fatalf("got %+v, want one contribution keyed \"apply\"", got)
	}
}

// TestOtherPrefixesAndWrongTypesAreIgnored. The registry is shared and a prefix
// is a namespace, not a lock.
func TestOtherPrefixesAndWrongTypesAreIgnored(t *testing.T) {
	c := &core.Core{}
	reg(t, c, testPrefix+"good", fakeMode{key: "good"})
	reg(t, c, testPrefix+"wrongtype", "not a mode at all")
	reg(t, c, "other.prefix.thing", fakeMode{key: "thing"})
	got := Contributions[keyed](c, testPrefix)
	if len(got) != 1 || got[0].Key != "good" {
		t.Errorf("got %+v, want only the well-typed one under this prefix", got)
	}
}

func TestContributionsToleratesNil(t *testing.T) {
	if got := Contributions[keyed](nil, testPrefix); got != nil {
		t.Errorf("nil core returned %+v", got)
	}
	c := &core.Core{}
	if got := Contributions[keyed](c, ""); got != nil {
		t.Errorf("empty prefix returned %+v — it would match every key in the registry", got)
	}
}

func TestContributedResolvesOne(t *testing.T) {
	c := &core.Core{}
	reg(t, c, testPrefix+"apply", fakeMode{key: "apply"})

	if got, ok := Contributed[keyed](c, testPrefix, "apply"); !ok || got.ModeKey() != "apply" {
		t.Errorf("got %v/%v, want the apply mode", got, ok)
	}
	if _, ok := Contributed[keyed](c, testPrefix, "nothing"); ok {
		t.Error("an unregistered key resolved")
	}
	if _, ok := Contributed[keyed](c, testPrefix, ""); ok {
		t.Error("an empty key resolved — it would look up the bare prefix")
	}
}

// TestConsistentKeysDropsADisagreement is the silent failure this exists for.
// The registered key is what gets stored and submitted; the value's own key is
// what code branches on. When they differ, one half of the site believes a
// setting took and the other does not — which an operator experiences as the
// site quietly reverting their choice.
func TestConsistentKeysDropsADisagreement(t *testing.T) {
	c := &core.Core{}
	reg(t, c, testPrefix+"honest", fakeMode{key: "honest"})
	reg(t, c, testPrefix+"liar", fakeMode{key: "something-else"})
	reg(t, c, testPrefix+"silent", fakeMode{key: ""})

	got := ConsistentKeys(Contributions[keyed](c, testPrefix), func(m keyed) string { return m.ModeKey() })
	if len(got) != 1 || got[0].Key != "honest" {
		t.Errorf("got %+v, want only the one whose key matches its registration", got)
	}
}

func TestContributedValuesDropsTheKeys(t *testing.T) {
	c := &core.Core{}
	reg(t, c, testPrefix+"b", fakeMode{key: "b"})
	reg(t, c, testPrefix+"a", fakeMode{key: "a"})
	got := ContributedValues[keyed](c, testPrefix)
	if len(got) != 2 || got[0].ModeKey() != "a" || got[1].ModeKey() != "b" {
		t.Errorf("got %v, want [a b] — still ordered", got)
	}
}
