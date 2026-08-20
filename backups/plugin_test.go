package backups

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ── the fakes ───────────────────────────────────────────────────────

// fakeJob models what the host registry actually does: a run is in flight from
// SetRunning until something ends it, and both SetIdle and SetError end it.
type fakeJob struct {
	mu      sync.Mutex
	running bool
	started int
}

func (j *fakeJob) SetRunning() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.started = true, j.started+1
}

func (j *fakeJob) SetIdle(time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
}

func (j *fakeJob) SetError(string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
}

func (j *fakeJob) Log(string, ...any)    {}
func (j *fakeJob) MarkOffPeak() core.Job { return j }
func (j *fakeJob) MarkWrites() core.Job  { return j }
func (j *fakeJob) SetTrigger(func())     {}
func (j *fakeJob) IsPaused() bool        { return false }

func (j *fakeJob) stillRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

type fakeReporter struct{ ops []string }

func (r *fakeReporter) Report(_ context.Context, op string, err error) {
	if err != nil {
		r.ops = append(r.ops, op)
	}
}

func (r *fakeReporter) HandlerError(*gin.Context, string, error) {}

// entry is one thing the archive was asked to hold.
type entry struct {
	buf    bytes.Buffer
	closed bool
}

func (e *entry) Write(p []byte) (int, error) { return e.buf.Write(p) }
func (e *entry) Close() error                { e.closed = true; return nil }

// archive stands in for whatever the host writes into — a dated directory, a
// tar, an object store.
type archive struct {
	entries map[string]*entry
	order   []string
	failOn  string // OpenEntry returns an error for this name
}

func newArchive() *archive { return &archive{entries: map[string]*entry{}} }

func (a *archive) open(_ context.Context, name string) (io.WriteCloser, error) {
	if name == a.failOn {
		return nil, errors.New("no space left on device")
	}
	e := &entry{}
	a.entries[name] = e
	a.order = append(a.order, name)
	return e, nil
}

type hook struct {
	name    string
	payload string
	err     error
}

func (h *hook) BackupName() string { return h.name }

func (h *hook) Backup(_ context.Context, w io.Writer) error {
	if _, err := io.WriteString(w, h.payload); err != nil {
		return err
	}
	return h.err
}

func harness(t *testing.T, a *archive, dump func(context.Context, io.Writer) error, hooks ...*hook) (*Plugin, *fakeJob, *fakeReporter) {
	t.Helper()
	saved := deps
	t.Cleanup(func() { deps = saved })
	SetDeps(Deps{OpenEntry: a.open, DumpDB: dump})

	rep := &fakeReporter{}
	c := &core.Core{Errors: rep}
	for _, h := range hooks {
		if err := pluginapi.RegisterBackup(c, h); err != nil {
			t.Fatal(err)
		}
	}
	job := &fakeJob{}
	return &Plugin{core: c, job: job}, job, rep
}

// ── the invariant ───────────────────────────────────────────────────

// TestRunNeverLeavesTheJobInFlight. Same rule as every other job in this repo,
// and the same consequence stated in the comment beside the ctx check: a run
// left marked in flight never runs again and holds the shutdown drain open.
//
// It matters more here than most: this job runs WEEKLY, so a run that wedges
// the job is a site that quietly stops being backed up, and nothing says so
// until somebody needs the backup.
func TestRunNeverLeavesTheJobInFlight(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	okDump := func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, "-- dump"); return err }

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		dump   func(context.Context, io.Writer) error
		failOn string
		hooks  []*hook
	}{
		{"nothing to back up", context.Background(), nil, "", nil},
		{"the ordinary path", context.Background(), okDump, "", []*hook{{name: "store", payload: "rows"}}},
		{"the database dump fails", context.Background(),
			func(context.Context, io.Writer) error { return errors.New("pg_dump is missing") }, "", nil},
		{"a plugin hook fails", context.Background(), okDump, "",
			[]*hook{{name: "store", err: errors.New("table is locked")}}},
		{"the archive refuses an entry", context.Background(), okDump, "store.bak",
			[]*hook{{name: "store"}}},
		{"the context is cancelled", cancelled, okDump, "", []*hook{{name: "store"}}},
	} {
		a := newArchive()
		a.failOn = tc.failOn
		p, job, _ := harness(t, a, tc.dump, tc.hooks...)
		p.run(tc.ctx)
		if job.stillRunning() {
			t.Errorf("%s: the job is still marked running — the weekly backup will never run again", tc.name)
		}
		if job.started != 1 {
			t.Errorf("%s: SetRunning called %d times, want 1", tc.name, job.started)
		}
	}
}

