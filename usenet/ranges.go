package usenet

import (
	"context"
	"sort"

	"github.com/jmoiron/sqlx"
)

// articleRange is a closed span of article numbers [Start, End].
type articleRange struct {
	Start int64
	End   int64
}

// recordFetchedRange marks a span as covered, merging it with any overlapping or
// adjacent runs so the table holds one row per contiguous run rather than one
// per batch. Adjacency (not just overlap) is what keeps it from growing linearly
// with batch count: consecutive batches collapse into a single row.
func (s *PGStore) recordFetchedRange(ctx context.Context, group string, start, end int64) error {
	if start > end {
		start, end = end, start
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Absorb every run touching [start-1, end+1] (the ±1 makes abutting runs
		// merge too) and rewrite them as one span.
		_, err := tx.ExecContext(ctx,
			`WITH absorbed AS (
			     DELETE FROM newsgroup_ranges
			      WHERE group_name = $1
			        AND range_start <= $3 + 1
			        AND range_end   >= $2 - 1
			  RETURNING range_start, range_end
			 )
			 INSERT INTO newsgroup_ranges (group_name, range_start, range_end)
			 SELECT $1,
			        LEAST($2, COALESCE(MIN(range_start), $2)),
			        GREATEST($3, COALESCE(MAX(range_end), $3))
			   FROM absorbed`,
			group, start, end)
		return err
	})
}

// coveredRanges returns the merged runs for a group, ascending.
func (s *PGStore) coveredRanges(ctx context.Context, group string) ([]articleRange, error) {
	var rows []articleRange
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT range_start AS start, range_end AS end
			   FROM newsgroup_ranges WHERE group_name = $1 ORDER BY range_start`, group)
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// backfillGaps returns the uncovered spans within [low, high], NEWEST FIRST —
// backfill walks history downward, so the newest gap is the one to close next.
//
// Computed in Go rather than SQL: the complement of a set of ranges is awkward
// as a window query, the input is already merged and tiny, and doing it here
// keeps the rule ("what is still missing") readable and testable.
func (s *PGStore) backfillGaps(ctx context.Context, group string, low, high int64) ([]articleRange, error) {
	covered, err := s.coveredRanges(ctx, group)
	if err != nil {
		return nil, err
	}
	return gapsBetween(covered, low, high), nil
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
