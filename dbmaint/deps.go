// Package dbmaint is the database-maintenance plugin: worker jobs that keep
// Postgres lean — "Repack NZBs (online)" via the pg_repack CLI, "Reindex
// (online)" via REINDEX INDEX CONCURRENTLY, and the legacy "Vacuum NZBs"
// (VACUUM FULL, gated behind maintenance mode; paused by default since
// pg_repack replaced it). Extracted from the host's pkg/services; the heavy
// table ops + disk pre-flight are host-provided seams (Deps), while the
// scheduling + admin-editable config knobs come from loon/schedule.
package dbmaint

import (
	"context"

	"github.com/the-loon-clan/loon/schedule"
)

// IndexUsage is one candidate index for the reindex pass — the plugin's own
// shape so it needn't import the host storage package. The host adapts its
// storage.IndexUsage into this at the injection boundary.
type IndexUsage struct {
	TableName string
	IndexName string
	SizeBytes int64
	Scans     int64
}

// Diagnostics is the DB-introspection seam (a host storage adapter). Three of
// the four methods have primitive signatures the host repo satisfies directly;
// only GetIndexUsage needs a small type conversion on the host side.
type Diagnostics interface {
	GetTableTotalSize(ctx context.Context, table string) (int64, error)
	IsPGExtensionInstalled(ctx context.Context, name string) (bool, error)
	GetIndexUsage(ctx context.Context, limit int) ([]IndexUsage, error)
	ReindexIndexConcurrently(ctx context.Context, indexName string) error
	// DropInvalidIndexes removes the half-built indexes a failed REINDEX
	// CONCURRENTLY leaves behind, returning how many went.
	//
	// Postgres names them <index>_ccnew[N] and marks them invalid, and says
	// plainly that they should be dropped. Nothing was doing it: eighty
	// failed monthly attempts on one index accumulated eighty of them, and
	// because each is 0 bytes the only symptom was a catalogue nobody reads.
	DropInvalidIndexes(ctx context.Context) (int, error)
}

// StatCache persists per-job run durations so the next run shows a real ETA.
type StatCache interface {
	GetStatCache(ctx context.Context, key string) (int64, string, error)
	SetStatCache(ctx context.Context, key string, valueNum int64, valueText string) error
}

// Nzbs is the single table op VACUUM FULL needs (the host owns the nzbs table).
type Nzbs interface {
	VacuumFullNzbs(ctx context.Context) error
}

// Maintenance is the host maintenance-mode gate. VACUUM FULL takes an
// AccessExclusiveLock, so the whole site shows a maintenance page for the
// duration; Begin/End bracket that. The host's middleware.Global satisfies it.
type Maintenance interface {
	Begin(reason string, etaSecs int64)
	End()
}

// RepackConn is the Postgres connection target the pg_repack CLI is invoked
// against — the same values storage.New connects with.
type RepackConn struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
}

// Deps are the host-provided seams, staged before core.Boot in the worker.
type Deps struct {
	Diag        Diagnostics
	StatCache   StatCache
	Nzbs        Nzbs
	Maintenance Maintenance
	// ConfigStore backs the admin-editable job knobs (the host JobRun repo
	// satisfies schedule.JobConfigStore).
	ConfigStore schedule.JobConfigStore
	// FreeDisk returns free bytes on the working volume. The host wraps
	// gopsutil, so this module needn't pull that dependency. Fail-soft: an
	// error skips the disk pre-flight rather than blocking the run.
	FreeDisk func(ctx context.Context) (int64, error)
	// Repack is the CLI connection target for pg_repack.
	Repack RepackConn
}

var deps *Deps

// SetDeps stages the host seams. Call once, in the worker process, before
// core.Boot.
func SetDeps(d Deps) { deps = &d }
