package usenet

import (
	"fmt"
	"testing"
	"time"
)

func groupsNamed(n int) []groupRow {
	out := make([]groupRow, n)
	for i := range out {
		out[i] = groupRow{Name: fmt.Sprintf("alt.binaries.group%02d", i)}
	}
	return out
}

// TestShareOfPartitions is the property that makes multi-crawler work: every
// group goes to exactly ONE worker, and together they cover everything. A gap
// means a group silently stops being crawled; an overlap means duplicate work.
func TestShareOfPartitions(t *testing.T) {
	groups := groupsNamed(40)
	for _, n := range []int{1, 2, 3, 5, 8} {
		workers := make([]string, n)
		for i := range workers {
			workers[i] = fmt.Sprintf("worker-%02d", i)
		}
		seen := map[string]int{}
		for _, w := range workers {
			for _, g := range shareOf(groups, workers, w) {
				seen[g.Name]++
			}
		}
		if len(seen) != len(groups) {
			t.Errorf("n=%d: %d of %d groups assigned — the rest would never be crawled",
				n, len(seen), len(groups))
		}
		for name, c := range seen {
			if c != 1 {
				t.Errorf("n=%d: %s assigned to %d workers, want exactly 1", n, name, c)
			}
		}
	}
}

// TestShareOfRoughlyEven: hash assignment need not be perfectly balanced, but a
// wildly lopsided split would leave one crawler doing most of the work.
func TestShareOfRoughlyEven(t *testing.T) {
	groups := groupsNamed(120)
	workers := []string{"a", "b", "c"}
	ideal := len(groups) / len(workers)
	for _, w := range workers {
		got := len(shareOf(groups, workers, w))
		if got < ideal/2 || got > ideal*2 {
			t.Errorf("worker %s got %d groups, ideal ~%d — split is too lopsided", w, got, ideal)
		}
	}
}

// TestShareOfStable: the same inputs must always produce the same split, or
// workers would trade groups between passes and thrash their watermarks.
func TestShareOfStable(t *testing.T) {
	groups := groupsNamed(20)
	workers := []string{"a", "b"}
	first := shareOf(groups, workers, "a")
	for i := 0; i < 5; i++ {
		again := shareOf(groups, workers, "a")
		if len(again) != len(first) {
			t.Fatalf("unstable split: %d then %d", len(first), len(again))
		}
		for j := range first {
			if again[j].Name != first[j].Name {
				t.Fatalf("unstable split at %d: %s vs %s", j, again[j].Name, first[j].Name)
			}
		}
	}
}

// TestShareOfGroupChurnIsLocal: adding a newsgroup must move only that group.
// Position-based assignment would reshuffle everything and make every worker
// re-crawl from another's watermark.
func TestShareOfGroupChurnIsLocal(t *testing.T) {
	workers := []string{"a", "b", "c"}
	before := groupsNamed(30)
	after := append(append([]groupRow{}, before...), groupRow{Name: "alt.binaries.brand.new"})

	moved := 0
	for _, w := range workers {
		was := map[string]bool{}
		for _, g := range shareOf(before, workers, w) {
			was[g.Name] = true
		}
		now := map[string]bool{}
		for _, g := range shareOf(after, workers, w) {
			now[g.Name] = true
		}
		for name := range was {
			if !now[name] {
				moved++
			}
		}
	}
	if moved != 0 {
		t.Errorf("%d existing group(s) changed owner when one group was added; want 0", moved)
	}
}

// TestShareOfNonMemberWaits: a crawler that joined mid-term is not in the member
// list and must take nothing — that is the "a third crawler waits" behaviour.
func TestShareOfNonMemberWaits(t *testing.T) {
	groups := groupsNamed(10)
	if got := shareOf(groups, []string{"a", "b"}, "newcomer"); len(got) != 0 {
		t.Errorf("non-member got %d groups, want 0 (it must wait for the next term)", len(got))
	}
	// A single worker takes everything even if it isn't listed — a lone crawler
	// must never stall waiting for a quorum it is the only member of.
	if got := shareOf(groups, []string{"a"}, "somebody-else"); len(got) != len(groups) {
		t.Errorf("single-worker case got %d groups, want all %d", len(got), len(groups))
	}
}

// TestTermStartAgrees: every worker computes the boundary independently, so they
// must all land on the same instant or they would disagree about membership.
func TestTermStartAgrees(t *testing.T) {
	term := 15 * time.Minute
	base := time.Date(2026, 7, 22, 10, 7, 30, 0, time.UTC)
	want := termStart(base, term)
	for _, off := range []time.Duration{0, time.Second, 2 * time.Minute, 7*time.Minute + 29*time.Second} {
		if got := termStart(base.Add(off), term); !got.Equal(want) {
			t.Errorf("+%v landed on a different term: %v vs %v", off, got, want)
		}
	}
	// Crossing the boundary must advance it.
	if next := termStart(base.Add(10*time.Minute), term); !next.After(want) {
		t.Error("crossing the boundary did not advance the term")
	}
}
