package usenet

import (
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Busy/idle accounting per job — the number three production incidents were
// diagnosed by measuring BY HAND: the backfill at a 5.9% duty cycle (no
// catch-up loop), 15.7% (the staged==0 regression), and the pressure-gate
// deadlock, found only because the operator asked why nothing was working and
// a shell loop sampling job status showed every job idle 92% of the time.
// Each time the pass cards looked healthy — they describe what a pass DID, not
// how much of the wall clock the job spent doing anything.
//
// dutyJob wraps the core.Job handle every job already drives, so the busy
// windows record themselves at the SetRunning → SetIdle/SetError boundary
// with no per-job wiring, and telemetry publishes a trailing duty percentage
// per job. A scheduling or gating bug now shows up as a number on the Jobs
// tab instead of a diagnostic session.

// dutyWindowKeep bounds the per-job ring. Sized for the fastest realistic
// cycle (the builder kicked after every ~2s backfill round) to still span the
// whole trailing hour the percentage is computed over.
const dutyWindowKeep = 2048

// dutySpan is the trailing window the published percentage covers.
const dutySpan = time.Hour

type dutyWindow struct {
	start, end time.Time
}

type dutyTracker struct {
	mu      sync.Mutex
	windows map[string][]dutyWindow // completed busy windows, oldest first
	running map[string]time.Time    // in-progress window starts
}

func newDutyTracker() *dutyTracker {
	return &dutyTracker{
		windows: map[string][]dutyWindow{},
		running: map[string]time.Time{},
	}
}

func (d *dutyTracker) begin(job string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running[job] = time.Now()
}

func (d *dutyTracker) end(job string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	start, ok := d.running[job]
	if !ok {
		return // SetIdle without SetRunning (early-return paths) is a no-op
	}
	delete(d.running, job)
	w := append(d.windows[job], dutyWindow{start: start, end: time.Now()})
	if len(w) > dutyWindowKeep {
		w = w[len(w)-dutyWindowKeep:]
	}
	d.windows[job] = w
}

// dutyPct is the fraction of the trailing span the job spent busy, 0–100.
// An in-progress run counts up to now, so a job stuck "running" reads 100
// rather than dropping off as its windows age out.
func (d *dutyTracker) dutyPct(job string, span time.Duration, now time.Time) float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	from := now.Add(-span)
	var busy time.Duration
	for _, w := range d.windows[job] {
		if w.end.Before(from) {
			continue
		}
		s := w.start
		if s.Before(from) {
			s = from
		}
		busy += w.end.Sub(s)
	}
	if s, ok := d.running[job]; ok {
		if s.Before(from) {
			s = from
		}
		if now.After(s) {
			busy += now.Sub(s)
		}
	}
	if busy > span {
		busy = span
	}
	return 100 * float64(busy) / float64(span)
}

// all reports the trailing duty percentage for every job that has ever run.
func (d *dutyTracker) all(span time.Duration, now time.Time) map[string]float64 {
	d.mu.Lock()
	names := make(map[string]struct{}, len(d.windows)+len(d.running))
	for n := range d.windows {
		names[n] = struct{}{}
	}
	for n := range d.running {
		names[n] = struct{}{}
	}
	d.mu.Unlock()
	out := make(map[string]float64, len(names))
	for n := range names {
		out[n] = d.dutyPct(n, span, now)
	}
	return out
}

// dutyJob decorates a core.Job so lifecycle transitions record busy windows.
// Everything else forwards to the wrapped handle.
type dutyJob struct {
	core.Job
	name string
	d    *dutyTracker
}

func (d *dutyTracker) wrap(name string, j core.Job) core.Job {
	return dutyJob{Job: j, name: name, d: d}
}

func (j dutyJob) SetRunning() {
	j.d.begin(j.name)
	j.Job.SetRunning()
}

func (j dutyJob) SetIdle(next time.Time) {
	j.d.end(j.name)
	j.Job.SetIdle(next)
}

func (j dutyJob) SetError(msg string) {
	j.d.end(j.name)
	j.Job.SetError(msg)
}

// MarkOffPeak must return the WRAPPER, or the chained registration idiom
// (RegisterJob(...).MarkOffPeak()) would silently unwrap the duty accounting.
func (j dutyJob) MarkOffPeak() core.Job {
	j.Job.MarkOffPeak()
	return j
}

// Unwrap exposes the scheduler-minted handle underneath. RunLoop needs the
// concrete job its RegisterJob returned and walks Unwrap to find it — without
// this the first RunLoop call panics the worker at boot, which is exactly how
// this wrapper shipped its first production incident.
func (j dutyJob) Unwrap() core.Job { return j.Job }
