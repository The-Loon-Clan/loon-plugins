package dbmaint

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

func init() {
	core.RegisterPlugin("dbmaint", func() core.Plugin { return &Plugin{} })
}

const (
	pgRepackIntervalMin   = 7 * 24 * 60  // weekly
	reindexIntervalMin    = 30 * 24 * 60 // monthly
	verifyIntervalMin     = 7 * 24 * 60  // weekly
	vacuumFullIntervalMin = 7 * 24 * 60  // weekly (legacy; paused by default)

	pgRepackStateKey   = "pg_repack_nzbs"
	reindexStateKey    = "reindex_concurrently"
	verifyStateKey     = "verify_indexes"
	vacuumFullStateKey = "vacuum_full_nzbs"
)

// Plugin owns the three maintenance jobs. Each has its own mutex so a manual
// /admin/jobs trigger can't race the scheduled loop.
type Plugin struct {
	// core is held for its error sink: a permanent REINDEX failure has to
	// outlive the job's in-memory log ring, which every deploy clears.
	core *core.Core

	repack  *schedule.JobInfo
	reindex *schedule.JobInfo
	verify  *schedule.JobInfo
	vacuum  *schedule.JobInfo

	repackMu  sync.Mutex
	reindexMu sync.Mutex
	verifyMu  sync.Mutex
	vacuumMu  sync.Mutex
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "dbmaint",
		Version:     "0.1.0",
		Description: "Database maintenance: online pg_repack + REINDEX CONCURRENTLY, plus the legacy maintenance-window VACUUM FULL.",
		// Worker-only: no routes, just the scheduled loops.
		Processes: []string{"worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if deps == nil || deps.Diag == nil || deps.StatCache == nil || deps.ConfigStore == nil {
		return fmt.Errorf("dbmaint: SetDeps not called (Diag/StatCache/ConfigStore) before core.Boot — wire it in the worker block")
	}
	if deps.Nzbs == nil || deps.Maintenance == nil {
		return fmt.Errorf("dbmaint: SetDeps missing Nzbs/Maintenance (needed for VACUUM FULL)")
	}
	p.provisionRepack()
	p.provisionReindex()
	p.provisionVerify()
	p.provisionVacuum()
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	// Bare ServiceLoop: the host installs the off-peak / interval-override /
	// CPU / panic hooks globally, so all three jobs inherit them. Boot delays
	// stagger the heavy jobs (repack before reindex before vacuum).
	go schedule.ServiceLoop(ctx, p.repack, 1*time.Hour, time.Duration(pgRepackIntervalMin)*time.Minute, p.runRepack)
	go schedule.ServiceLoop(ctx, p.reindex, 2*time.Hour, time.Duration(reindexIntervalMin)*time.Minute, p.runReindex)
	go schedule.ServiceLoop(ctx, p.verify, 3*time.Hour, time.Duration(verifyIntervalMin)*time.Minute, p.runVerify)
	go schedule.ServiceLoop(ctx, p.vacuum, 1*time.Hour, time.Duration(vacuumFullIntervalMin)*time.Minute, p.runVacuum)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

func (p *Plugin) provisionRepack() {
	p.repack = schedule.RegisterJob("Repack NZBs (online)",
		"Runs `pg_repack -t nzbs -x` to reclaim disk space and rebuild indexes "+
			"online — no maintenance window. Replaces the legacy weekly VACUUM "+
			"FULL job. Skips with a logged warning if the pg_repack binary or "+
			"extension isn't installed.").MarkOffPeak()
	p.repack.IntervalMin = pgRepackIntervalMin
	p.repack.SetTrigger(func() { go p.runRepack(context.Background()) })
	p.repack.DeclareConfig(deps.ConfigStore,
		schedule.JobConfigVar{
			Key:         "tables",
			Label:       "Tables (comma-separated)",
			Description: "Which tables pg_repack rebuilds each tick. Default 'nzbs,posters' covers the two biggest bloat targets (nzbs has the heaviest UPDATE traffic, posters the second-largest by row size); add 'nzb_requests', 'flow_nodes', etc. if those grow heavy. Each table is repacked sequentially within one tick.",
			Type:        schedule.JobConfigString,
			Default:     "nzbs,posters",
		},
		schedule.JobConfigVar{
			Key:         "disk_safety_multiplier",
			Label:       "Free-disk safety multiplier",
			Description: "Pre-flight gate: refuse to start unless free disk on the working volume is at least table_size × this multiplier. 120 = 1.2×. Same shape as the old VACUUM FULL knob; pg_repack also needs a full second copy on disk during the rewrite.",
			Type:        schedule.JobConfigInt,
			Default:     "120",
		},
		schedule.JobConfigVar{
			Key:         "soft_timeout_minutes",
			Label:       "Soft timeout (minutes)",
			Description: "Hard cap on a single repack run as a watchdog — aborts if pg_repack hangs (e.g. waiting on an exclusive lock). Generous default (240 = 4 hours) because the site stays up; tighten if you want earlier alerts.",
			Type:        schedule.JobConfigInt,
			Default:     "240",
		},
	)
}

func (p *Plugin) provisionReindex() {
	p.reindex = schedule.RegisterJob("Reindex (online)",
		"Runs REINDEX INDEX CONCURRENTLY on the largest indexes in the public "+
			"schema once a month, skipping any tables already covered by "+
			"pg_repack -x. Online operation — site stays up, brief locks only "+
			"at swap time.").MarkOffPeak()
	p.reindex.IntervalMin = reindexIntervalMin
	p.reindex.SetTrigger(func() { go p.runReindex(context.Background()) })
	p.reindex.DeclareConfig(deps.ConfigStore,
		schedule.JobConfigVar{
			Key:         "skip_tables",
			Label:       "Skip tables (comma-separated)",
			Description: "Tables whose indexes pg_repack already rebuilds — skip them here to avoid duplicate work. Should match the pg_repack 'tables' config. Default 'nzbs' covers the headline target.",
			Type:        schedule.JobConfigString,
			Default:     "nzbs",
		},
		schedule.JobConfigVar{
			Key:         "min_size_mb",
			Label:       "Minimum index size (MB)",
			Description: "Indexes smaller than this are skipped — rebuilding a 200KB index every month is wasted work. Larger threshold = fewer rebuilds but more lingering bloat on medium indexes.",
			Type:        schedule.JobConfigInt,
			Default:     "5",
		},
		schedule.JobConfigVar{
			Key:         "max_indexes_per_run",
			Label:       "Max indexes per run",
			Description: "Cap on how many REINDEXes one tick performs, so a monthly run can't run for many hours and accidentally race the pg_repack run. Larger indexes take longer; tune down if you see runs piling up.",
			Type:        schedule.JobConfigInt,
			Default:     "50",
		},
	)
}

func (p *Plugin) provisionVerify() {
	p.verify = schedule.RegisterJob("Verify Indexes",
		"Reads every collatable btree index against its table with amcheck's "+
			"bt_index_check and reports any that disagree. Detects the silent "+
			"corruption a glibc/ICU collation change causes, which no error "+
			"message and no structural check will ever surface. Read-only "+
			"(AccessShareLock) — the site stays up.").MarkOffPeak()
	p.verify.IntervalMin = verifyIntervalMin
	p.verify.SetTrigger(func() { go p.runVerify(context.Background()) })
	p.verify.DeclareConfig(deps.ConfigStore,
		schedule.JobConfigVar{
			Key:   "collatable_only",
			Label: "Only collatable indexes",
			Description: "Check only indexes whose ordering depends on a collation (text, varchar, citext). " +
				"An index on integers or timestamps cannot be damaged by a collation change, and skipping " +
				"them cuts the sweep to roughly a third. Turn OFF for a full audit after a hardware fault, " +
				"disk error, or unclean shutdown, where any index can be damaged.",
			Type:    schedule.JobConfigBool,
			Default: "true",
		},
		schedule.JobConfigVar{
			Key:   "heap_all_indexed",
			Label: "Check heap against index",
			Description: "Verify that every heap row actually appears in the index, rather than only that the " +
				"index is internally consistent. THIS IS THE SETTING THAT MATTERS: a collation-damaged index " +
				"is perfectly well-formed and merely incomplete, so the cheap structural check passes it. " +
				"Turning this off makes runs much faster and makes them unable to find what this job is for.",
			Type:    schedule.JobConfigBool,
			Default: "true",
		},
		schedule.JobConfigVar{
			Key:   "max_table_mb",
			Label: "Max table size (MB)",
			Description: "Skip indexes on tables whose heap exceeds this. Cost scales with the TABLE, not the " +
				"index, because each check reads the whole heap once. 0 = no limit. Raise it together with " +
				"the per-index timeout if the biggest tables are going unchecked.",
			Type:    schedule.JobConfigInt,
			Default: "12000",
		},
		schedule.JobConfigVar{
			Key:   "per_index_timeout_sec",
			Label: "Per-index timeout (seconds)",
			Description: "Cap on a single index check. A timeout is logged as SKIPPED, never as corruption — " +
				"a false alarm on the largest tables is the fastest way to make this job ignorable.",
			Type:    schedule.JobConfigInt,
			Default: "600",
		},
		schedule.JobConfigVar{
			Key:   "max_indexes_per_run",
			Label: "Max indexes per run",
			Description: "Cap on one sweep. Candidates come back smallest-heap-first, so a capped run covers " +
				"as many indexes as it can rather than spending the whole budget on one large table.",
			Type:    schedule.JobConfigInt,
			Default: "200",
		},
		schedule.JobConfigVar{
			Key:   "auto_reindex",
			Label: "Auto-rebuild corrupt indexes",
			Description: "Immediately REINDEX CONCURRENTLY a corrupt NON-unique index. Off by default: " +
				"a corrupt index is evidence, and rebuilding it destroys the evidence before anyone has " +
				"asked why it happened. Unique indexes are never auto-rebuilt regardless — when a broken " +
				"unique index has already let duplicates through, the rebuild cannot succeed until someone " +
				"deduplicates the table.",
			Type:    schedule.JobConfigBool,
			Default: "false",
		},
	)
}

func (p *Plugin) provisionVacuum() {
	p.vacuum = schedule.RegisterJob("Vacuum NZBs",
		"Runs VACUUM FULL on the nzbs table to reclaim disk space. Holds an "+
			"exclusive lock for the duration, so the site is put into "+
			"maintenance mode while it runs. Scheduled weekly; can also be "+
			"triggered manually from the admin jobs page.").MarkOffPeak()
	p.vacuum.IntervalMin = vacuumFullIntervalMin
	p.vacuum.SetTrigger(func() { go p.runVacuum(context.Background()) })
	p.vacuum.DeclareConfig(deps.ConfigStore,
		schedule.JobConfigVar{
			Key:         "timeout_minutes",
			Label:       "Timeout (minutes)",
			Description: "Hard cap on a single VACUUM FULL run. Past this, the Go context cancels the statement and maintenance mode lifts. Bump for very large tables on slow disks.",
			Type:        schedule.JobConfigInt,
			Default:     "30",
		},
		schedule.JobConfigVar{
			Key:         "disk_safety_multiplier",
			Label:       "Free-disk safety multiplier",
			Description: "Pre-flight: refuse to start unless free disk on the working volume is at least nzbs total size × this multiplier. 120 = 1.2× (recommended). Set to 0 to disable the check.",
			Type:        schedule.JobConfigInt,
			Default:     "120",
		},
	)
	// VACUUM FULL is the legacy path — pg_repack does the same reclaim online,
	// so vacuum starts PAUSED (it stays registered as a manual-trigger fallback
	// for environments without pg_repack; an admin resumes it, or pauses
	// pg_repack, to switch). Matches the host's pre-extraction default.
	schedule.PauseJob("Vacuum NZBs")
}
