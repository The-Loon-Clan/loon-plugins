package usenet

import (
	"testing"
	"time"
)

// TestCoverageCellsFullAndEmpty: the two ends of the scale must be exact, since
// they are what "backfill complete" and "never touched" look like on the page.
func TestCoverageCellsFullAndEmpty(t *testing.T) {
	full := coverageCells([]articleRange{{Start: 1, End: 1000}}, 1, 1000, 10)
	for i, c := range full {
		if c < 0.999 {
			t.Errorf("cell %d = %.3f for fully covered input, want 1", i, c)
		}
	}
	empty := coverageCells(nil, 1, 1000, 10)
	for i, c := range empty {
		if c != 0 {
			t.Errorf("cell %d = %.3f with no ranges, want 0", i, c)
		}
	}
}

// TestCoverageCellsLocatesTheHole is the whole point of the sparkline: a gap in
// the middle of a group must render in the middle, not smeared across the bar.
func TestCoverageCellsLocatesTheHole(t *testing.T) {
	// [0..999] covered except [400..599] — cells 4 and 5 of 10.
	cells := coverageCells([]articleRange{{Start: 0, End: 399}, {Start: 600, End: 999}}, 0, 999, 10)
	for i, c := range cells {
		want := 1.0
		if i == 4 || i == 5 {
			want = 0
		}
		if diff := c - want; diff > 0.01 || diff < -0.01 {
			t.Errorf("cell %d = %.3f, want %.0f", i, c, want)
		}
	}
}

// TestCoverageCellsPartialFill: a range covering half a slice must read as half,
// not round to full or to empty — otherwise a sparsely backfilled group looks
// either finished or untouched.
func TestCoverageCellsPartialFill(t *testing.T) {
	cells := coverageCells([]articleRange{{Start: 0, End: 49}}, 0, 99, 2)
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(cells))
	}
	if cells[0] < 0.99 {
		t.Errorf("first cell = %.3f, want ~1", cells[0])
	}
	if cells[1] > 0.01 {
		t.Errorf("second cell = %.3f, want ~0", cells[1])
	}
}

// TestCoverageCellsTinyRangeStillVisible: one small fetched batch inside a huge
// group must not round away to nothing. That sparse tail is exactly what an
// operator is looking at when they ask whether backfill is making progress.
func TestCoverageCellsTinyRangeStillVisible(t *testing.T) {
	cells := coverageCells([]articleRange{{Start: 500_000, End: 500_100}}, 0, 10_000_000, 48)
	levels := cellLevels(cells)
	any := false
	for _, l := range levels {
		if l > 0 {
			any = true
		}
	}
	if !any {
		t.Error("a fetched range rounded away to an entirely empty bar")
	}
}

// TestCoverageCellsClampsOutOfRange: ranges recorded before server_low moved (or
// against a since-expired horizon) must not write outside the slice or panic.
func TestCoverageCellsClampsOutOfRange(t *testing.T) {
	cells := coverageCells([]articleRange{{Start: -500, End: 5_000}}, 0, 99, 4)
	if len(cells) != 4 {
		t.Fatalf("got %d cells, want 4", len(cells))
	}
	for i, c := range cells {
		if c < 0.99 || c > 1.0 {
			t.Errorf("cell %d = %.3f, want exactly 1 (clamped, never over)", i, c)
		}
	}
	if got := coverageCells([]articleRange{{Start: 1, End: 2}}, 100, 100, 4); got != nil {
		t.Errorf("zero-width span returned %v, want nil", got)
	}
}

// TestCoverageCellsOverlapDoesNotExceedFull: unmerged or duplicated ranges must
// not push a cell past 1 and produce a level nothing renders.
func TestCoverageCellsOverlapDoesNotExceedFull(t *testing.T) {
	dup := []articleRange{{Start: 0, End: 99}, {Start: 0, End: 99}, {Start: 20, End: 40}}
	for i, c := range coverageCells(dup, 0, 99, 5) {
		if c > 1.0 {
			t.Errorf("cell %d = %.3f, want <= 1", i, c)
		}
	}
}

func TestCellLevels(t *testing.T) {
	got := cellLevels([]float64{0, 0.001, 0.4, 0.5, 0.9, 1})
	want := []int{0, 1, 1, 2, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("level[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestBackfillETASilentWithoutData: an ETA invented from a zero rate would read
// as "instant" on a crawler that has never run, which is worse than no ETA.
func TestBackfillETASilentWithoutData(t *testing.T) {
	if _, ok := backfillETA(1000, 0); ok {
		t.Error("returned an ETA with no measured rate")
	}
	if _, ok := backfillETA(0, 500); ok {
		t.Error("returned an ETA with nothing left to backfill")
	}
	d, ok := backfillETA(3600, 1)
	if !ok || d != time.Hour {
		t.Errorf("got %v (ok=%v), want 1h", d, ok)
	}
}

func TestFmtETA(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "< 1 min"},
		{90 * time.Second, "2 min"},
		{3 * time.Hour, "3 hours"},
		{72 * time.Hour, "3 days"},
	}
	for _, c := range cases {
		if got := fmtETA(c.d); got != c.want {
			t.Errorf("fmtETA(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestTrackerRateIgnoresYoungPass: the first seconds of a pass are connection
// setup, so dividing by them yields an ETA off by orders of magnitude. A young
// pass must fall back to the last completed one.
func TestTrackerRateIgnoresYoungPass(t *testing.T) {
	var tr passTracker
	tr.passStart(1)
	tr.noteBatch(1000, 1000, 1000, true)
	tr.passEnd()
	tr.last.Started = time.Now().Add(-100 * time.Second)
	tr.last.Finished = tr.last.Started.Add(100 * time.Second)
	settled := tr.rate()
	if settled < 9 || settled > 11 {
		t.Fatalf("completed-pass rate = %.2f, want ~10 art/s", settled)
	}

	tr.passStart(1)
	tr.noteBatch(5, 5, 5, true) // a couple of articles, a moment in
	if got := tr.rate(); got != settled {
		t.Errorf("young pass rate = %.2f, want the settled %.2f", got, settled)
	}
}
