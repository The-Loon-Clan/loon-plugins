package usenet

import (
	"fmt"
	"runtime/debug"

	"github.com/the-loon-clan/loon/core"
)

// Panic containment for the pipeline passes.
//
// WHY IT LIVES AT THE PASS ENTRY POINT, and not at the call sites.
//
// An unrecovered panic in ANY goroutine ends the whole process, and this
// plugin dispatches its passes from bare `go p.runX(ctx)` in several places.
// loon's SetTriggerAsync now protects the one route that goes through
// /admin/jobs — but that is one of at least five, and fixing only it produced
// the worse shape it was meant to remove: the SAME job, panic-protected when
// run from the host's job page and process-fatal when run from this plugin's
// own Jobs tab, which is the button an operator is far more likely to press
// for these six.
//
// writegate.go already learned this lesson for the read-only gate, in almost
// these words: "Guarding those call sites means finding all four and
// remembering the fifth. The pass entry point is the one place every route
// converges, so the check goes there." The routes are the same routes:
//
//   - SetTriggerAsync -> TriggerJob        (the host's /admin/jobs button)
//   - fireTrigger                          (this plugin's own Jobs tab)
//   - fireTriggerRequest                   (the same button, relayed to the
//     worker in a split deployment)
//   - chained passes: a crawl or backfill ends with `go p.runBuild(ctx)`
//   - the idle health check, spawned from the crawl loop
//   - p.svc.triggerCrawl / triggerBackfill (the pluginapi surface)
//
// So the recover goes where the write gate went. A new caller cannot route
// around it, and a new pass cannot forget it while TestEveryPassContainsPanics
// is watching.
//
// It does NOT swallow. A contained panic is logged on the job, so it shows in
// /admin/jobs and the per-job log ring, and reported to the host's error sink
// so it reaches error_logs — the same two places a scheduled tick's panic
// lands. The alternative to containing it is not "the bug gets noticed"; it is
// the whole site going down while the bug gets noticed.

// recoverPass contains a panic inside one pipeline pass.
//
// Used as the first statement of every spawned pass:
//
//	defer p.recoverPass(jobNameCrawl, p.crawlJob)
//
// job may be nil (a pass that runs before registration, or in a test); the
// report still reaches the error sink.
func (p *Plugin) recoverPass(name string, job core.Job) {
	r := recover()
	if r == nil {
		return
	}
	err := fmt.Errorf("panic in %s: %v", name, r)

	// On the job first, because that is where an operator who just pressed the
	// button is looking.
	if job != nil {
		job.Log("PANIC: %v", r)
		job.SetError(err.Error())
	}
	// Then durably. The stack is the whole value of the report; without it
	// "invalid memory address" names no line in any file.
	if p.core != nil && p.core.Errors != nil {
		p.core.Errors.Report(p.ctx, "usenet/"+name,
			fmt.Errorf("%w\n%s", err, debug.Stack()))
	}
}
