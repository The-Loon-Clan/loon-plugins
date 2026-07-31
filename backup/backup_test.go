package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
	"time"
)

// withBackupDir seeds COMPLETED run folders — each gets its completion marker,
// because a folder without one is a failed run and deliberately does not count
// as a backup. Use seedBackupDir(t, false, ...) for the failed-run case.
func withBackupDir(t *testing.T, stamps ...string) string {
	t.Helper()
	dir := seedBackupDir(t, true, stamps...)
	deps = &Deps{BackupDir: dir}
	t.Cleanup(func() { deps = nil })
	return dir
}

func seedBackupDir(t *testing.T, complete bool, stamps ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range stamps {
		if err := os.MkdirAll(filepath.Join(dir, s), 0o755); err != nil {
			t.Fatalf("seed %s: %v", s, err)
		}
		if complete {
			if err := os.WriteFile(filepath.Join(dir, s, completeMarker), []byte("stamp="+s), 0o644); err != nil {
				t.Fatalf("marker %s: %v", s, err)
			}
		}
	}
	return dir
}

// The guard that makes the boot delay safe. Without it, pairing a 1h boot delay
// with a weekly job means a full pg_dump + asset zip an hour after every
// restart; without the boot delay, the job never runs at all on a box that
// redeploys more often than weekly (the bug this replaced).
func TestNewestBackupAge(t *testing.T) {
	t.Run("no backups reads as never, not as recent", func(t *testing.T) {
		withBackupDir(t)
		if _, ok := (&Plugin{}).newestBackupAge(); ok {
			t.Error("ok=true for an empty dir — a fresh install must back up, not skip")
		}
	})

	t.Run("unreadable dir reads as never", func(t *testing.T) {
		deps = &Deps{BackupDir: filepath.Join(t.TempDir(), "does-not-exist")}
		t.Cleanup(func() { deps = nil })
		if _, ok := (&Plugin{}).newestBackupAge(); ok {
			t.Error("ok=true for a missing dir — must fail toward backing up")
		}
	})

	t.Run("picks the newest, not the last read", func(t *testing.T) {
		old := time.Now().Add(-30 * 24 * time.Hour).Format(stampFormat)
		recent := time.Now().Add(-2 * time.Hour).Format(stampFormat)
		// Seed oldest last so a naive "last entry wins" would pick wrong.
		withBackupDir(t, recent, old)

		age, ok := (&Plugin{}).newestBackupAge()
		if !ok {
			t.Fatal("ok=false with backups present")
		}
		if age > 3*time.Hour {
			t.Errorf("age = %s, want ~2h — it found an older folder than the newest", age)
		}
	})

	t.Run("ignores folders that are not ours", func(t *testing.T) {
		withBackupDir(t, "notes", "restore-me", "README")
		if _, ok := (&Plugin{}).newestBackupAge(); ok {
			t.Error("ok=true — unrelated folders must not read as a backup")
		}
	})
}

