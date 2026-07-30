package usenet

import (
	"testing"
	"time"
)

// TestHorizonReached pins the two rules that keep backfill_done — the most
// consequential silent write in the pipeline, with no automatic reset and a
// full-history re-walk as the only recovery — from firing on bad evidence.
func TestHorizonReached(t *testing.T) {
	cutoff := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	old := cutoff.AddDate(0, -2, 0)      // well below the horizon
	recent := cutoff.AddDate(0, 2, 0)    // within retention
	ancient := cutoff.AddDate(-40, 0, 0) // one forged/broken Date header

	ok := func(minD, maxD time.Time) batchResult {
		return batchResult{ok: true, minDate: minD, maxDate: maxD}
	}

	cases := []struct {
		name string
		rs   []batchResult
		want bool
	}{
		{"whole batch below the cutoff, all ok",
			[]batchResult{ok(old, old), ok(recent, recent)}, true},

		// One failed batch means an unrecorded gap. recordBackfill's contract
		// — "a failed batch simply leaves its gap unrecorded, so the next pass
		// recomputes it and tries again" — only holds while the group stays
		// open, so the shortcut must not fire past a failure.
		{"old batch present but another batch failed",
			[]batchResult{ok(old, old), {ok: false}}, false},

		// The forged-date case that motivated the rule: one ancient Date
		// header in an otherwise-recent batch drags minDate below the cutoff,
		// but the batch's NEWEST article gives it away. Marking done here
		// permanently stranded everything below the back watermark.
		{"single ancient date inside a recent batch",
			[]batchResult{ok(ancient, recent)}, false},

		// Deep history where every article has expired: batches fetch fine but
		// carry no dates. The group finishes through gap re-derivation, not
		// through this shortcut.
		{"empty batches carry no evidence",
			[]batchResult{ok(time.Time{}, time.Time{})}, false},

		{"no batches", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := horizonReached(tc.rs, cutoff); got != tc.want {
				t.Errorf("horizonReached = %v, want %v", got, tc.want)
			}
		})
	}
}
