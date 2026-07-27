package usenet

import (
	"testing"
	"time"
)

// A pass runs many catch-up rounds. The progress counters must describe the
// CURRENT round, or the bar measures a denominator that only grows: prod
// published 542,460 / 520,000 batches after 26 rounds, where 520,000 was just
// 26 x the per-round budget.
func TestRoundScopedProgressCounters(t *testing.T) {
	var tr passTracker
	tr.passStart(2)

	// Round 1: two groups, three batches each, all completed.
	tr.roundStart()
	tr.noteGroups([]string{"alt.a", "alt.b"})
	tr.notePlanned("alt.a", 3)
	tr.notePlanned("alt.b", 3)
	for i := 0; i < 3; i++ {
		tr.noteBatchFor("alt.a", 100, 10, 1000, true)
		tr.noteBatchFor("alt.b", 100, 10, 1000, true)
	}
	cur, _ := tr.snapshot()
	if cur.Round != 1 {
		t.Errorf("Round = %d, want 1", cur.Round)
	}
	if cur.Batches != 6 || cur.BatchesTotal != 6 {
		t.Errorf("round 1: %d/%d batches, want 6/6", cur.Batches, cur.BatchesTotal)
	}
	if cur.Groups != 2 || cur.GroupsDone != 2 {
		t.Errorf("round 1: %d/%d groups done, want 2/2", cur.GroupsDone, cur.Groups)
	}

	// Round 2: one group, two batches. The progress counters must RESTART.
	tr.roundStart()
	tr.noteGroups([]string{"alt.a"})
	tr.notePlanned("alt.a", 2)
	tr.noteBatchFor("alt.a", 100, 10, 1000, true)

	cur, _ = tr.snapshot()
	if cur.Round != 2 {
		t.Errorf("Round = %d, want 2", cur.Round)
	}
	if cur.BatchesTotal != 2 {
		t.Errorf("round 2 denominator = %d, want 2 — it must not carry round 1's work", cur.BatchesTotal)
	}
	if cur.Batches != 1 {
		t.Errorf("round 2 numerator = %d, want 1", cur.Batches)
	}
	if cur.Groups != 1 {
		t.Errorf("round 2 groups = %d, want 1 — groupsSeen must reset", cur.Groups)
	}
	// A group finished in round 1 and re-planned in round 2 is NOT done.
	if cur.GroupsDone != 0 {
		t.Errorf("round 2 groupsDone = %d, want 0 — a re-planned group is not still done", cur.GroupsDone)
	}

	// The pass totals must survive the round boundary: they are what the stats
	// widget derives its rate from, and a counter that goes backwards makes the
	// rate negative.
	if cur.Articles != 700 {
		t.Errorf("Articles = %d, want 700 carried across rounds", cur.Articles)
	}
	if cur.Staged != 70 {
		t.Errorf("Staged = %d, want 70 carried across rounds", cur.Staged)
	}
	if cur.WireBytes != 7000 {
		t.Errorf("WireBytes = %d, want 7000 carried across rounds", cur.WireBytes)
	}
	if cur.Started.IsZero() {
		t.Error("pass Started must survive roundStart")
	}
	if cur.Providers != 2 {
		t.Errorf("Providers = %d, want 2 carried across rounds", cur.Providers)
	}
}

// Failed is pass-cumulative like the other totals: an operator asking "is this
// pass failing" wants the whole pass, not whichever round is open.
func TestRoundKeepsFailedCumulative(t *testing.T) {
	var tr passTracker
	tr.passStart(1)
	tr.roundStart()
	tr.notePlanned("alt.a", 1)
	tr.noteBatchFor("alt.a", 0, 0, 0, false)
	tr.roundStart()
	tr.notePlanned("alt.a", 1)
	tr.noteBatchFor("alt.a", 0, 0, 0, false)

	cur, _ := tr.snapshot()
	if cur.Failed != 2 {
		t.Errorf("Failed = %d, want 2 across both rounds", cur.Failed)
	}
}

// A pass that ends before any round opened must not claim round 1 ran.
func TestPassStartLeavesRoundZero(t *testing.T) {
	var tr passTracker
	tr.passStart(1)
	cur, _ := tr.snapshot()
	if cur.Round != 0 {
		t.Errorf("Round = %d before any roundStart, want 0", cur.Round)
	}
}

// The clamp: a batch can land after its round's denominator was replaced, and a
// bar wider than its track is a rendering bug stacked on a counting one.
func TestStatsVMClampsPctAt100(t *testing.T) {
	vm := statsVM(passStats{
		Started: time.Now().Add(-time.Minute), InProgress: true,
		Batches: 542460, BatchesTotal: 520000,
	})
	if vm.Pct != 100 {
		t.Errorf("Pct = %d for 542460/520000, want it clamped to 100", vm.Pct)
	}
	vm = statsVM(passStats{Started: time.Now().Add(-time.Minute), Batches: 1, BatchesTotal: 4})
	if vm.Pct != 25 {
		t.Errorf("Pct = %d for 1/4, want 25", vm.Pct)
	}
	// No denominator yet must not divide by zero.
	vm = statsVM(passStats{Started: time.Now().Add(-time.Minute), Batches: 5, BatchesTotal: 0})
	if vm.Pct != 0 {
		t.Errorf("Pct = %d with no planned work, want 0", vm.Pct)
	}
}