// The pre-flight is the guard between "no backup today" and "the site is down".
// This job stages a full copy locally, so on a volume without room it fills the
// disk and the site starts erroring — an outage caused by the backup, which
// protects nothing and breaks what it was protecting.
func TestPreflight(t *testing.T) {
	const gb = int64(1) << 30

	newPlugin := func(t *testing.T, free, dbSize int64, mode string) *Plugin {
		t.Helper()
		assets := t.TempDir()
		// ~1 MB of "covers".
		if err := os.WriteFile(filepath.Join(assets, "cover.jpg"), make([]byte, 1<<20), 0o644); err != nil {
			t.Fatal(err)
		}
		deps = &Deps{
			BackupDir: t.TempDir(),
			Classes:   []AssetClass{{Slug: "covers", Dir: assets}},
			Config:    stubConfig{mode: mode},
			FreeDisk:  func(context.Context) (int64, error) { return free, nil },
			DBSize:    func(context.Context) (int64, error) { return dbSize, nil },
		}
		t.Cleanup(func() { deps = nil })
		return &Plugin{job: schedule.RegisterJob("Backup test "+t.Name(), "")}
	}

	t.Run("refuses when the backup would not fit", func(t *testing.T) {
		p := newPlugin(t, 1*gb, 10*gb, "full") // 10GB DB, 1GB free
		if p.preflightOK(context.Background(), false) {
			t.Error("pre-flight passed with 1GB free for a 10GB database — this is the disk-full outage")
		}
	})

	t.Run("allows when there is ample room", func(t *testing.T) {
		p := newPlugin(t, 100*gb, 1*gb, "full")
		if !p.preflightOK(context.Background(), false) {
			t.Error("pre-flight refused with 100GB free for a 1GB database")
		}
	})

	t.Run("a manual trigger does not override it", func(t *testing.T) {
		p := newPlugin(t, 1*gb, 10*gb, "full")
		if p.preflightOK(context.Background(), true) {
			t.Error("force=true bypassed the disk pre-flight — an operator pressing Run is not asking for an outage")
		}
	})

	t.Run("db_only ignores asset size but still checks the dump", func(t *testing.T) {
		p := newPlugin(t, 1*gb, 10*gb, "db_only")
		if p.preflightOK(context.Background(), false) {
			t.Error("db_only passed with 1GB free for a 10GB dump")
		}
	})

	t.Run("an unreadable free-disk probe skips rather than guesses", func(t *testing.T) {
		p := newPlugin(t, 100*gb, 1*gb, "full")
		deps.FreeDisk = func(context.Context) (int64, error) { return 0, errors.New("boom") }
		if p.preflightOK(context.Background(), false) {
			t.Error("pre-flight proceeded despite not knowing free space — must fail toward not backing up")
		}
	})

	t.Run("an unreadable db-size probe skips rather than guesses", func(t *testing.T) {
		p := newPlugin(t, 100*gb, 1*gb, "full")
		deps.DBSize = func(context.Context) (int64, error) { return 0, errors.New("boom") }
		if p.preflightOK(context.Background(), false) {
			t.Error("pre-flight proceeded despite not knowing the dump size")
		}
	})
}

type stubConfig struct {
	mode    string
	keep    int
	exclude []string
	dbKeep  int
}

func (s stubConfig) GetBackupMode(context.Context) string   { return s.mode }
func (s stubConfig) GetBackupKeepCount(context.Context) int { return s.keep }
func (s stubConfig) GetBackupDBExcludeTableData(context.Context) []string {
	return s.exclude
}
func (s stubConfig) GetBackupDBKeep(context.Context) int { return s.dbKeep }

// prune must only ever touch its own dated folders: BackupDir is a bind mount
// an operator may keep other things in.
func TestPrune(t *testing.T) {
	logf := func(string, ...any) {}

	t.Run("keeps the newest N and deletes the rest", func(t *testing.T) {
		stamps := []string{
			"2026-01-01_000000", "2026-02-01_000000",
			"2026-03-01_000000", "2026-04-01_000000",
		}
		dir := withBackupDir(t, stamps...)

		(&Plugin{}).prune(logf, 2)

		for _, gone := range stamps[:2] {
			if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
				t.Errorf("%s survived, want pruned", gone)
			}
		}
		for _, kept := range stamps[2:] {
			if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
				t.Errorf("%s was pruned, want kept (it is one of the newest 2)", kept)
			}
		}
	})

	t.Run("keep<=0 disables pruning", func(t *testing.T) {
		dir := withBackupDir(t, "2026-01-01_000000", "2026-02-01_000000")
		(&Plugin{}).prune(logf, 0)
		entries, _ := os.ReadDir(dir)
		if len(entries) != 2 {
			t.Errorf("%d folders left, want 2 — keep=0 means retain everything", len(entries))
		}
	})

	t.Run("never touches foreign files", func(t *testing.T) {
		dir := withBackupDir(t, "2026-01-01_000000", "2026-02-01_000000", "2026-03-01_000000")
		foreign := filepath.Join(dir, "DO-NOT-DELETE.txt")
		if err := os.WriteFile(foreign, []byte("operator's"), 0o644); err != nil {
			t.Fatal(err)
		}
		foreignDir := filepath.Join(dir, "manual-restore")
		if err := os.MkdirAll(foreignDir, 0o755); err != nil {
			t.Fatal(err)
		}

		(&Plugin{}).prune(logf, 1)

		if _, err := os.Stat(foreign); err != nil {
			t.Error("prune deleted a non-backup file — BackupDir is a bind mount, not ours alone")
		}
		if _, err := os.Stat(foreignDir); err != nil {
			t.Error("prune deleted a non-backup directory")
		}
	})

	t.Run("fewer than keep is a no-op", func(t *testing.T) {
		dir := withBackupDir(t, "2026-01-01_000000")
		(&Plugin{}).prune(logf, 5)
		if _, err := os.Stat(filepath.Join(dir, "2026-01-01_000000")); err != nil {
			t.Error("pruned the only backup when keep=5")
		}
	})
}

