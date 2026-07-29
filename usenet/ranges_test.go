package usenet

import (
	"reflect"
	"testing"
)

// TestGapsBetween covers the rule backfill now runs on: what is still missing is
// the complement of the fetched runs. Gaps come back NEWEST FIRST because
// history is walked downward.
func TestGapsBetween(t *testing.T) {
	cases := []struct {
		name      string
		covered   []articleRange
		low, high int64
		want      []articleRange
	}{
		{
			name: "nothing covered = one whole gap",
			low:  1, high: 1000,
			want: []articleRange{{1, 1000}},
		},
		{
			name:    "fully covered = no gaps",
			covered: []articleRange{{1, 1000}},
			low:     1, high: 1000,
			want: nil,
		},
		{
			name:    "hole in the middle",
			covered: []articleRange{{1, 300}, {700, 1000}},
			low:     1, high: 1000,
			want: []articleRange{{301, 699}},
		},
		{
			name:    "two holes, newest first",
			covered: []articleRange{{200, 300}, {600, 700}},
			low:     1, high: 1000,
			want: []articleRange{{701, 1000}, {301, 599}, {1, 199}},
		},
		{
			name:    "unsorted input is handled",
			covered: []articleRange{{600, 700}, {200, 300}},
			low:     1, high: 1000,
			want: []articleRange{{701, 1000}, {301, 599}, {1, 199}},
		},
		{
			name:    "ranges outside the window are ignored",
			covered: []articleRange{{1, 50}, {5000, 6000}},
			low:     100, high: 200,
			want: []articleRange{{100, 200}},
		},
		{
			name:    "ranges are clipped to the window",
			covered: []articleRange{{1, 150}},
			low:     100, high: 200,
			want: []articleRange{{151, 200}},
		},
		{
			name:    "overlapping covered ranges collapse",
			covered: []articleRange{{100, 300}, {200, 400}},
			low:     1, high: 500,
			want: []articleRange{{401, 500}, {1, 99}},
		},
		{
			name: "empty window",
			low:  500, high: 400,
			want: nil,
		},
		{
			name:    "single article gap",
			covered: []articleRange{{1, 99}, {101, 200}},
			low:     1, high: 200,
			want: []articleRange{{100, 100}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gapsBetween(tc.covered, tc.low, tc.high)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("gapsBetween = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGapsBetweenDoesNotMutateInput: callers pass a slice they still hold (the
// stored coverage), so the complement calculation must not reorder or clip it.
func TestGapsBetweenDoesNotMutateInput(t *testing.T) {
	covered := []articleRange{{600, 700}, {1, 150}}
	orig := append([]articleRange(nil), covered...)
	_ = gapsBetween(covered, 100, 1000)
	if !reflect.DeepEqual(covered, orig) {
		t.Errorf("input was mutated: got %v, want %v", covered, orig)
	}
}

// TestGapJobs: gaps become batch jobs newest-article-first, and the shared
// budget caps a pass so one group cannot monopolise the connection pool.
func TestGapJobs(t *testing.T) {
	gaps := []articleRange{{1, 1000}}

	jobs := gapJobs("alt.test", gaps, 300, 100)
	want := []batchJob{
		{group: "alt.test", lo: 701, hi: 1000},
		{group: "alt.test", lo: 401, hi: 700},
		{group: "alt.test", lo: 101, hi: 400},
		{group: "alt.test", lo: 1, hi: 100}, // clipped to the gap start
	}
	if !reflect.DeepEqual(jobs, want) {
		t.Errorf("jobs = %v, want %v", jobs, want)
	}

	// Budget stops the queue mid-gap.
	if got := gapJobs("alt.test", gaps, 300, 2); len(got) != 2 {
		t.Errorf("budget 2: got %d jobs, want 2", len(got))
	}
	if got := gapJobs("alt.test", gaps, 300, 0); len(got) != 0 {
		t.Errorf("budget 0: got %d jobs, want 0", len(got))
	}

	// Budget spans multiple gaps.
	multi := []articleRange{{900, 1000}, {1, 100}}
	got := gapJobs("alt.test", multi, 3000, 10)
	if len(got) != 2 {
		t.Fatalf("got %d jobs, want 2 (one per gap, each smaller than a batch)", len(got))
	}
	if got[0].lo != 900 || got[0].hi != 1000 {
		t.Errorf("first job = %+v, want 900-1000 (newest gap first)", got[0])
	}
}

// TestGapJobsBatchLargerThanGap: a gap smaller than one batch yields exactly one
// job clipped to the gap, never a range extending past it (which would refetch
// already-covered articles and, worse, record coverage we never verified).
func TestGapJobsBatchLargerThanGap(t *testing.T) {
	jobs := gapJobs("g", []articleRange{{500, 520}}, 3000, 10)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].lo != 500 || jobs[0].hi != 520 {
		t.Errorf("job = %+v, want exactly 500-520", jobs[0])
	}
}

// The bug the operator spotted as "backfilling — 0 / 1 groups".
//
// gapJobs was handed the WHOLE remaining pass budget on the first iteration, so
// the first group with gaps took every batch and `budget <= 0` broke the loop
// before any other group was even looked at. Production had four groups needing
// history and the leading one still had 2.48 billion articles to go — over two
// months during which the other three would not advance a single batch.
func TestBackfillBudgetIsSharedNotMonopolised(t *testing.T) {
	// Four groups, each with far more work than one pass can take — the
	// production shape, where nobody ever runs out.
	deep := func(name string, n int) []batchJob {
		out := make([]batchJob, n)
		for i := range out {
			out[i] = batchJob{group: name, lo: i * 100, hi: i*100 + 99}
		}
		return out
	}
	groups := [][]batchJob{
		deep("alt.binaries.nzb", 500),
		deep("alt.binaries.multimedia.anime.highspeed", 500),
		deep("alt.binaries.movies.repost", 500),
		deep("alt.binaries.anime.repost", 500),
	}

	jobs, taken := shareBudget(groups, 25)
	if len(jobs) != 25 {
		t.Errorf("allocated %d batches, want the whole budget of 25", len(jobs))
	}
	for i, n := range taken {
		if n == 0 {
			t.Errorf("group %d got ZERO batches — this is the monopolisation bug: "+
				"one group with a deep history starves every other group indefinitely", i)
		}
	}
	// Roughly even: 25 across 4 groups is 6 each with one remainder.
	for i, n := range taken {
		if n > 8 {
			t.Errorf("group %d took %d of 25 batches; the split should be near-even, got %v", i, n, taken)
		}
	}

	// Fairness must not waste the pass. When only one group has work, it should
	// still get everything — otherwise sharing costs throughput.
	jobs, taken = shareBudget([][]batchJob{deep("only", 500)}, 25)
	if len(jobs) != 25 || taken[0] != 25 {
		t.Errorf("a lone group got %d of 25 batches; sharing must not throttle a single-group pass", taken[0])
	}

	// A group with only a little history left must not hold back the remainder:
	// its leftovers go to groups that still have work.
	jobs, taken = shareBudget([][]batchJob{deep("nearly-done", 2), deep("deep", 500)}, 25)
	if len(jobs) != 25 {
		t.Errorf("allocated %d of 25 when one group ran out early — the remainder must be reallocated", len(jobs))
	}
	if taken[0] != 2 {
		t.Errorf("the shallow group took %d, want its available 2", taken[0])
	}
	if taken[1] != 23 {
		t.Errorf("the deep group took %d, want the remaining 23", taken[1])
	}

	// Degenerate inputs must not spin or panic.
	if jobs, taken := shareBudget(nil, 25); len(jobs) != 0 || len(taken) != 0 {
		t.Error("no groups should allocate nothing")
	}
	if jobs, _ := shareBudget(groups, 0); len(jobs) != 0 {
		t.Error("a zero budget should allocate nothing")
	}
	// More groups than budget: some go without this pass, but it must terminate
	// and spend everything it has.
	if jobs, _ := shareBudget(groups, 2); len(jobs) != 2 {
		t.Errorf("allocated %d with a budget of 2", len(jobs))
	}
}
