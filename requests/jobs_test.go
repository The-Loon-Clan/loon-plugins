package requests

import (
	"context"
	"errors"
	"testing"
)

// sweepRecorder captures what the job asked of the host.
type sweepRecorder struct {
	calls  []int // the window each call used
	moved  int
	err    error
	window int
	winSet bool
	ops    []string
}

func (r *sweepRecorder) deps() JobDeps {
	d := JobDeps{
		BacklogSweep: func(ctx context.Context, days int) (int, error) {
			r.calls = append(r.calls, days)
			return r.moved, r.err
		},
		ReportError: func(ctx context.Context, op string, err error) {
			r.ops = append(r.ops, op)
		},
	}
	if r.winSet {
		d.BacklogWindowDays = func(context.Context) int { return r.window }
	}
	return d
}

// Zero is the off switch, and it has to be the OFF switch rather than a
// validation error: an operator who decides the backlog was a bad idea should
// be able to stop it from the settings page without also disabling a job. A
// sweep run with 0 would fall through to the repository's own 30-day default
// and shelve everything the operator just said not to touch.
func TestSweepWindowZeroSweepsNothing(t *testing.T) {
	r := &sweepRecorder{window: 0, winSet: true, moved: 500}
	newBacklogSweeper(r.deps()).runOnce(context.Background())

	if len(r.calls) != 0 {
		t.Errorf("swept with window %v — 0 must mean disabled, not 'use the default'", r.calls)
	}
}

// The window is read every tick so a change on /admin/settings takes effect on
// the next run rather than the next deploy.
func TestSweepReadsTheWindowEveryRun(t *testing.T) {
	r := &sweepRecorder{window: 45, winSet: true}
	s := newBacklogSweeper(r.deps())

	s.runOnce(context.Background())
	r.window = 90
	s.runOnce(context.Background())

	if len(r.calls) != 2 || r.calls[0] != 45 || r.calls[1] != 90 {
		t.Errorf("windows used = %v, want [45 90] — the setting was captured, not read", r.calls)
	}
}

// A host that wires no setting still gets a working sweep at the default.
func TestSweepFallsBackToTheDefaultWindow(t *testing.T) {
	r := &sweepRecorder{} // winSet false: no BacklogWindowDays wired
	newBacklogSweeper(r.deps()).runOnce(context.Background())

	if len(r.calls) != 1 || r.calls[0] != defaultBacklogDays {
		t.Errorf("windows used = %v, want [%d]", r.calls, defaultBacklogDays)
	}
}

// A failing sweep must be reported. It writes to member-visible state and runs
// unattended once a day, so a silent failure is a backlog that quietly stops
// filling and a queue that quietly goes back to being unreadable.
func TestSweepReportsItsFailures(t *testing.T) {
	r := &sweepRecorder{window: 30, winSet: true, err: errors.New("db down")}
	newBacklogSweeper(r.deps()).runOnce(context.Background())

	if len(r.ops) != 1 || r.ops[0] != "requests/backlog-sweep" {
		t.Errorf("reported %v, want one requests/backlog-sweep", r.ops)
	}
}

// JobDeps is optional: a host that never calls SetJobDeps gets no sweep rather
// than a nil dereference at boot.
func TestJobDepsOkRefusesAPartialWiring(t *testing.T) {
	var nilDeps *JobDeps
	if nilDeps.ok() {
		t.Error("nil JobDeps reported itself wired")
	}
	if (&JobDeps{ReportError: func(context.Context, string, error) {}}).ok() {
		t.Error("JobDeps with no BacklogSweep reported itself wired")
	}
	full := JobDeps{
		BacklogSweep: func(context.Context, int) (int, error) { return 0, nil },
		ReportError:  func(context.Context, string, error) {},
	}
	if !full.ok() {
		t.Error("a complete JobDeps was refused")
	}
}