// A failed run must not be able to answer "did we back up recently?".
//
// This was a real defect with a seven-day blast radius: doRun created the dated
// folder BEFORE doing any work, logged "Backup complete" whatever happened, and
// newestBackupAge counted any dated folder. So a run whose pg_dump failed left
// behind the exact artifact that told the next six scheduled runs to skip. The
// evidence of the failure was the thing that silenced it.
func TestAFailedRunDoesNotCountAsABackup(t *testing.T) {
	recent := time.Now().Add(-2 * time.Hour).Format(stampFormat)

	t.Run("an unmarked folder reads as never backed up", func(t *testing.T) {
		deps = &Deps{BackupDir: seedBackupDir(t, false, recent)}
		t.Cleanup(func() { deps = nil })
		if age, ok := (&Plugin{}).newestBackupAge(); ok {
			t.Errorf("a folder with no completion marker counted as a backup (age=%s) — "+
				"a failed run would suppress the next week of attempts", age)
		}
	})

	t.Run("a completed folder still counts", func(t *testing.T) {
		withBackupDir(t, recent)
		if _, ok := (&Plugin{}).newestBackupAge(); !ok {
			t.Error("a completed backup was ignored — the skip guard would never fire and every tick would re-dump")
		}
	})

	t.Run("the newest COMPLETED one wins, not the newest folder", func(t *testing.T) {
		older := time.Now().Add(-30 * 24 * time.Hour).Format(stampFormat)
		dir := seedBackupDir(t, true, older)
		// A newer run that failed: folder present, no marker.
		if err := os.MkdirAll(filepath.Join(dir, recent), 0o755); err != nil {
			t.Fatal(err)
		}
		deps = &Deps{BackupDir: dir}
		t.Cleanup(func() { deps = nil })

		age, ok := (&Plugin{}).newestBackupAge()
		if !ok {
			t.Fatal("ok=false despite a completed backup being present")
		}
		if age < 20*24*time.Hour {
			t.Errorf("age = %s — the failed newer run was counted, which is exactly the bug: "+
				"a broken backup hiding a month-old real one", age)
		}
	})
}

