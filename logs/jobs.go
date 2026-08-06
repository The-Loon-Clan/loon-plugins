package logs

// The Error Log Cleanup job. Runs ONLY in worker-capable processes — Plugin
// .Start gates on the process kind captured at Provision, so a split-mode web
// process registers the /admin/logs routes but never races the worker's prune
// loop.

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

// ErrorLogRetention is the default age beyond which error-log rows are
// pruned. Long enough to spot a slow-burn regression in /admin/logs,
// short enough that the table doesn't dominate disk on a long-running
// deploy.
const ErrorLogRetention = 30 * 24 * time.Hour

const cleanupIntervalMin = 24 * 60 // daily

// cleanupJob is registered at package init so the job appears in the
// /admin/jobs registry with its historical name even before Provision
// wires the worker deps — a manual trigger just no-ops until then.
var cleanupJob = schedule.RegisterJob("Error Log Cleanup",
	"Deletes error_log rows older than 30 days. Lightweight — runs daily.")

// cleaner prunes stale error-log rows on a daily tick, threaded through
// ServiceLoop so it inherits off-peak gating + SIGTERM-aware sleep.
type cleaner struct {
	deps JobDeps
	job  *schedule.JobInfo
}

func newCleaner(deps JobDeps) *cleaner {
	c := &cleaner{deps: deps, job: cleanupJob}
	cleanupJob.IntervalMin = cleanupIntervalMin
	// The trigger context is deliberately Background, not the boot context:
	// a manual /admin/jobs run must not be cancelled by whatever request or
	// caller happened to fire it.
	cleanupJob.SetTrigger(func() { go c.runOnce(context.Background()) })
	return c
}

// start fires daily after a 30-min boot delay so the time-critical
// jobs (junk cleanup, vacuum, imports) get their startup window first.
// ctx comes from Start, so SIGTERM unblocks the loop.
func (c *cleaner) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, c.job,
		30*time.Minute,
		time.Duration(cleanupIntervalMin)*time.Minute,
		c.runOnce)
}

func (c *cleaner) runOnce(ctx context.Context) {
	c.job.SetRunning()
	deleted, err := c.deps.Prune(ctx, ErrorLogRetention)
	if err != nil {
		c.job.SetError(err.Error())
		c.deps.ReportError(ctx, "logs/cleanup", err)
		return
	}
	if deleted > 0 {
		c.job.Log("Pruned %d error_log row(s) older than %s", deleted, ErrorLogRetention)
	}
	c.job.SetIdle(time.Now().Add(time.Duration(cleanupIntervalMin) * time.Minute))
}
