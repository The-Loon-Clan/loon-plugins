package usenet

import "testing"

// The "+N new" figure on /admin/crawlers.
//
// It printed ServerHigh - HighWatermark, which counts the propagation headroom
// planGroup deliberately leaves unfetched. So every group on the page reported
// the same "+6,000 new" — the default CrawlHeadroom, 2 x Batch — permanently,
// including groups with nothing whatsoever to do.
//
// The existing view tests could not catch it: they construct crawlerGroupVM
// values directly and assert on the rendered template, so the arithmetic that
// produced NewFmt was never executed by a test. That is why this is a plain
// function rather than a field computed inline in the render loop.
func TestPendingForwardExcludesCrawlHeadroom(t *testing.T) {
	const headroom = 6000 // the default: 2 x Batch, 2 x 3000

	cases := []struct {
		name                      string
		serverHigh, highWatermark int64
		want                      int64
	}{
		{
			// THE BUG. A fully caught-up group sits exactly one headroom below
			// the server's high mark, because that is where the crawl stops on
			// purpose. This reported 6,000 on every group, forever.
			name:       "caught up reports nothing",
			serverHigh: 1_000_000, highWatermark: 994_000, want: 0,
		},
		{
			// The distinction the column exists for, and the one the bug
			// destroyed: this group is genuinely 500 behind, and before the fix
			// it rendered "6,500" — indistinguishable at a glance from the idle
			// group above it.
			name:       "genuine backlog reports only the backlog",
			serverHigh: 1_000_000, highWatermark: 993_500, want: 500,
		},
		{
			name:       "large backlog",
			serverHigh: 1_000_000, highWatermark: 500_000, want: 494_000,
		},
		{
			// Mid-pass the watermark can sit above ServerHigh - headroom, since
			// ServerHigh is a snapshot from the last plan. Must not go negative
			// and must not render.
			name:       "watermark inside the headroom clamps to zero",
			serverHigh: 1_000_000, highWatermark: 999_999, want: 0,
		},
		{
			name:       "watermark past ServerHigh clamps to zero",
			serverHigh: 1_000_000, highWatermark: 1_000_500, want: 0,
		},
		{
			// Never crawled forward: the whole range is backfill, which the
			// Remaining column reports. Reporting it twice would double-count
			// a first-pass group's work.
			name:       "never crawled reports nothing",
			serverHigh: 1_000_000, highWatermark: 0, want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingForward(tc.serverHigh, tc.highWatermark, headroom)
			if got != tc.want {
				t.Errorf("pendingForward(%d, %d, %d) = %d, want %d",
					tc.serverHigh, tc.highWatermark, headroom, got, tc.want)
			}
		})
	}
}

// With headroom disabled (CrawlHeadroom = 0, which planGroup honours) the figure
// is the raw distance again — the fix must not hard-code the subtraction.
func TestPendingForwardWithHeadroomDisabled(t *testing.T) {
	if got := pendingForward(1_000_000, 994_000, 0); got != 6000 {
		t.Errorf("with headroom off, want the raw 6000, got %d", got)
	}
}

// The invariant behind the bug, stated directly: whatever the headroom is
// configured to, a group parked at the crawl ceiling reports nothing.
func TestPendingForwardIsZeroAtTheCrawlCeiling(t *testing.T) {
	for _, headroom := range []int64{0, 1, 3000, 6000, 40_000} {
		const serverHigh = 5_000_000
		// Exactly where planGroup stops: ceiling = high - CrawlHeadroom.
		ceiling := serverHigh - headroom
		if got := pendingForward(serverHigh, ceiling, headroom); got != 0 {
			t.Errorf("headroom %d: a group at the crawl ceiling reported %d, want 0",
				headroom, got)
		}
	}
}
