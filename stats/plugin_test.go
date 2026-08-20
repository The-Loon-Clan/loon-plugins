package stats

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ── the fakes ───────────────────────────────────────────────────────

// fakeJob models the ONE thing about the host's registry that matters here:
// a run is in flight from SetRunning until something ends it, and both SetIdle
// and SetError end it (the registry sets Status to "idle" and "error"
// respectively). The plugin comments all say "must SetIdle", which is the
// common case; the invariant is the broader one and this models that.
type fakeJob struct {
	mu      sync.Mutex
	running bool
	started int
	idled   int
	errs    []string
	logs    []string
}

func (j *fakeJob) SetRunning() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.started = true, j.started+1
}

func (j *fakeJob) SetIdle(time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.idled = false, j.idled+1
}

func (j *fakeJob) SetError(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.errs = append(j.errs, msg)
}

func (j *fakeJob) Log(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.logs = append(j.logs, format)
}

func (j *fakeJob) MarkOffPeak() core.Job { return j }
func (j *fakeJob) MarkWrites() core.Job  { return j }
func (j *fakeJob) SetTrigger(fn func())  {}
func (j *fakeJob) IsPaused() bool        { return false }

// stillRunning is the failure this whole file exists to catch: a job left in
// flight never runs again on the loop and holds the shutdown drain open.
func (j *fakeJob) stillRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

type fakeReporter struct {
	mu  sync.Mutex
	ops []string
}

func (r *fakeReporter) Report(_ context.Context, op string, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *fakeReporter) HandlerError(*gin.Context, string, error) {}

func (r *fakeReporter) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

type fakeContributor struct {
	name  string
	stats []pluginapi.Stat
	err   error

	// atomic because the concurrency test calls run() from several goroutines;
	// a racy counter in the fake would report as a race in the plugin.
	callCount atomic.Int64
}

func (f *fakeContributor) StatsName() string { return f.name }

func (f *fakeContributor) Stats(context.Context) ([]pluginapi.Stat, error) {
	f.callCount.Add(1)
	return f.stats, f.err
}

func (f *fakeContributor) calls() int64 { return f.callCount.Load() }

// harness wires a plugin the way Provision does, minus the host.
func harness(t *testing.T, cache func(context.Context, []pluginapi.Stat) error, cs ...*fakeContributor) (*Plugin, *fakeJob, *fakeReporter) {
	t.Helper()
	saved := deps
	t.Cleanup(func() { deps = saved })
	SetDeps(Deps{Cache: cache})

	rep := &fakeReporter{}
	c := &core.Core{Errors: rep}
	for _, fc := range cs {
		if err := pluginapi.RegisterStats(c, fc); err != nil {
			t.Fatal(err)
		}
	}
	job := &fakeJob{}
	return &Plugin{core: c, job: job}, job, rep
}

func okCache(context.Context, []pluginapi.Stat) error { return nil }

// ── the invariant ───────────────────────────────────────────────────

// TestRunNeverLeavesTheJobInFlight walks every path out of run(). The comment
// above the ctx check says what happens if one of them misses: "the job reads
// as running forever and stalls the host's shutdown drain". Nothing enforced it.
func TestRunNeverLeavesTheJobInFlight(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		cache func(context.Context, []pluginapi.Stat) error
		cs    []*fakeContributor
	}{
		{"nothing contributes", context.Background(), okCache, nil},
		{"the ordinary path", context.Background(), okCache,
			[]*fakeContributor{{name: "store", stats: []pluginapi.Stat{{Key: "store.x", Value: 1}}}}},
		{"a contributor fails", context.Background(), okCache,
			[]*fakeContributor{{name: "store", err: errors.New("down")}}},
		{"the context is cancelled", cancelled, okCache,
			[]*fakeContributor{{name: "store"}}},
		{"the cache write fails", context.Background(),
			func(context.Context, []pluginapi.Stat) error { return errors.New("disk full") },
			[]*fakeContributor{{name: "store"}}},
	} {
		p, job, _ := harness(t, tc.cache, tc.cs...)
		p.run(tc.ctx)
		if job.stillRunning() {
			t.Errorf("%s: the job is still marked running — it will never run again and shutdown will wait on it", tc.name)
		}
		if job.started != 1 {
			t.Errorf("%s: SetRunning called %d times, want 1", tc.name, job.started)
		}
	}
}

