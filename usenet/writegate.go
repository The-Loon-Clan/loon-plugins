package usenet

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// The read-only write gate, asked at the START OF EVERY PASS.
//
// WHY HERE AND NOT AT THE DISPATCH SITES. All six pipeline jobs carry
// MarkWrites(), and for a long time the comment above their registration claimed
// that made them "hold back while the site is read-only". It did not.
// schedule.WriteGate is consulted in exactly two places -- ServiceLoop
// (loop.go:83) and TriggerJob (registry.go:595) -- and this plugin uses NEITHER for
// automatic dispatch. Measured on production 2026-08-11: with read-only engaged and
// all 28 HTTP write paths correctly refusing with 503, the nzbs table still grew by
// 4 rows in 45 seconds, because this pipeline kept running.
//
// There are at least four ways a pass starts here, which is the actual lesson:
//   - SetTrigger -> TriggerJob            (gated, the only one that was)
//   - fireTrigger, the cross-process relay for the admin Jobs tab buttons
//   - chained calls: the crawl and backfill passes end with `go p.runBuild(ctx)`
//   - the idle health check, spawned from the crawl loop
//
// Guarding those call sites means finding all four and remembering the fifth. The
// pass entry point is the one place every route converges, so the check goes there:
// a new caller cannot route around it, and a new job cannot forget it while
// TestEveryPassAsksTheWriteGate is watching.
//
// FAIL OPEN, deliberately, matching core.SiteWritable and schedule.WriteGate. A
// host that has not wired SiteState gets a working crawler rather than one that
// silently indexes nothing -- an empty index is a far more expensive failure than a
// few rows written during a window, and it is the demo site's default. The backstop
// for a mis-wired host is not this function: it is `pg18_migrate.sh quiesce`, which
// samples pg_stat_user_tables and refuses to dump if anything at all is moving.
// Evidence from the database, not from a flag that has already lied once.
func (p *Plugin) mayWrite(ctx context.Context, job core.Job) bool {
	if core.SiteWritable(ctx, p.core) {
		return true
	}
	// Logged on the job so it surfaces in /admin/jobs and the per-job log ring,
	// which is where an operator looks to answer "why has nothing been indexed
	// for four hours" -- the question a silent skip guarantees.
	if job != nil {
		job.Log("Skipped: site is read-only (write gate)")
	}
	return false
}
