package games

import (
	"bytes"
	"testing"
)

// The draw must be reachable by every contributor, weighted by giving, and
// must never fail to pick once anyone has given — a pot that closes with no
// winner kept everyone's points.
func TestPickWeighted(t *testing.T) {
	totals := map[int64]int64{1: 100, 2: 300, 3: 600}

	// A reader of zeros lands the draw at 0 → the lowest id wins; a reader
	// pinned high lands past everyone but the last. Both prove the walk
	// covers the whole range in sorted order.
	if got := pickWeighted(bytes.NewReader(make([]byte, 64)), totals); got != 1 {
		t.Errorf("zero draw picked %d, want 1 (first in sorted order)", got)
	}

	// Nobody gave: no winner, and no panic.
	if got := pickWeighted(bytes.NewReader(make([]byte, 64)), map[int64]int64{}); got != 0 {
		t.Errorf("empty pot picked %d, want 0", got)
	}
	// Zero-amount rows are not tickets.
	if got := pickWeighted(bytes.NewReader(make([]byte, 64)), map[int64]int64{7: 0}); got != 0 {
		t.Errorf("zero-only pot picked %d, want 0", got)
	}

	// A failed entropy read must still crown someone — the largest giver.
	if got := pickWeighted(failReader{}, totals); got != 3 {
		t.Errorf("entropy failure picked %d, want the largest contributor 3", got)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, bytes.ErrTooLarge }

// The split is even to within one point, sums exactly, and fronts the
// remainder — give() sorts poorest first, so the extra lands on need.
func TestSplitEven(t *testing.T) {
	shares := splitEven(1000, 3)
	if len(shares) != 3 || shares[0] != 334 || shares[1] != 333 || shares[2] != 333 {
		t.Fatalf("splitEven(1000,3) = %v", shares)
	}
	var sum int64
	for _, s := range shares {
		sum += s
	}
	if sum != 1000 {
		t.Fatalf("shares sum to %d, want 1000 — points must never appear or vanish in the split", sum)
	}
	if got := splitEven(5, 0); len(got) != 0 {
		t.Fatalf("splitEven(_, 0) = %v, want empty", got)
	}
}

// The ratio bands are a closed set: a hand-crafted POST with 47.3 is a typo,
// not a policy.
func TestValidCharityRatio(t *testing.T) {
	for _, ok := range charityRatios {
		if !validCharityRatio(ok) {
			t.Errorf("%v refused", ok)
		}
	}
	for _, bad := range []float64{0, 0.3, 1.5, -1} {
		if validCharityRatio(bad) {
			t.Errorf("%v accepted", bad)
		}
	}
}
