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
