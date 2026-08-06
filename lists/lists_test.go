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

func lst(id int) List { return List{ID: id} }

func ids(ls []List) []int {
	out := make([]int, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

func TestDedupPublicLists(t *testing.T) {
	t.Run("dedups across axes keeping first occurrence order", func(t *testing.T) {
		newLists := []List{lst(1), lst(2)}
		topLists := []List{lst(2), lst(3)}   // 2 already seen
		topGrabbed := []List{lst(3), lst(4)} // 3 already seen
		got := ids(dedupPublicLists(newLists, topLists, topGrabbed))
		want := []int{1, 2, 3, 4}
		if !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("preserves within-axis order", func(t *testing.T) {
		got := ids(dedupPublicLists([]List{lst(5), lst(1), lst(9)}))
		if want := []int{5, 1, 9}; !equalInts(got, want) {
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
		got := dedupPublicLists(nil, []List{}, nil)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", ids(got))
		}
	})

	t.Run("duplicate within a single axis is deduped", func(t *testing.T) {
		got := ids(dedupPublicLists([]List{lst(7), lst(7), lst(8)}))
		if want := []int{7, 8}; !equalInts(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
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
