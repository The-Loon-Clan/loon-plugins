package backup

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"sync"
	"time"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// migrations carries the inventory schema. This plugin used to be deliberately
// stateless — its README argued the dated folders WERE the last-run record —
// and that was right for "zip everything again each week". It cannot survive an
// incremental: deciding what changed requires remembering what was there, and
// the per-file inventory is also the first answer this site has ever had to
// "what is on disk and is it intact".
//
//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("backup", func() core.Plugin { return &Plugin{} })
}

const (
	// The index is cheap — almost nothing changes between runs, so the stat
	// gate does the work and hashing is the exception. Daily keeps the window
	// in which an in-place overwrite can hide down to a day.
	indexIntervalMin = 24 * 60
	// The database dump is DAILY.
	//
	// It was weekly, justified as "a week-old copy restores just as
	// correctly". That is true about correctness and silent about loss: the
	// assets are captured continuously, so the database was the only thing
	// with a seven-day recovery point, and what it holds — accounts, points
	// ledger, comments, forum posts — is exactly the part that cannot be
	// re-derived from anything. A restore that works perfectly and returns
	// the site to last Tuesday is still the worst outcome in the system.
	//
	// The cost is real and was the original argument: a full dump every time
	// (the update rate makes an incremental impossible — see dbdump.go), so
	// daily means moving the whole thing daily. Measured rather than
	// estimated: 51.6 GB of heap+TOAST, of which nzbs.nzb_data alone is 35.6
	// GB and is irreplaceable, landing as ~29 GB compressed. The exclusion
	// list below trims what it honestly can. That is a bigger nightly
	// transfer, on a tailnet, off-peak — and it is worth it to turn a
	// seven-day worst case into a one-day one.
	dbDumpIntervalMin = 24 * 60
	// Parallel dump workers. Four is pg_dump's sweet spot on this box and the
	// number the restore side mirrors (pg_restore -j 4); higher mostly buys
	// contention with the live site, which is running while this dumps.
	dbDumpJobs = 4
)

// Plugin owns the two jobs that put this box's data somewhere else: the index
// that inventories it and the dump that captures the database. Each carries
// its own mutex so a manual /admin/jobs trigger cannot race the scheduled
// loop — two concurrent passes would do the same work twice for no benefit.
//
// The local archive job that used to live here is gone; see
// archive_retired.go for what replaced it and why.
type Plugin struct {
	// indexJob walks the asset classes and records a generation.
	indexJob *schedule.JobInfo
	indexMu  sync.Mutex
	// dumpJob writes the database into the asset tree as ordinary files, so
	// the pull pipeline carries it with no transport code of its own
	// (dbdump.go). Its own mutex: a manual trigger racing the weekly loop
	// would run two pg_dumps against the live database at once.
	dumpJob *schedule.JobInfo
	dumpMu  sync.Mutex
	st      inventoryStore
	core    *core.Core
	// tmpl backs the admin page (views.go), parsed in the web process only.
	tmpl *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "backup",
		Version:     "0.1.0",
		Description: "Backup pipeline: daily asset indexing into content-addressed packs plus a daily PostgreSQL dump, with retention pruning.",
		// web too, for the admin page. The jobs stay worker-side — see
		// Provision, which gates on Process; without that gate the web process
		// would register three jobs it never runs (a Run button and a next_run
		// that nothing honours) and Start would launch a second set of loops.
		Processes:  []string{"web", "worker"},
		Flavours:   []string{core.FlavourAny},
		Migrations: migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// Missing deps are fatal where this plugin BACKS UP and merely disabling
	// where it only DISPLAYS.
	//
	// Both halves are the lesson from one deploy: the page was added, "web"
	// joined Processes, and the host staged SetDeps in its worker block only —
	// so every web process hit the refusal below and the whole site failed to
	// boot. A backup that cannot run must be loud, but an admin page that
	// cannot render has no business taking the site down with it.
	if deps == nil || deps.Config == nil || deps.DB.DBName == "" {
		if c.Process == "web" {
			c.Logger.Info("backup: SetDeps not staged in this process — the admin page is " +
				"unavailable here; stage it in the SHARED section of the host's wiring, " +
				"not the worker block")
			return nil
		}
		switch {
		case deps == nil || deps.Config == nil:
			return fmt.Errorf("backup: SetDeps not called (Config) before core.Boot — stage it in the host's shared wiring")
		default:
			return fmt.Errorf("backup: SetDeps missing DB connection (DBName empty) — pg_dump has nothing to target")
		}
	}

	p.core = c
	p.st = NewPGStore(c.Storage.SchemaDB("backup"))

	// web/all: the admin page. Read-only over the same store the worker
	// writes, so it needs nothing else staged.
	if c.Process == "web" || c.Process == "all" {
		if err := p.registerViews(c); err != nil {
			return err
		}
	}
	if c.Process == "web" {
		// Jobs and the pack server belong to the worker. Returning here keeps
		// the web process from registering either.
		return nil
	}

	p.indexJob = schedule.RegisterJob("Backup Index",
		"Walks the asset directories and records every file's size and content hash").
		MarkWrites()
	p.indexJob.IntervalMin = indexIntervalMin
	p.indexJob.SetTriggerAsync(func() { p.runIndex(context.Background()) })

	p.dumpJob = schedule.RegisterJob("Backup Database",
		"Dumps PostgreSQL into the asset tree so the pull pipeline carries it").
		MarkWrites()
	p.dumpJob.IntervalMin = dbDumpIntervalMin
	p.dumpJob.SetTriggerAsync(func() { p.runDBDump(context.Background()) })

	// Publish the pack server so the host can mount HTTP routes over it
	// without importing this package. Registered here rather than in Start
	// because the host wires its routes straight after Boot, which runs
	// Provision — by Start it would be too late.
	if err := c.Register(lpapi.BackupPacksName, packServer{p: p}); err != nil {
		return err
	}
	return nil
}

