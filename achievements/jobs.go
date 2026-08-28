package achievements

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The scoring job — the metric half of progress, plus the payment repair
// sweep. In rewards this ran as one section of the maintenance tick; here it
// is a job of its own, on core.Scheduler, held to the checklist's job rules
// (ranks/promote.go is the worked example this follows).

// scoringInterval is the sweep cadence AND the SetIdle horizon, one
// declaration so the admin page's "next run" cannot drift from what the loop
// actually waits — the same rule ranks' promotionInterval states.
//
// Hourly. A counter-scored badge is not a live figure: the event path
// completes the common case within seconds, and this pass exists to reconcile
// drift, score what nothing emits (tenure), and repair unpaid completions —
// none of which is less owed for arriving twenty minutes late.
const scoringInterval = time.Hour

// repayBatch bounds one repair sweep. Unpaid completions are rare (a crash
// window, a granter outage) and the next tick picks up the remainder; an
// unbounded pass over a growing table is how a maintenance job becomes an
// outage.
const repayBatch = 500

// registerScoringJob captures the job handle. Called during Provision — the
// core.Scheduler contract warns that registering at Start races the admin
// view's registry snapshot.
func (p *Plugin) registerScoringJob(sched core.SchedulerService) {
	p.job = sched.RegisterJob("Achievement Scoring",
		"Scores metric achievements from host counters, backfills new ones, and repairs completions whose reward payment has not landed").
		MarkWrites()
	p.job.SetTriggerAsync(func() { p.runScoring(context.Background()) })
}

func (p *Plugin) runScoring(ctx context.Context) {
	// The manual trigger does not go through the loop, so pause is checked
	// here; RunLoop already skips a paused job on the scheduled path.
	if p.job.IsPaused() {
		return
	}
	p.job.SetRunning()
	// NOTE: every path below this line must reach SetIdle or SetError. A run
	// that returns after SetRunning and does neither leaves the job displayed
	// as "running" forever — and the scheduler will not re-trigger one it
	// believes is still going, so the job silently never runs again. The
	// promotion sweep shipped exactly that bug: the first sweep worked, and
	// every manual trigger after it returned 200 and did nothing.

	// Metric scoring. Per-metric failures are logged on the job and the next
	// metric still runs — one broken counter must not stop tenure badges
	// because upload badges are unhappy.
	for metric, src := range p.metrics {
		n, err := p.scoreMetric(ctx, metric, src)
		if err != nil {
			p.job.Log("metric %q: %v", metric, err)
			continue
		}
		if n > 0 {
			p.job.Log("%s: completed %d achievement(s)", metric, n)
		}
	}

	// The payment repair sweep — the other half of the idempotence design.
	// A completion whose GrantOneOff never ran (a crash between the commit
	// and the call) or never succeeded (granter down) sits with paid_at
	// NULL; the granter is idempotent on (reward, user, reference), so
	// calling it again pays exactly once however many times this runs.
	repaired, err := p.repayUnpaid(ctx)
	if err != nil {
		p.job.SetError(fmt.Sprintf("payment repair: %v", err))
		return
	}
	if repaired > 0 {
		p.job.Log("Repaid %d completion(s) whose reward had not landed", repaired)
	}
	p.job.SetIdle(time.Now().Add(scoringInterval))
}

// repayUnpaid pays every completed-but-unpaid row it can, and says so when it
// cannot.
func (p *Plugin) repayUnpaid(ctx context.Context) (int, error) {
	rows, err := p.store.UnpaidCompletions(ctx, repayBatch)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if p.granter == nil {
		// Said on the job, once per run rather than once per row: an
		// operator who has configured paid achievements and sees them stuck
		// pending needs to know the payer never arrived.
		p.jobLog("%d unpaid completion(s), and no rewards granter is registered — "+
			"the rewards plugin supplies %q, and without it these cannot be paid",
			len(rows), "rewards.granter.byslug")
		return 0, nil
	}
	repaired := 0
	for _, r := range rows {
		if _, err := p.granter.GrantOneOff(ctx, r.UserID, r.RewardSlug, r.Slug); err != nil {
			// A misconfigured reward (deleted, disabled, payout-less) lands
			// here every tick. That is the lazy payability report this
			// design accepted in exchange for not reading rewards' tables.
			p.jobLog("repay %q for user %d via reward %q: %v", r.Slug, r.UserID, r.RewardSlug, err)
			continue
		}
		if err := p.store.MarkPaid(ctx, r.AchievementID, r.UserID); err != nil {
			p.jobLog("stamp paid_at on %q for user %d: %v", r.Slug, r.UserID, err)
			continue
		}
		repaired++
	}
	return repaired, nil
}

// jobLog writes to the job's admin-visible log when the job exists (the
// worker leg), and to the process log otherwise — repayUnpaid is also
// exercised directly by tests, where no job is registered.
func (p *Plugin) jobLog(format string, args ...any) {
	if p.job != nil {
		p.job.Log(format, args...)
		return
	}
	log.Printf("achievements: "+format, args...)
}
