package pluginapi

import (
	"testing"

	"github.com/the-loon-clan/loon/core"
)

func navCore(t *testing.T, entries map[string][]AdminNavEntry) *core.Core {
	t.Helper()
	c := &core.Core{}
	for plugin, es := range entries {
		es := es
		if err := RegisterAdminNav(c, plugin, func() []AdminNavEntry { return es }); err != nil {
			t.Fatalf("RegisterAdminNav(%s): %v", plugin, err)
		}
	}
	return c
}

func TestAdminNavEntriesCollectsEveryPlugin(t *testing.T) {
	c := navCore(t, map[string][]AdminNavEntry{
		"wiki":      {{Href: "/admin/wiki", Label: "Wiki"}},
		"news":      {{Href: "/admin/news", Label: "News"}},
		"donations": {{Href: "/admin/donate", Label: "Donations"}},
	})
	got := AdminNavEntries(c)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
}

// TestAdminNavOrdersByGroupThenWeight — a bar whose order depends on registry
// iteration would move between boots, and an operator learns a menu by
// position.
func TestAdminNavOrdersByGroupThenWeight(t *testing.T) {
	c := navCore(t, map[string][]AdminNavEntry{
		"z": {{Href: "/z", Label: "Z", Group: "Content", Weight: 20}},
		"a": {{Href: "/a", Label: "A", Group: "Content", Weight: 10}},
		"m": {{Href: "/m", Label: "M", Group: "Community"}},
		"n": {{Href: "/n", Label: "N"}}, // no group sorts first
	})
	got := AdminNavEntries(c)
	want := []string{"N", "M", "A", "Z"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Label != w {
			t.Errorf("position %d = %q, want %q (full: %+v)", i, got[i].Label, w, got)
		}
	}
}

// TestAdminNavIsStable. Registry iteration is map order underneath; a nav that
// reshuffled between boots is the kind of bug nobody files and everybody
// notices.
func TestAdminNavIsStable(t *testing.T) {
	c := navCore(t, map[string][]AdminNavEntry{
		"a": {{Href: "/a", Label: "A"}},
		"b": {{Href: "/b", Label: "B"}},
		"c": {{Href: "/c", Label: "C"}},
		"d": {{Href: "/d", Label: "D"}},
	})
	first := AdminNavEntries(c)
	for i := 0; i < 20; i++ {
		got := AdminNavEntries(c)
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("pass %d differs at %d: %+v vs %+v", i, j, got, first)
			}
		}
	}
}

// TestAdminNavDropsHalfFilledEntries. A blank item in a nav bar is a bug an
// operator cannot diagnose — it is a gap they click and nothing happens.
func TestAdminNavDropsHalfFilledEntries(t *testing.T) {
	c := navCore(t, map[string][]AdminNavEntry{
		"good": {{Href: "/admin/good", Label: "Good"}},
		"bad": {
			{Href: "", Label: "No href"},
			{Href: "/admin/nolabel", Label: ""},
		},
	})
	got := AdminNavEntries(c)
	if len(got) != 1 || got[0].Label != "Good" {
		t.Errorf("got %+v, want only the complete entry", got)
	}
}

// TestAdminNavSurvivesABadRegistration. A nil source, or something registered
// under the prefix that is not a source at all, must not take the whole bar
// down — every other plugin's links are still correct.
func TestAdminNavSurvivesABadRegistration(t *testing.T) {
	c := &core.Core{}
	if err := RegisterAdminNav(c, "good", func() []AdminNavEntry {
		return []AdminNavEntry{{Href: "/admin/good", Label: "Good"}}
	}); err != nil {
		t.Fatal(err)
	}
	// The wrong type under the right prefix: ContributedValues skips it.
	if err := c.Register(AdminNavPrefix+"wrong", "not a source"); err != nil {
		t.Fatal(err)
	}
	var nilSource AdminNavSource
	if err := c.Register(AdminNavPrefix+"nil", nilSource); err != nil {
		// Registering a typed nil is allowed by core; if it ever is not, the
		// guard in AdminNavEntries is still the one that matters.
		t.Logf("core refused a nil source: %v", err)
	}

	got := AdminNavEntries(c)
	if len(got) != 1 || got[0].Label != "Good" {
		t.Errorf("got %+v, want the one good entry", got)
	}
}

func TestAdminNavWithNothingRegistered(t *testing.T) {
	if got := AdminNavEntries(&core.Core{}); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}
