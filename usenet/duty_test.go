package usenet

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

// The exact shape Start hands the scheduler, driven through the REAL adapter.
// loon's RunLoop type-asserts the handle its RegisterJob minted and panics on
// anything else; the duty wrapper shipped without Unwrap and crash-looped the
// production worker sub-second at boot, every ~60s, on the first RunLoop call
// — invisible to a suite that never wired Provision's wrapping to the real
// scheduler. This test IS that wiring.
func TestDutyWrappedJobsAreRunLoopCompatible(t *testing.T) {
	sched := schedule.CoreScheduler(schedule.NewRegistry())
	d := newDutyTracker()
	job := d.wrap(jobNameCrawl, sched.RegisterJob(jobNameCrawl, "test").MarkOffPeak())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // registration is the test; the loop should exit immediately

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunLoop panicked on the duty-wrapped handle: %v — this exact shape "+
				"crash-looped the production worker at boot", r)
		}
	}()
	sched.RunLoop(ctx, job, time.Hour, time.Hour, func(context.Context) {})
}

// The duty percentage is the metric three idle-pipeline incidents (5.9%,
// 15.7%, and the 92%-idle deadlock) were diagnosed by measuring BY HAND. It
// must count completed windows, clip to the span, and count an in-progress
// run — a job stuck "running" reads busy, not aged-out.
func TestDutyTrackerMath(t *testing.T) {
	d := newDutyTracker()
	now := time.Now()

	if pct := d.dutyPct("job", time.Hour, now); pct != 0 {
		t.Errorf("no windows: duty = %.1f, want 0", pct)
	}

	// One completed 6-minute window inside the hour = 10%.
	d.mu.Lock()
	d.windows["job"] = []dutyWindow{{start: now.Add(-30 * time.Minute), end: now.Add(-24 * time.Minute)}}
	d.mu.Unlock()
	if pct := d.dutyPct("job", time.Hour, now); pct < 9.9 || pct > 10.1 {
		t.Errorf("6 busy minutes of 60 = %.1f%%, want 10%%", pct)
	}

	// A window straddling the span boundary only counts its inside part.
	d.mu.Lock()
	d.windows["job"] = []dutyWindow{{start: now.Add(-90 * time.Minute), end: now.Add(-54 * time.Minute)}}
	d.mu.Unlock()
	if pct := d.dutyPct("job", time.Hour, now); pct < 9.9 || pct > 10.1 {
		t.Errorf("clipped window = %.1f%%, want 10%% (only the 6 minutes inside the hour)", pct)
	}

	// An in-progress run counts up to now: a wedged job must read busy.
	d.mu.Lock()
	d.windows["job"] = nil
	d.running["job"] = now.Add(-2 * time.Hour)
	d.mu.Unlock()
	if pct := d.dutyPct("job", time.Hour, now); pct < 99.9 {
		t.Errorf("run started 2h ago and never ended = %.1f%%, want 100%%", pct)
	}
}

func TestDutyTrackerLifecycle(t *testing.T) {
	d := newDutyTracker()
	// end without begin (the early-return SetIdle paths) is a no-op.
	d.end("job")
	if n := len(d.windows["job"]); n != 0 {
		t.Fatalf("end without begin recorded %d windows", n)
	}
	d.begin("job")
	d.end("job")
	if n := len(d.windows["job"]); n != 1 {
		t.Fatalf("begin+end recorded %d windows, want 1", n)
	}
	if _, still := d.running["job"]; still {
		t.Error("end left the job marked running")
	}
}

// The pass-cumulative counters are what kill the "0 batch(es)" mystery: every
// catch-up pass ENDS on a round that found no work (that is the loop's exit
// condition), so a readout built from the round-scoped pair always snapshots
// the zeroed terminal round. The cumulative pair must survive roundStart and
// reach `last` intact.
func TestPassBatchesSurviveTheTerminalRound(t *testing.T) {
	var tr passTracker
	tr.passStart(1)

	// Two productive rounds.
	for round := 0; round < 2; round++ {
		tr.roundStart()
		tr.notePlanned("g", 25)
		for i := 0; i < 25; i++ {
			tr.noteBatchFor("g", 100, 50, 1000, true)
		}
	}
	// The terminal round: plans nothing, runs nothing — how every pass ends.
	tr.roundStart()
	tr.passEnd()

	_, last := tr.snapshot()
	if last.Batches != 0 {
		t.Errorf("round-scoped Batches = %d after the terminal round, want 0 — the live-bar "+
			"semantics must not change", last.Batches)
	}
	if last.PassBatches != 50 || last.PassBatchesTotal != 50 {
		t.Errorf("pass-cumulative = %d/%d, want 50/50 — this is the '0 batch(es) while "+
			"millions of articles moved' bug", last.PassBatches, last.PassBatchesTotal)
	}
}

// A lapsed heartbeat means the worker is gone: its in-progress claims are
// history, and rendering them live showed a dead worker as a crawl whose
// duration climbed forever.
func TestMarkStaleIfLapsed(t *testing.T) {
	now := time.Now()
	tv := workerTelemetry{
		UpdatedAt:   now.Add(-2 * telemetryStaleAfter),
		CrawlCur:    passStats{InProgress: true},
		BackfillCur: passStats{InProgress: true},
		Jobs:        []crawlerJobVM{{Name: "Usenet Crawler", Running: true}},
	}
	tv.markStaleIfLapsed(now)
	if !tv.Stale {
		t.Fatal("a snapshot past the staleness bound was not marked stale")
	}
	if tv.CrawlCur.InProgress || tv.BackfillCur.InProgress || tv.Jobs[0].Running {
		t.Error("stale snapshot still claims work in progress — the dead-worker-as-busy bug")
	}

	fresh := workerTelemetry{UpdatedAt: now.Add(-time.Second), CrawlCur: passStats{InProgress: true}}
	fresh.markStaleIfLapsed(now)
	if fresh.Stale || !fresh.CrawlCur.InProgress {
		t.Error("a fresh snapshot was downgraded")
	}

	// Zero UpdatedAt = nothing ever published; that is "no telemetry", not
	// "stale telemetry", and the zero-value rendering already handles it.
	var empty workerTelemetry
	empty.markStaleIfLapsed(now)
	if empty.Stale {
		t.Error("an empty snapshot was marked stale")
	}
}