// TestRunWithNoContextDoesNotStartAtAll. The nil-ctx guard sits BEFORE
// SetRunning, which is the only order that works: marking a run started and
// then returning is exactly the state the guard exists to avoid.
func TestRunWithNoContextDoesNotStartAtAll(t *testing.T) {
	p, job, _ := harness(t, okCache)
	p.run(nil)
	if job.started != 0 || job.stillRunning() {
		t.Errorf("started=%d running=%v — the guard must sit before SetRunning", job.started, job.stillRunning())
	}
}

// ── collection ──────────────────────────────────────────────────────

// TestOneFailingContributorDoesNotCostTheRest is the reason the loop reports
// and continues rather than returning. A site with twelve contributors should
// not lose its whole stats page because one plugin's table is locked.
func TestOneFailingContributorDoesNotCostTheRest(t *testing.T) {
	broken := &fakeContributor{name: "aaa-broken", err: errors.New("table is locked")}
	fine := &fakeContributor{name: "zzz-fine", stats: []pluginapi.Stat{{Key: "z.count", Value: 7}}}

	var cached []pluginapi.Stat
	p, job, rep := harness(t, func(_ context.Context, s []pluginapi.Stat) error {
		cached = s
		return nil
	}, broken, fine)
	p.run(context.Background())

	if fine.calls() != 1 {
		t.Error("the working contributor was skipped because an earlier one failed")
	}
	if len(cached) != 1 || cached[0].Key != "z.count" {
		t.Errorf("cached %v, want just the working contributor's stat", cached)
	}
	if got := rep.reported(); len(got) != 1 || got[0] != "stats/collect" {
		t.Errorf("reported %v, want one stats/collect — a failure must not be silent", got)
	}
	if job.stillRunning() {
		t.Error("the job was left running")
	}
}

// TestCancellationStopsBeforeTheNextContributor. The check is at the top of the
// loop body, so a cancelled context must not start work it cannot finish.
func TestCancellationStopsBeforeTheNextContributor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &fakeContributor{name: "store"}
	p, _, _ := harness(t, okCache, c)
	p.run(ctx)
	if c.calls() != 0 {
		t.Errorf("called a contributor %d times under a cancelled context", c.calls())
	}
}

// TestSnapshotIsPublishedBeforeTheCacheIsWritten. The in-memory copy is what
// the views read, and it is set before deps.Cache — so a cache backend that is
// down costs the persisted copy, not the page.
func TestSnapshotSurvivesACacheFailure(t *testing.T) {
	fc := &fakeContributor{name: "store", stats: []pluginapi.Stat{{Key: "store.x", Value: 3}}}
	p, job, rep := harness(t, func(context.Context, []pluginapi.Stat) error {
		return errors.New("cache is down")
	}, fc)
	p.run(context.Background())

	got, at := p.snapshot()
	if len(got) != 1 || got[0].Key != "store.x" {
		t.Errorf("snapshot = %v, want the collected stats despite the cache failing", got)
	}
	if at.IsZero() {
		t.Error("the snapshot timestamp was not set")
	}
	if len(job.errs) != 1 {
		t.Errorf("job errors = %v, want the cache failure recorded on the job", job.errs)
	}
	if got := rep.reported(); len(got) != 1 || got[0] != "stats/cache" {
		t.Errorf("reported %v, want one stats/cache", got)
	}
}

// TestSnapshotIsSafeUnderTheLock — the views read this from web requests while
// the job writes it. Run with -race, which is where this earns its place.
func TestSnapshotIsSafeUnderTheLock(t *testing.T) {
	p, _, _ := harness(t, okCache, &fakeContributor{name: "store", stats: []pluginapi.Stat{{Key: "a"}}})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); p.run(context.Background()) }()
		go func() { defer wg.Done(); p.snapshot() }()
	}
	wg.Wait()
}