func TestRunWithNoContextDoesNotStartAtAll(t *testing.T) {
	p, job, _ := harness(t, newArchive(), nil)
	p.run(nil)
	if job.started != 0 || job.stillRunning() {
		t.Errorf("started=%d running=%v — the guard must sit before SetRunning", job.started, job.stillRunning())
	}
}

// ── what ends up in the archive ─────────────────────────────────────

// TestOneFailingHookDoesNotCostTheBackup is the property worth the most here.
// A backup that aborts on the first plugin that errors is one that silently
// stops covering everything after it, in registry order, for a reason nobody
// looks at until a restore.
func TestOneFailingHookDoesNotCostTheBackup(t *testing.T) {
	// Registry order is sorted by name, so "aaa" fails first.
	broken := &hook{name: "aaa-broken", err: errors.New("table is locked")}
	fine := &hook{name: "zzz-fine", payload: "everything"}

	a := newArchive()
	p, job, rep := harness(t, a, nil, broken, fine)
	p.run(context.Background())

	e, ok := a.entries["zzz-fine.bak"]
	if !ok {
		t.Fatalf("the working hook was never written; archive holds %v", a.order)
	}
	if e.buf.String() != "everything" {
		t.Errorf("wrote %q, want the whole payload", e.buf.String())
	}
	if len(rep.ops) != 1 || rep.ops[0] != "backups/plugin" {
		t.Errorf("reported %v, want one backups/plugin — a failed hook must not be silent", rep.ops)
	}
	if job.stillRunning() {
		t.Error("the job was left running")
	}
}

// TestEveryEntryIsClosed. writeEntry defers Close, including on the path where
// the hook itself returns an error — a half-written entry still has to be
// closed or the archive holds an open handle per failing plugin, every week.
func TestEveryEntryIsClosed(t *testing.T) {
	a := newArchive()
	p, _, _ := harness(t, a,
		func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, "-- dump"); return err },
		&hook{name: "fine", payload: "ok"},
		&hook{name: "broken", payload: "half", err: errors.New("failed midway")},
	)
	p.run(context.Background())

	if len(a.order) != 3 {
		t.Fatalf("opened %v, want database.sql and both hooks", a.order)
	}
	for name, e := range a.entries {
		if !e.closed {
			t.Errorf("%s was left open", name)
		}
	}
	// The partial write is kept rather than discarded: this plugin does not
	// know whether half a payload is useless or the most that could be had,
	// and the reported error is what says the entry is suspect.
	if got := a.entries["broken.bak"].buf.String(); got != "half" {
		t.Errorf("broken.bak holds %q, want the partial write kept", got)
	}
}

// TestEntryNames pins the two naming rules the archive depends on: the database
// is database.sql, and a plugin's entry is its BackupName with .bak. A restore
// script reads these.
func TestEntryNames(t *testing.T) {
	a := newArchive()
	p, _, _ := harness(t, a,
		func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, "-- dump"); return err },
		&hook{name: "store", payload: "x"},
	)
	p.run(context.Background())

	if len(a.order) != 2 || a.order[0] != "database.sql" || a.order[1] != "store.bak" {
		t.Errorf("archive holds %v, want [database.sql store.bak] in that order", a.order)
	}
	if got := a.entries["database.sql"].buf.String(); got != "-- dump" {
		t.Errorf("database.sql holds %q", got)
	}
}

// TestNoDumpSeamMeansNoDatabaseEntry. DumpDB is documented optional — a host
// with no pg_dump should get the plugin hooks and no empty database.sql
// pretending the database was captured.
func TestNoDumpSeamMeansNoDatabaseEntry(t *testing.T) {
	a := newArchive()
	p, _, rep := harness(t, a, nil, &hook{name: "store", payload: "x"})
	p.run(context.Background())

	if _, ok := a.entries["database.sql"]; ok {
		t.Error("wrote a database entry with no dump seam wired")
	}
	if _, ok := a.entries["store.bak"]; !ok {
		t.Error("the plugin hooks were skipped along with the database")
	}
	if len(rep.ops) != 0 {
		t.Errorf("reported %v — a deliberately unwired seam is not an error", rep.ops)
	}
}

// TestCancellationStopsBeforeTheNextHook. Checked at the top of the loop, so a
// shutdown mid-backup does not start an entry it cannot finish.
func TestCancellationStopsBeforeTheNextHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := newArchive()
	p, _, _ := harness(t, a, nil, &hook{name: "store", payload: "x"})
	p.run(ctx)

	if len(a.order) != 0 {
		t.Errorf("opened %v under a cancelled context", a.order)
	}
}
