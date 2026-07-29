package backup

import (
	"context"
	"embed"
	"fmt"
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
	backupIntervalMin = 7 * 24 * 60 // weekly
	// The index is cheap — almost nothing changes between runs, so the stat
	// gate does the work and hashing is the exception. Daily keeps the window
	// in which an in-place overwrite can hide down to a day.
	indexIntervalMin = 24 * 60
)

// Plugin owns the single backup job. The mutex keeps a manual /admin/jobs
// trigger from racing the scheduled loop — a second concurrent run would
// pg_dump and zip the same assets into a different dated folder, doubling the
// IO and the disk for no benefit.
type Plugin struct {
	job *schedule.JobInfo
	mu  sync.Mutex

	// indexJob walks the asset classes and records a generation. Separate from
	// the archive job because they run on different clocks: the index is cheap
	// enough to run daily, the full archive is not.
	indexJob *schedule.JobInfo
	indexMu  sync.Mutex
	st       inventoryStore
	core     *core.Core
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "backup",
		Version:     "0.1.0",
		Description: "Weekly backup: zips persistent static-asset directories and dumps the PostgreSQL database, with retention pruning.",
		// Worker-only: no routes, just the scheduled loop.
		Processes:  []string{"worker"},
		Migrations: migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps == nil || deps.Config == nil {
		return fmt.Errorf("backup: SetDeps not called (Config) before core.Boot — wire it in the worker block")
	}
	if deps.BackupDir == "" {
		return fmt.Errorf("backup: SetDeps missing BackupDir")
	}
	if deps.DB.DBName == "" {
		return fmt.Errorf("backup: SetDeps missing DB connection (DBName empty) — pg_dump has nothing to target")
	}

	p.core = c
	p.st = NewPGStore(c.Storage.SchemaDB("backup"))

	p.indexJob = schedule.RegisterJob("Backup Index",
		"Walks the asset directories and records every file's size and content hash")
	p.indexJob.IntervalMin = indexIntervalMin
	p.indexJob.SetTrigger(func() { go p.runIndex(context.Background()) })

	p.job = schedule.RegisterJob("Backup",
		"Weekly backup: compresses cover art and dumps the PostgreSQL database")
	p.job.IntervalMin = backupIntervalMin
	// The trigger forces: an operator pressing Run in /admin/jobs means "now",
	// not "now unless a recent backup exists".
	p.job.SetTrigger(func() { go p.runForced(context.Background()) })

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
		// The boot delay replaces the old service's `for { sleep(week); run() }`,
		// which never ran at boot and so slept a full week before its first
		// backup. An hour is late enough not to compete with boot, early enough
		// that a fresh deploy has a backup the same day.
		{p.job, 1 * time.Hour, backupIntervalMin * time.Minute, p.run},
		// The index is the cheap half and its whole value is freshness — it is
		// what bounds the window in which an in-place overwrite can hide — so
		// it waits minutes rather than an hour.
		{p.indexJob, 10 * time.Minute, time.Duration(indexIntervalMin) * time.Minute, p.runIndex},
	}
}

func (p *Plugin) Start(ctx context.Context) error {
	// Bare ServiceLoop: the host installs the off-peak / interval-override /
	// CPU / panic hooks globally.
	for _, l := range p.loops() {
		go schedule.ServiceLoop(ctx, l.job, l.bootDelay, l.interval, l.run)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