// scheduledJob is one background loop: which job, when it first runs, how often
// after that, and what it does.
type scheduledJob struct {
	job       *schedule.JobInfo
	bootDelay time.Duration
	interval  time.Duration
	run       func(context.Context)
}

// loops is every background loop this plugin runs.
//
// Declared as data so a test can assert that every REGISTERED job also gets
// SCHEDULED. That invariant was broken and stayed broken silently: the index
// job was registered with a trigger and an IntervalMin — which is enough to
// render a Run button and an interval in /admin/jobs — but no loop was ever
// started for it. It therefore only ran when an operator pressed the button,
// showed run_count 0 after every deploy, and reported a next_run that nothing
// would ever honour. Nothing failed; the inventory simply stopped advancing.
func (p *Plugin) loops() []scheduledJob {
	return []scheduledJob{
		// The index is the cheap half and its whole value is freshness — it is
		// what bounds the window in which an in-place overwrite can hide — so
		// it waits minutes rather than an hour.
		{p.indexJob, 10 * time.Minute, time.Duration(indexIntervalMin) * time.Minute, p.runIndex},
		// The dump waits longer than the archive: it is the heaviest thing this
		// plugin does to the live database, and a box that just booted is
		// already busy. Ninety minutes also puts it AFTER the first index, so
		// a fresh dump is picked up by the following day's pass rather than
		// sitting unindexed for a week.
		{p.dumpJob, 90 * time.Minute, time.Duration(dbDumpIntervalMin) * time.Minute, p.runDBDump},
	}
}

func (p *Plugin) Start(ctx context.Context) error {
	// No jobs registered means this is the WEB process, where Provision
	// returns after the admin page and deliberately registers none. Launching
	// loops anyway hands ServiceLoop a nil *JobInfo, and the first thing it
	// does with one is call IsPaused on it:
	//
	//   panic: runtime error: invalid memory address or nil pointer dereference
	//     schedule.(*JobInfo).IsPaused ← schedule.ServiceLoop ← backup.Start
	//
	// which killed the web process on boot. Adding "web" to Processes for the
	// page is what created the shape; this is the other half of that change.
	if p.indexJob == nil {
		return nil
	}
	// Bare ServiceLoop: the host installs the off-peak / interval-override /
	// CPU / panic hooks globally.
	for _, l := range p.loops() {
		go schedule.ServiceLoop(ctx, l.job, l.bootDelay, l.interval, l.run)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
