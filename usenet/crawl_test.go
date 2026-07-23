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

// TestGroupCutoffFallback pins the per-group retention rule. A group with no
// override must follow the plugin-wide crawl depth — storing a copied number
// instead would pin it to whatever the default happened to be when it was added,
// so raising the global depth later would silently skip that group.
func TestGroupCutoffFallback(t *testing.T) {
	cfg := Config{RetentionDays: 100}

	global := groupRow{Name: "a"}.cutoff(cfg)
	wantGlobal := time.Now().AddDate(0, 0, -100)
	if global.Sub(wantGlobal) > time.Minute || wantGlobal.Sub(global) > time.Minute {
		t.Errorf("no override should use the global depth: got %v, want ~%v", global, wantGlobal)
	}

	// An override wins and is independent of the global.
	over := groupRow{Name: "b", RetentionDays: 7}.cutoff(cfg)
	wantOver := time.Now().AddDate(0, 0, -7)
	if over.Sub(wantOver) > time.Minute || wantOver.Sub(over) > time.Minute {
		t.Errorf("override should win: got %v, want ~%v", over, wantOver)
	}
	if !over.After(global) {
		t.Error("a shallower per-group depth should produce a LATER cutoff than the global one")
	}

	// Zero means "no override" (composite literals need parens in a condition).
	zero := groupRow{Name: "c", RetentionDays: 0}.cutoff(cfg)
	none := groupRow{Name: "c"}.cutoff(cfg)
	if !zero.Equal(none) {
		t.Error("zero retention should mean 'follow the global depth'")
	}
}
