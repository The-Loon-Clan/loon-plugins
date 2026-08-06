package lists

import (
	"testing"
)

func TestSanitizeListFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "My Anime Picks", "My Anime Picks"},
		{"slash", "a/b", "a_b"},
		{"backslash", `a\b`, "a_b"},
		{"quote", `he said "hi"`, "he said _hi_"},
		{"colon", "S01:E02", "S01_E02"},
		{"star", "best*of", "best_of"},
		{"question", "what?", "what_"},
		{"angles", "<tag>", "_tag_"},
		{"pipe", "a|b", "a_b"},
		{"all reserved", `"/\:*?<>|`, "_________"},
		{"unicode kept", "アニメ — 2024", "アニメ — 2024"},
		{"empty", "", ""},
		{"dots and dashes kept", "top-10.list", "top-10.list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeListFilename(tt.in); got != tt.want {
				t.Errorf("sanitizeListFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// row stands in for the host record the grid template renders. The dedup
// returns these untouched, so the test asserts on identity, not on fields.
type row struct{ id int }

func lst(id int) ListRef { return ListRef{ID: id, Raw: &row{id}} }

// ids reads the ids back out of the returned host rows, which is the only
// way to check ordering — the plugin hands the rows through opaquely.
func ids(t *testing.T, out []any) []int {
	t.Helper()
	got := make([]int, len(out))
	for i, v := range out {
		r, ok := v.(*row)
		if !ok {
			t.Fatalf("entry %d is not the host row that went in: %T", i, v)
		}
		got[i] = r.id
	}
	return got
}

func TestDedupPublicLists(t *testing.T) {
	t.Run("dedups across axes keeping first occurrence order", func(t *testing.T) {
		newLists := []ListRef{lst(1), lst(2)}
		topLists := []ListRef{lst(2), lst(3)}   // 2 already seen
		topGrabbed := []ListRef{lst(3), lst(4)} // 3 already seen
		got := ids(t, dedupPublicLists(newLists, topLists, topGrabbed))
		want := []int{1, 2, 3, 4}
		if !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("preserves within-axis order", func(t *testing.T) {
		got := ids(t, dedupPublicLists([]ListRef{lst(5), lst(1), lst(9)}))
		if want := []int{5, 1, 9}; !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	// A row the host could not resolve must not reach the template, which
	// would range straight into the nil.
	t.Run("skips entries with no host row", func(t *testing.T) {
		got := ids(t, dedupPublicLists([]ListRef{lst(1), {ID: 3}, lst(2)}))
		if want := []int{1, 2}; !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("no axes yields empty non-nil slice", func(t *testing.T) {
		got := dedupPublicLists()
		if got == nil || len(got) != 0 {
			t.Errorf("got %v, want empty non-nil slice", got)
		}
	})

	t.Run("empty axes yield empty result", func(t *testing.T) {
		got := dedupPublicLists(nil, []ListRef{}, nil)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("duplicate within a single axis is deduped", func(t *testing.T) {
		got := ids(t, dedupPublicLists([]ListRef{lst(7), lst(7), lst(8)}))
		if want := []int{7, 8}; !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// The detail template reads far more of an item than the ZIP builder does, so
// unwrapping must hand back the host rows themselves.
func TestRawItemsUnwrapsHostRows(t *testing.T) {
	a, b := &row{1}, &row{2}
	out := rawItems([]ItemRef{
		{ID: 1, Filename: "a.nzb", Raw: a},
		{ID: 2, Filename: "b.nzb", Raw: b},
	})
	if len(out) != 2 || out[0] != any(a) || out[1] != any(b) {
		t.Fatalf("rawItems did not pass the host rows through: %v", out)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
