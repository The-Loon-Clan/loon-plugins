package usenet

import (
	"context"
	"errors"
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

// Share within a tier, never across one.
//
// The even-share fix cured one starvation and caused a worse one. Production
// had exactly ONE unfinished critical group — alt.binaries.multimedia.anime
// .highspeed, 684 million articles — and hold_low_until_backfilled means every
// LOW group stays uncrawled until it finishes. Splitting the pass three ways
// between that group and two LOW ones (alt.binaries.nzb at 2.48 BILLION, and
// movies.repost) spent two thirds of every pass on history that is itself gated
// behind the group being starved, tripling how long the gate holds.
func TestBackfillSpendsOnTheHighestTierWithWork(t *testing.T) {
	batches := func(name string, n int) []batchJob {
		out := make([]batchJob, n)
		for i := range out {
			out[i] = batchJob{group: name, lo: i * 100, hi: i*100 + 99}
		}
		return out
	}
	cand := func(name, tier string, n int) candidate {
		return candidate{g: backfillRow{Name: name, Tier: tier}, batches: batches(name, n)}
	}

	// The production shape, through planPass — the function the pass actually
	// calls — so that deleting the tier step is caught rather than only a
	// direct test of the filter.
	jobs, used := planPass([]candidate{
		cand("alt.binaries.multimedia.anime.highspeed", "critical", 500),
		cand("alt.binaries.movies.repost", "low", 500),
		cand("alt.binaries.nzb", "low", 500),
	}, 25)
	if len(used) != 1 || used[0].g.Name != "alt.binaries.multimedia.anime.highspeed" {
		names := []string{}
		for _, c := range used {
			names = append(names, c.g.Name)
		}
		t.Errorf("pass touched %v, want only the critical group — spending on LOW history while a "+
			"CRITICAL group gates every LOW group makes that gate last longer", names)
	}
	if len(jobs) != 25 || used[0].taken != 25 {
		t.Errorf("the critical group got %d of 25 batches; with LOW filtered out it should take the whole pass", len(jobs))
	}

	got := highestTierOnly([]candidate{
		cand("alt.binaries.multimedia.anime.highspeed", "critical", 500),
		cand("alt.binaries.nzb", "low", 500),
	})
	if len(got) != 1 {
		t.Errorf("highestTierOnly kept %d candidates, want 1", len(got))
	}

	// Within a tier the share is still even: that was the original fix and it
	// must survive.
	got = highestTierOnly([]candidate{
		cand("a", "critical", 500), cand("b", "critical", 500), cand("c", "low", 500),
	})
	if len(got) != 2 {
		t.Errorf("got %d candidates, want both critical groups sharing", len(got))
	}
	_, taken := shareBudget([][]batchJob{got[0].batches, got[1].batches}, 25)
	for i, n := range taken {
		if n == 0 {
			t.Errorf("critical group %d got nothing; within-tier sharing regressed", i)
		}
	}

	// When the top tier finishes, the next one gets the budget rather than
	// everything stalling.
	got = highestTierOnly([]candidate{cand("x", "normal", 10), cand("y", "low", 10)})
	if len(got) != 1 || got[0].g.Name != "x" {
		t.Error("with no critical work left, the normal tier must take the pass")
	}
	got = highestTierOnly([]candidate{cand("y", "low", 10)})
	if len(got) != 1 {
		t.Error("a LOW-only candidate set must still be backfilled, not skipped")
	}

	// An unrecognised tier must not be treated as the lowest priority and
	// stranded behind everything else.
	got = highestTierOnly([]candidate{cand("weird", "typo", 10), cand("l", "low", 10)})
	if len(got) != 1 || got[0].g.Name != "weird" {
		t.Error("an unknown tier should rank as normal, ahead of low")
	}
	if len(highestTierOnly(nil)) != 0 {
		t.Error("empty input should stay empty")
	}
}

// The backfill pressure gate, which the catch-up loop now consults every round
// rather than once on entry.
//
// Hysteresis is the point: pause at the high-water mark, resume only below the
// LOW one. Without it a backend hovering at the threshold flaps between running
// and paused every round, which with a loop that re-checks continuously means
// thrashing rather than the orderly drain the gate exists to produce.
func TestBackfillPressureHysteresis(t *testing.T) {
	cfg := Config{BackfillPressureHighPct: 85, BackfillPressureLowPct: 70, BackfillPressureCeilingPct: 92}

	p := &Plugin{staging: fakePressure{pr: 0.77, ready: 500}}
	if yield, _ := p.backfillYields(context.Background(), cfg); yield {
		t.Error("yielded at 77% with the gate at 85% — this is what was throttling nothing at all")
	}

	// Over the high-water mark: stop.
	p = &Plugin{staging: fakePressure{pr: 0.86, ready: 500}}
	if yield, _ := p.backfillYields(context.Background(), cfg); !yield {
		t.Error("did not yield at 86% with the gate at 85%")
	}
	if !p.backfillPaused {
		t.Error("crossing the high-water mark did not latch the paused state")
	}

	// Latched: between low and high it must STAY paused, or it flaps.
	p.staging = fakePressure{pr: 0.80, ready: 500}
	if yield, _ := p.backfillYields(context.Background(), cfg); !yield {
		t.Error("resumed at 80% while latched; hysteresis requires falling below 70% first")
	}

	// Below the low-water mark: resume, and unlatch.
	p.staging = fakePressure{pr: 0.69, ready: 500}
	if yield, _ := p.backfillYields(context.Background(), cfg); yield {
		t.Error("still paused at 69% with the low-water mark at 70%")
	}
	if p.backfillPaused {
		t.Error("dropping below the low-water mark did not clear the latch")
	}

	// An unreadable backend must not silently stop the backfill: not knowing
	// the pressure is not a reason to abandon 659 million missing articles.
	p = &Plugin{staging: fakePressure{err: true, ready: 500}}
	if yield, _ := p.backfillYields(context.Background(), cfg); yield {
		t.Error("an unreadable pressure probe stopped the backfill")
	}
}

// fakePressure answers pressure() and readyDepth() and nothing else.
//
// The interface is embedded rather than implemented: this test is about the
// gate's arithmetic, and stubbing a dozen unrelated staging methods would bury
// that. Anything else the code under test touches nil-panics loudly, which is
// the correct outcome — and it happened: adding the ready-depth probe to the
// gate panicked this fake immediately, which is precisely the signal wanted.
type fakePressure struct {
	stagingStore
	pr    float64
	err   bool
	ready int64 // -1 = backend cannot answer cheaply (pg)
}

func (f fakePressure) readyDepth(context.Context) (int64, error) { return f.ready, nil }

func (f fakePressure) pressure(context.Context) (float64, error) {
	if f.err {
		return 0, errors.New("unreadable")
	}
	return f.pr, nil
}

// The catch-up decision, which has been wrong in production twice.
func TestCatchUpKeepsGoingWhileThereIsWork(t *testing.T) {
	noYield := func() (bool, float64) { return false, 0 }
	enabled := func() bool { return false }

	// THE BUG THAT SHIPPED. Most of this history is empty on the server, so a
	// round covers 150,000 article numbers and stages NOTHING — and that is
	// real progress, because the range is marked covered and never revisited.
	// Measuring progress by articles staged put the job straight back to sleep.
	rounds := 0
	res := runCatchUp(context.Background(), func() (int, int) {
		rounds++
		if rounds >= 5 {
			return 0, 0 // genuinely out of work
		}
		return 0, 25 // covered ground, staged nothing
	}, enabled, noYield, nil)
	if res.Rounds != 5 {
		t.Errorf("ran %d round(s), want 5 — a round that stages nothing but covers "+
			"25 batches is progress, not a reason to sleep", res.Rounds)
	}
	if res.StoppedBy != stopNoWork {
		t.Errorf("stopped by %q, want %q", res.StoppedBy, stopNoWork)
	}
	if res.Batches != 100 {
		t.Errorf("counted %d batches, want 100", res.Batches)
	}

	// No batches at all on the first round ends the pass immediately: caught up,
	// or every group is lease-held by a sibling.
	res = runCatchUp(context.Background(), func() (int, int) { return 0, 0 }, enabled, noYield, nil)
	if res.Rounds != 1 || res.StoppedBy != stopNoWork {
		t.Errorf("rounds=%d stoppedBy=%q, want 1/%s", res.Rounds, res.StoppedBy, stopNoWork)
	}

	// Pressure ends it, and reports the reading so the log can say why.
	calls := 0
	res = runCatchUp(context.Background(), func() (int, int) { return 100, 25 }, enabled,
		func() (bool, float64) {
			calls++
			return calls >= 3, 0.87
		}, nil)
	if res.StoppedBy != stopPressure {
		t.Errorf("stopped by %q, want %q", res.StoppedBy, stopPressure)
	}
	if res.Rounds != 3 {
		t.Errorf("ran %d round(s) before yielding, want 3", res.Rounds)
	}
	if res.Pressure != 0.87 {
		t.Errorf("pressure %.2f not carried out for the log", res.Pressure)
	}

	// The operator switch is honoured mid-pass, not only at entry.
	n := 0
	res = runCatchUp(context.Background(), func() (int, int) { return 1, 1 },
		func() bool { n++; return n >= 2 }, noYield, nil)
	if res.StoppedBy != stopDisabled || res.Rounds != 2 {
		t.Errorf("rounds=%d stoppedBy=%q, want 2/%s", res.Rounds, res.StoppedBy, stopDisabled)
	}

	// Cancellation wins over everything, so shutdown is not delayed by a pass
	// that still has work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res = runCatchUp(ctx, func() (int, int) { return 1, 25 }, enabled, noYield, nil)
	if res.StoppedBy != stopCancelled || res.Rounds != 1 {
		t.Errorf("rounds=%d stoppedBy=%q, want 1/%s", res.Rounds, res.StoppedBy, stopCancelled)
	}

	// The per-round hook sees cumulative totals, which is what the log reports.
	var lastB, lastS int
	runCatchUp(context.Background(), func() (int, int) { return 10, 2 }, func() bool { return true },
		noYield, func(_, b, s int) { lastB, lastS = b, s })
	if lastB != 2 || lastS != 10 {
		t.Errorf("hook saw batches=%d staged=%d, want 2/10", lastB, lastS)
	}
}

// The builder's catch-up loop measures ENTRIES DRAINED, not releases built.
//
// Most of this queue is junk that will never produce a release, but discarding
// it still frees the Redis the real sets need to complete — so a round that
// builds nothing and throws away 500 is progress. Incomplete sets stay staged
// and must NOT count, or the loop spins forever on a queue that is simply
// waiting for more articles to arrive.
func TestBuildCatchUpCountsDrainedNotBuilt(t *testing.T) {
	never := func() bool { return false }
	noYield := func() (bool, float64) { return false, 0 }

	// Builds nothing, drains 500 a round: real progress, keep going.
	rounds := 0
	res := runCatchUp(context.Background(), func() (int, int) {
		rounds++
		if rounds > 4 {
			return 0, 0
		}
		return 0, 500
	}, never, noYield, nil)
	if res.Rounds != 5 || res.Batches != 2000 {
		t.Errorf("rounds=%d drained=%d, want 5/2000 — a round that discards junk is progress",
			res.Rounds, res.Batches)
	}

	// A queue of nothing but incomplete sets drains nothing, so the pass must
	// end rather than spin: those sets need the backfill, not the builder.
	res = runCatchUp(context.Background(), func() (int, int) { return 0, 0 }, never, noYield, nil)
	if res.Rounds != 1 || res.StoppedBy != stopNoWork {
		t.Errorf("rounds=%d stoppedBy=%q, want 1/%s", res.Rounds, res.StoppedBy, stopNoWork)
	}

	// Building releases while draining is the normal case and both are tracked.
	res = runCatchUp(context.Background(), func() (int, int) {
		rounds++
		if rounds > 7 {
			return 0, 0
		}
		return 10, 500
	}, never, noYield, nil)
	if res.Staged == 0 || res.Batches == 0 {
		t.Errorf("built=%d drained=%d, want both counted", res.Staged, res.Batches)
	}
}

// The backfill's progress signal is batches COMPLETED, never planned. Planned
// work is a fixed point for an all-failing provider — a failed batch records
// no coverage, so the next round re-derives the identical gaps and plans the
// identical jobs, and a loop fed planned counts spins forever with the job
// stuck Running and the error ring flooding once per batch per round. Only
// r.ok batches (fetched AND staged, coverage recorded) shrink future rounds.
func TestCompletedBatchesCountsOnlyRecordedWork(t *testing.T) {
	all := []batchResult{
		{ok: true, staged: 100},
		{ok: true, staged: 0}, // empty history still records its range: progress
		{ok: false},           // fetch or stage failed: replanned next round
		{ok: false},
	}
	if got := completedBatches(all); got != 2 {
		t.Errorf("completedBatches = %d, want 2 — failed batches must not count", got)
	}
	if got := completedBatches([]batchResult{{ok: false}, {ok: false}}); got != 0 {
		t.Errorf("all-failing round = %d, want 0 — this is what ends the pass instead of spinning", got)
	}
	if got := completedBatches(nil); got != 0 {
		t.Errorf("no results = %d, want 0", got)
	}
}
