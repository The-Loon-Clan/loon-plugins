package usenet

import (
	"testing"
	"time"
)

func day(n int) time.Time { return time.Date(2026, 7, n, 0, 0, 0, 0, time.UTC) }

// TestContiguousEnd covers the rule that keeps parallel fetching from losing
// articles: the watermark may only advance across an UNBROKEN run of successful
// batches. Batches complete out of order, so "highest successful batch" would
// silently step over a failed range in the middle and those articles would never
// be fetched again.
func TestContiguousEnd(t *testing.T) {
	cases := []struct {
		name     string
		start    int
		results  []batchResult
		wantEnd  int
		wantDate time.Time
	}{
		{
			name:  "all contiguous and ok",
			start: 100,
			results: []batchResult{
				{lo: 100, hi: 199, ok: true, maxDate: day(1)},
				{lo: 200, hi: 299, ok: true, maxDate: day(3)},
				{lo: 300, hi: 399, ok: true, maxDate: day(2)},
			},
			wantEnd: 399, wantDate: day(3),
		},
		{
			name:  "results arrive out of order",
			start: 100,
			results: []batchResult{
				{lo: 300, hi: 399, ok: true, maxDate: day(2)},
				{lo: 100, hi: 199, ok: true, maxDate: day(1)},
				{lo: 200, hi: 299, ok: true, maxDate: day(3)},
			},
			wantEnd: 399, wantDate: day(3),
		},
		{
			name:  "failure in the middle stops the advance",
			start: 100,
			results: []batchResult{
				{lo: 100, hi: 199, ok: true, maxDate: day(1)},
				{lo: 200, hi: 299, ok: false},                 // failed
				{lo: 300, hi: 399, ok: true, maxDate: day(9)}, // succeeded, but unreachable
			},
			wantEnd: 199, wantDate: day(1), // day(9) must NOT leak past the break
		},
		{
			name:  "first batch fails: no advance at all",
			start: 100,
			results: []batchResult{
				{lo: 100, hi: 199, ok: false},
				{lo: 200, hi: 299, ok: true, maxDate: day(5)},
			},
			wantEnd: 99, // start-1
		},
		{
			name:  "hole in the ranges stops the advance",
			start: 100,
			results: []batchResult{
				{lo: 100, hi: 199, ok: true, maxDate: day(1)},
				{lo: 300, hi: 399, ok: true, maxDate: day(4)}, // 200-299 missing
			},
			wantEnd: 199, wantDate: day(1),
		},
		{
			name:    "no results",
			start:   100,
			results: nil,
			wantEnd: 99,
		},
		{
			name:     "single batch",
			start:    1,
			results:  []batchResult{{lo: 1, hi: 50, ok: true, maxDate: day(7)}},
			wantEnd:  50,
			wantDate: day(7),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, date := contiguousEnd(tc.start, tc.results)
			if end != tc.wantEnd {
				t.Errorf("end = %d, want %d", end, tc.wantEnd)
			}
			if !date.Equal(tc.wantDate) {
				t.Errorf("date = %v, want %v", date, tc.wantDate)
			}
		})
	}
}

// TestContiguousEndNoAdvanceSentinel documents the contract advanceWatermarks
// relies on: an end below start means "nothing to record", and the caller must
// not translate that into a watermark write.
func TestContiguousEndNoAdvanceSentinel(t *testing.T) {
	start := 500
	end, _ := contiguousEnd(start, []batchResult{{lo: 500, hi: 599, ok: false}})
	if end >= start {
		t.Fatalf("end = %d, want < start (%d) so the caller skips the write", end, start)
	}
}