// The end-to-end shape of the silent-success bug: a run that fails part-way
// must not leave behind something the next run mistakes for a backup, and must
// not report itself as healthy.
//
// The database dump is made to fail by pointing pg_dump at a closed port, which
// is also the realistic failure (a db container down, wrong credentials). Before
// the fix this produced a dated folder, the log line "Backup complete", a job
// status of idle with no error, and six subsequent weekly runs skipped.
func TestAPartialRunIsReportedAndLeavesNoUsableMarker(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "cover.jpg"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	deps = &Deps{
		BackupDir: backupDir,
		Classes:   []AssetClass{{Slug: "covers", Dir: assets}},
		Config:    stubConfig{mode: "full"},
		FreeDisk:  func(context.Context) (int64, error) { return 1 << 40, nil },
		DBSize:    func(context.Context) (int64, error) { return 1 << 20, nil },
		// Port 1 is closed, so pg_dump fails immediately rather than hanging.
		DB: PGConn{Host: "127.0.0.1", Port: 1, User: "nobody", DBName: "nothing"},
	}
	t.Cleanup(func() { deps = nil })

	p := &Plugin{job: schedule.RegisterJob("Backup test "+t.Name(), "")}
	p.doRun(context.Background(), true)

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() {
			runs = append(runs, e.Name())
		}
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly one run folder, got %v", runs)
	}

	// The asset zip should have succeeded, proving the run really did partial
	// work rather than bailing before it started.
	if _, err := os.Stat(filepath.Join(backupDir, runs[0], "covers.zip")); err != nil {
		t.Errorf("the asset zip is missing, so this test is not exercising a PARTIAL failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, runs[0], completeMarker)); err == nil {
		t.Error("a run whose database dump failed still wrote the completion marker — " +
			"the next six weekly runs would skip on the strength of it")
	}

	// And the operator must be able to see it. SetIdle clears LastError, so an
	// unconditional deferred SetIdle would erase this.
	if p.job.Status != "error" {
		t.Errorf("job status = %q, want \"error\" — a failed backup reporting idle is the whole defect", p.job.Status)
	}
	if p.job.LastError == "" {
		t.Error("LastError is empty after a failed run")
	}

	// The decisive consequence: this folder must not answer "did we back up?".
	if age, ok := p.newestBackupAge(); ok {
		t.Errorf("the failed run counted as a backup (age=%s)", age)
	}
}

// An asset class whose directory is absent must be counted as a failed part,
// named in the error, and must withhold the completion marker.
//
// This is not hypothetical: production currently has four classes indexing zero
// files because their bind mounts are missing, and an unmounted class is
// indistinguishable from an empty one from inside the container. The difference
// here is that a MISSING directory makes zipDir return an error outright, so it
// is the one case the job can actually name — and it must not be swallowed by
// the per-class `continue`.
func TestAMissingAssetDirectoryIsNamedInTheFailure(t *testing.T) {
	present := t.TempDir()
	if err := os.WriteFile(filepath.Join(present, "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "unmounted-class")

	backupDir := t.TempDir()
	deps = &Deps{
		BackupDir: backupDir,
		// Missing FIRST, deliberately (Order, not slice position — the archive
		// sorts). With the healthy class ahead of it the test cannot tell
		// `continue` from `break`: the healthy zip would be written either way.
		Classes: []AssetClass{
			{Slug: "unmounted-class", Dir: missing, Order: 1},
			{Slug: "present", Dir: present, Order: 2},
		},
		Config:   stubConfig{mode: "full"},
		FreeDisk: func(context.Context) (int64, error) { return 1 << 40, nil },
		DBSize:   func(context.Context) (int64, error) { return 1 << 20, nil },
		DB:       PGConn{Host: "127.0.0.1", Port: 1, User: "nobody", DBName: "nothing"},
	}
	t.Cleanup(func() { deps = nil })

	p := &Plugin{job: schedule.RegisterJob("Backup test "+t.Name(), "")}
	p.doRun(context.Background(), true)

	if !strings.Contains(p.job.LastError, "unmounted-class.zip") {
		t.Errorf("LastError = %q — a missing asset directory must be named, not swallowed by the "+
			"per-class continue; that is how an unmounted volume stays invisible", p.job.LastError)
	}
	// The class that WAS present must still have been archived: one broken class
	// must not cost the others their backup.
	if _, err := os.Stat(filepath.Join(backupDir, mustOneRun(t, backupDir), "present.zip")); err != nil {
		t.Errorf("a healthy class was not archived because another class failed: %v", err)
	}
}

func mustOneRun(t *testing.T, backupDir string) string {
	t.Helper()
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return e.Name()
		}
	}
	t.Fatal("no run folder created")
	return ""
}

