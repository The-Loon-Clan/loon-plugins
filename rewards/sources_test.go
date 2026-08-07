package rewards

import (
	"strings"
	"testing"
)

// The naming the operator asked for: "First post" at one, "100 posts" above.
func TestSuggestName(t *testing.T) {
	post := SourceDef{Key: "posts.created", Unit: "post", Units: "posts"}
	fill := SourceDef{Key: "requests.filled", Unit: "fill", Units: "fills"}
	// A source that gives only the singular still names sensibly — the plural
	// is a nicety, not a requirement, and demanding it would be a boot error
	// over an "s".
	entry := SourceDef{Key: "entries", Unit: "entry"}

	for _, tc := range []struct {
		def       SourceDef
		threshold int64
		want      string
	}{
		{post, 1, "First post"},
		{post, 100, "100 posts"},
		{fill, 1, "First fill"},
		{fill, 25, "25 fills"},
		{entry, 5, "5 entrys"}, // naive plural, and visibly the host's job to override
		// Zero and negatives cannot be stored (CHECK threshold > 0) but must
		// not produce "0 posts" if one ever reaches the suggester.
		{post, 0, "First post"},
	} {
		if got := tc.def.SuggestName(tc.threshold); got != tc.want {
			t.Errorf("SuggestName(%q, %d) = %q, want %q", tc.def.Key, tc.threshold, got, tc.want)
		}
	}

	// No unit, no suggestion — better an empty field the admin fills than a
	// name like "100 ".
	if got := (SourceDef{Key: "x"}).SuggestName(100); got != "" {
		t.Errorf("a unitless source suggested %q, want empty", got)
	}
}

// A def that cannot work must be refused at boot, not offered in a dropdown.
// An operator picking a dead row configures something that looks right and
// never fires, which is the failure the catalogue exists to remove.
func TestSourceDefValid(t *testing.T) {
	ok := SourceDef{Key: "posts.created", Label: "Posts", Fires: true, Counts: true, Unit: "post"}
	if err := ok.Valid(); err != nil {
		t.Errorf("a complete def was refused: %v", err)
	}
	// Fires-only needs no unit: nothing counts it, so nothing names it.
	if err := (SourceDef{Key: "pw.changed", Label: "Password changed", Fires: true}).Valid(); err != nil {
		t.Errorf("a fires-only def was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		def  SourceDef
		want string
	}{
		{"no key", SourceDef{Label: "x", Fires: true}, "no key"},
		{"no label", SourceDef{Key: "k", Fires: true}, "no label"},
		{"neither fires nor counts", SourceDef{Key: "k", Label: "x"}, "neither fires nor counts"},
		{"counts with no unit", SourceDef{Key: "k", Label: "x", Counts: true}, "names no unit"},
	} {
		err := tc.def.Valid()
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// Triggers and Metrics are different questions, and a source answers them
// independently — most answer both, which is why one flag could not do it.
func TestCatalogueSplitsFiresFromCounts(t *testing.T) {
	cat := SourceCatalog{
		{Key: "both", Label: "Both", Fires: true, Counts: true, Unit: "x"},
		{Key: "event-only", Label: "Event", Fires: true},
		{Key: "counter-only", Label: "Counter", Counts: true, Unit: "y"},
	}
	if got := cat.Triggers().Keys(); len(got) != 2 || got[0] != "both" || got[1] != "event-only" {
		t.Errorf("Triggers() = %v, want [both event-only]", got)
	}
	if got := cat.Metrics().Keys(); len(got) != 2 || got[0] != "both" || got[1] != "counter-only" {
		t.Errorf("Metrics() = %v, want [both counter-only]", got)
	}
}

// Every stock entry has to survive its own validation, or a host copying the
// list gets a boot error out of the box.
func TestStockSourcesAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range StockSources() {
		if err := d.Valid(); err != nil {
			t.Errorf("stock source %q is invalid: %v", d.Key, err)
		}
		if seen[d.Key] {
			t.Errorf("stock source %q is listed twice", d.Key)
		}
		seen[d.Key] = true
		if d.Group == "" {
			t.Errorf("stock source %q has no group; the dropdown cannot bucket it", d.Key)
		}
	}
	// The set the operator named.
	for _, want := range []string{
		"posts.created", "uploads.created", "comments.created",
		"requests.created", "requests.filled",
	} {
		if !seen[want] {
			t.Errorf("stock catalogue is missing %q", want)
		}
	}
}

// The picker prefers the declaration, and never drops a key already in use —
// a rename in the catalogue must not make an existing reward's own trigger
// vanish from the dropdown that edits it.
func TestTriggerOptionsPreferTheCatalogue(t *testing.T) {
	p := &Plugin{sources: SourceCatalog{
		{Key: "posts.created", Label: "Posts", Fires: true, Counts: true, Unit: "post"},
		{Key: "counter-only", Label: "Counter", Counts: true, Unit: "y"},
	}}
	got := p.triggerOptions([]Reward{{Slug: "legacy", Trigger: "old.name"}})

	has := func(k string) bool {
		for _, g := range got {
			if g == k {
				return true
			}
		}
		return false
	}
	if !has("posts.created") {
		t.Error("a declared trigger is missing from the picker")
	}
	if has("counter-only") {
		t.Error("a counter-only source was offered as a trigger")
	}
	if !has("old.name") {
		t.Error("a trigger already in use vanished from the picker that edits it")
	}

	// With nothing declared, the old derived behaviour stands.
	empty := &Plugin{}
	if got := empty.triggerOptions(nil); len(got) == 0 {
		t.Error("an install with no catalogue got an empty picker")
	}
}

func TestSchedulesAreLookedUpByKey(t *testing.T) {
	s, ok := Schedule("hourly")
	if !ok || s.Minutes != 60 {
		t.Errorf("Schedule(hourly) = %+v, %v", s, ok)
	}
	if _, ok := Schedule("every-blue-moon"); ok {
		t.Error("an unknown schedule resolved")
	}
	for _, s := range Schedules() {
		if s.Minutes <= 0 || s.Label == "" {
			t.Errorf("schedule %q is unusable: %+v", s.Key, s)
		}
	}
}
