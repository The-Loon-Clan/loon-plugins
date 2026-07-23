package usenet

import (
	"math"
	"sort"
)

// articleRange is a closed span of article numbers [Start, End].
type articleRange struct {
	Start int64
	End   int64
}

// gapsBetween is the pure complement calculation, split out so it can be tested
// without a database. `covered` need not be sorted or merged.
func gapsBetween(covered []articleRange, low, high int64) []articleRange {
	if low > high {
		return nil
	}
	// Work on a copy: callers hand us a slice they may still be using.
	rs := make([]articleRange, 0, len(covered))
	for _, r := range covered {
		if r.End < low || r.Start > high {
			continue // entirely outside the window
		}
		if r.Start < low {
			r.Start = low
		}
		if r.End > high {
			r.End = high
		}
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })

	var gaps []articleRange
	cursor := low
	for _, r := range rs {
		if r.Start > cursor {
			gaps = append(gaps, articleRange{Start: cursor, End: r.Start - 1})
		}
		if r.End >= cursor {
			cursor = r.End + 1
		}
	}
	if cursor <= high {
		gaps = append(gaps, articleRange{Start: cursor, End: high})
	}

	// Newest first.
	for i, j := 0, len(gaps)-1; i < j; i, j = i+1, j-1 {
		gaps[i], gaps[j] = gaps[j], gaps[i]
	}
	return gaps
}

// gapJobs turns gaps into batch jobs, newest article first within each gap, and
// stops once `budget` batches are queued so one pass stays bounded.
func gapJobs(group string, gaps []articleRange, batch, budget int) []batchJob {
	if batch <= 0 {
		batch = 3000
	}
	var jobs []batchJob
	for _, g := range gaps {
		for end := g.End; end >= g.Start; end -= int64(batch) {
			if len(jobs) >= budget {
				return jobs
			}
			start := end - int64(batch) + 1
			if start < g.Start {
				start = g.Start
			}
			jobs = append(jobs, batchJob{group: group, lo: int(start), hi: int(end)})
		}
	}
	return jobs
}

// coverKey identifies one group's coverage on one backbone — the only scope in
// which article numbers mean anything.
type coverKey struct{ backbone, group string }

// coverageCells buckets merged ranges into n equal slices of [low, high] and
// returns the covered FRACTION of each, so the page can draw where the holes
// actually are rather than one flat "have" block.
//
// Bucketed rather than one element per range on purpose: parallel backfill
// leaves hundreds of separate runs per group, and a div per run is both
// unbounded page weight and invisible at typical article counts. A fixed cell
// count keeps the markup constant-size whatever the fragmentation.
func coverageCells(covered []articleRange, low, high int64, n int) []float64 {
	if n <= 0 || high <= low {
		return nil
	}
	cells := make([]float64, n)
	// +1 because the bounds are INCLUSIVE: [0..99] is a hundred articles, not
	// ninety-nine. Without it the last cell of a fully covered group overflows
	// past the end of the bar.
	span := float64(high - low + 1)
	for _, r := range covered {
		s, e := r.Start, r.End
		if s < low {
			s = low
		}
		if e > high {
			e = high
		}
		if s > e {
			continue
		}
		// Fractional cell positions: a range narrower than one cell still
		// contributes its real weight instead of rounding away to nothing.
		from := float64(s-low) / span * float64(n)
		to := float64(e+1-low) / span * float64(n)
		for c := int(from); c < n && float64(c) < to; c++ {
			if c < 0 {
				continue
			}
			lo, hi := math.Max(from, float64(c)), math.Min(to, float64(c+1))
			if hi > lo {
				cells[c] += hi - lo
			}
		}
	}
	for i := range cells {
		if cells[i] > 1 {
			cells[i] = 1
		}
	}
	return cells
}