// If the run folder cannot be created there is no backup at all, and that must
// read as a failure rather than as a quiet no-op. The realistic cause is a
// BackupDir that is not what the operator thinks it is — a file, a stale
// symlink, or a read-only mount.
func TestAnUncreatableRunFolderIsAFailure(t *testing.T) {
	// BackupDir is a FILE, so MkdirAll(BackupDir/<stamp>) cannot succeed.
	notADir := filepath.Join(t.TempDir(), "backups")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps = &Deps{
		BackupDir: notADir,
		Classes:   []AssetClass{{Slug: "covers", Dir: assets}},
		Config:    stubConfig{mode: "full"},
		FreeDisk:  func(context.Context) (int64, error) { return 1 << 40, nil },
		DBSize:    func(context.Context) (int64, error) { return 1 << 20, nil },
		DB:        PGConn{Host: "127.0.0.1", Port: 1, User: "nobody", DBName: "nothing"},
	}
	t.Cleanup(func() { deps = nil })

	p := &Plugin{job: schedule.RegisterJob("Backup test "+t.Name(), "")}
	p.doRun(context.Background(), true)

	if p.job.Status != "error" {
		t.Errorf("job status = %q, want \"error\" — a run that created nothing reported healthy", p.job.Status)
	}
	if !strings.Contains(p.job.LastError, "backup directory") {
		t.Errorf("LastError = %q, want it to name the backup directory", p.job.LastError)
	}
}

