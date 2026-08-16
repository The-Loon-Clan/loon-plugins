package requests

// The board's one background job. It runs ONLY in worker-capable processes:
// Plugin.Start gates on the process kind captured at Provision, so a web
// process registers the routes and never races the loop.

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

var backlogSweepJob = schedule.RegisterJob(
	"Request Backlog Sweep",
	"Moves open requests that stopped moving into the backlog, recording why each one stalled. Members pull them back from /community/requests?tab=backlog.",
).MarkWrites()

// backlogSweeper shelves requests that have stopped moving.
//
// This exists as a JOB rather than as something an operator runs by hand
// because the problem it solves is continuous. The queue did not fill up once;
// it fills up every day, and a cleanup that depends on somebody remembering is
// a cleanup that happens when the queue is already unreadable — which is how
// 7,151 of 13,912 open requests came to be over thirty days old, the oldest
// sitting since April.
//
// Daily is the right cadence for the same reason it is not hourly: the input
// changes by a handful of rows a day, so a tighter loop would be a write
// transaction and a log line to shelve nothing.
//
// Cheap: one indexed UPDATE per tick, and after the first run it touches only
// what crossed the line that day.
type backlogSweeper struct {
	deps JobDeps
	job  *schedule.JobInfo
}

func newBacklogSweeper(deps JobDeps) *backlogSweeper {
	s := &backlogSweeper{deps: deps, job: backlogSweepJob}
	// Background, not the boot context: a manual trigger from /admin/jobs must
	// not be cancelled by whatever fired it.
	backlogSweepJob.SetTrigger(func() { go s.runOnce(context.Background()) })
	return s
}

// start runs the sweep daily after a 10-minute boot delay, so it does not
// compete with the worker's warm-up scans. ctx comes from Start, so SIGTERM
// unblocks the loop.
func (s *backlogSweeper) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, s.job, 10*time.Minute, 24*time.Hour, s.runOnce)
}

func (s *backlogSweeper) runOnce(ctx context.Context) {
	s.job.SetRunning()
	days := defaultBacklogDays
	if s.deps.BacklogWindowDays != nil {
		days = s.deps.BacklogWindowDays(ctx)
	}
	// Zero is the off switch, and it is deliberately not a validation error:
	// an operator who decides the backlog was a bad idea should be able to
	// stop it from the settings page without also having to disable a job.
	if days <= 0 {
		s.job.Log("window is 0 — sweeping disabled")
		s.job.SetIdle(time.Now().Add(24 * time.Hour))
		return
	}
	n, err := s.deps.BacklogSweep(ctx, days)
	if err != nil {
		s.job.SetError(err.Error())
		s.deps.ReportError(ctx, "requests/backlog-sweep", err)
		return
	}
	if n > 0 {
		// Logged even though the count is also in the backlog tab, because the
		// tab shows a total and this shows a RATE — a day that shelves four
		// hundred requests is a different event from one that shelves four,
		// and only the job log can tell them apart afterwards.
		s.job.Log("Shelved %d request(s) with no movement in %d days", n, days)
	}
	s.job.SetIdle(time.Now().Add(24 * time.Hour))
}

// defaultBacklogDays is the fallback when the host wires no setting. Thirty
// days is long enough that a request an agent is genuinely working through its
// queue toward will not be shelved, and short enough that the open queue stops
// being a list nobody can read.
const defaultBacklogDays = 30