// The mode that makes a backup possible at all on this box.
//
// Measured in production: a full run needs 202 GB of free space and the volume
// has 180 GB, so the pre-flight refuses and NOTHING is protected — not the
// database, not the 13 GB of irreplaceable artwork. One class (screenshots) is
// 116 GB of the 129 GB asset tree and is the only regenerable one. Skipping it
// drops the requirement to 63 GB and the same run covers everything else.
func TestSkipRegenerableMakesAnImpossibleBackupPossible(t *testing.T) {
	// Stand-ins with the production shape: one huge regenerable class, one
	// small irreplaceable one.
	huge := t.TempDir()
	if err := os.WriteFile(filepath.Join(huge, "frame.jpg"), make([]byte, 8<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	small := t.TempDir()
	if err := os.WriteFile(filepath.Join(small, "mascot.png"), make([]byte, 1<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	classes := []AssetClass{
		{Slug: "mascots", Dir: small, Order: 10},
		{Slug: "screenshots", Dir: huge, Order: 90, Regenerable: true},
	}

	newPlugin := func(mode string, free int64) *Plugin {
		deps = &Deps{
			BackupDir: t.TempDir(),
			Classes:   classes,
			Config:    stubConfig{mode: mode},
			FreeDisk:  func(context.Context) (int64, error) { return free, nil },
			DBSize:    func(context.Context) (int64, error) { return 1 << 20, nil },
		}
		t.Cleanup(func() { deps = nil })
		return &Plugin{job: schedule.RegisterJob("Backup test "+t.Name()+mode, "")}
	}

	// Free space chosen to sit BETWEEN the two requirements, which is the only
	// range where the mode makes a difference: the full run needs
	// (8 MiB + 1 KiB + 1 MiB) x 1.2 ~= 10.8 MiB, skipping the regenerable class
	// needs (1 KiB + 1 MiB) x 1.2 ~= 1.2 MiB.
	const free = 5 << 20

	if newPlugin(ModeFull, free).preflightOK(context.Background(), false) {
		t.Error("full mode passed the pre-flight when the regenerable class does not fit — " +
			"the gate is not measuring what the run will write")
	}
	if !newPlugin(ModeSkipRegenerable, free).preflightOK(context.Background(), false) {
		t.Error("skip_regenerable refused although only the regenerable class was too big; " +
			"this is the mode that exists to make the backup possible, so refusing here protects nothing")
	}
	if !newPlugin(ModeDBOnly, free).preflightOK(context.Background(), false) {
		t.Error("db_only refused despite needing only the dump")
	}

	// And the selection itself, independent of disk.
	deps = &Deps{Classes: classes}
	t.Cleanup(func() { deps = nil })
	if got := len(archiveClasses(ModeFull)); got != 2 {
		t.Errorf("full archives %d classes, want 2", got)
	}
	skipped := archiveClasses(ModeSkipRegenerable)
	if len(skipped) != 1 || skipped[0].Slug != "mascots" {
		t.Errorf("skip_regenerable archived %v, want just the non-regenerable class", slugs(skipped))
	}
	if got := archiveClasses(ModeDBOnly); len(got) != 0 {
		t.Errorf("db_only archived %v, want nothing", slugs(got))
	}
	// An unknown mode must archive everything rather than silently skipping —
	// failing toward MORE data is the only safe default in a backup.
	if got := len(archiveClasses("typo")); got != 2 {
		t.Errorf("an unrecognised mode archived %d classes, want all 2", got)
	}
}

// Every job this plugin REGISTERS must also be SCHEDULED.
//
// The index job was registered with a trigger and an IntervalMin, which is
// enough for /admin/jobs to render a Run button and an interval, but Start()
// only ever launched a loop for the archive job. So the index ran solely when
// somebody pressed the button: run_count reset to 0 on every deploy, next_run
// was a time nothing would honour, and the inventory quietly stopped advancing.
// Nothing errored — the job just sat idle forever looking configured.
func TestEveryRegisteredJobIsScheduled(t *testing.T) {
	p := &Plugin{
		job:      schedule.RegisterJob("Backup test archive "+t.Name(), ""),
		indexJob: schedule.RegisterJob("Backup test index "+t.Name(), ""),
		dumpJob:  schedule.RegisterJob("Backup test dump "+t.Name(), ""),
	}

	scheduled := map[*schedule.JobInfo]scheduledJob{}
	for _, l := range p.loops() {
		if l.job == nil {
			t.Fatal("a loop was declared with no job")
		}
		if _, dup := scheduled[l.job]; dup {
			t.Errorf("%s is scheduled twice — two loops would run it concurrently", l.job.Name)
		}
		scheduled[l.job] = l
	}

	for _, want := range []struct {
		name string
		job  *schedule.JobInfo
	}{
		{"archive", p.job},
		{"index", p.indexJob},
		{"database dump", p.dumpJob},
	} {
		l, ok := scheduled[want.job]
		if !ok {
			t.Errorf("the %s job is registered but never scheduled — it would only ever run "+
				"when an operator pressed Run, which is how the inventory silently stopped advancing", want.name)
			continue
		}
		if l.run == nil {
			t.Errorf("the %s job is scheduled with no function to run", want.name)
		}
		if l.interval <= 0 {
			t.Errorf("the %s job has interval %s — a non-positive interval would spin", want.name, l.interval)
		}
		// A boot delay of zero would put a heavy pass in the middle of startup,
		// competing with migrations and cache warming on every deploy.
		if l.bootDelay <= 0 {
			t.Errorf("the %s job has boot delay %s, want a positive delay", want.name, l.bootDelay)
		}
		if l.bootDelay >= l.interval {
			t.Errorf("the %s job waits %s before its first run but repeats every %s — "+
				"the delay must not exceed the interval or the first run is late for no reason",
				want.name, l.bootDelay, l.interval)
		}
	}
}

// A missing SetDeps must be fatal where this plugin BACKS UP and merely
// disabling where it only DISPLAYS.
//
// This is a regression test for a site outage. The admin page was added, "web"
// joined Metadata.Processes, and the host staged SetDeps in its worker block
// only — so every web process hit Provision's refusal and the whole site
// failed to boot on deploy. A backup that cannot run must be loud; an admin
// page that cannot render must not take the site down with it.
func TestProvisionWithoutDepsDisablesThePageButFailsTheWorker(t *testing.T) {
	saved := deps
	deps = nil
	t.Cleanup(func() { deps = saved })

	web := &Plugin{}
	if err := web.Provision(&core.Core{Process: "web", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatalf("web Provision returned %v — an unconfigured admin page must not fail boot", err)
	}

	worker := &Plugin{}
	if err := worker.Provision(&core.Core{Process: "worker"}); err == nil {
		t.Error("worker Provision succeeded without deps — a backup that cannot run must refuse loudly")
	}
}
