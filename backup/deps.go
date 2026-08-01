// Package backup is the weekly-backup plugin: one worker job that zips every
// persistent static-asset directory and pg_dumps the database into a dated
// folder, then prunes old runs to a retention count.
//
// Extracted from the host's pkg/services. The host keeps what only it can know
// — which directories are persistent, where backups land, how to reach
// Postgres — and hands them over as Deps; the scheduling comes from
// loon/schedule.
//
// Notably this plugin needs NO per-plugin cooperation: the database half is a
// pg_dump of the whole cluster, so it captures every plugin's tables without
// knowing any of them exist. Only the asset-directory list is host knowledge,
// and that is a slice of strings.
package backup

import (
	"context"
)

// PGConn is the Postgres connection target the pg_dump CLI is invoked against —
// the same values the host connects with. A struct of five fields rather than
// the host's config type: this module has no business importing the site's
// configuration package to read a hostname.
type PGConn struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
}

// ConfigStore is the admin-tunable behaviour, read fresh on every run so a
// change in the admin UI takes effect on the next tick without a restart.
//
// These stay host settings rather than becoming loon/schedule job-config vars:
// they already exist in the host's admin surface, and moving them would be a
// migration of live operator settings, not an extraction. The host's
// SettingsService satisfies this as-is.
type ConfigStore interface {
	// GetBackupDBExcludeTableData names tables whose ROWS the dump skips while
	// still dumping their schema — request logs, caches, job history. On this
	// install that is ~3.9 GB off every dump at zero correctness risk, which
	// is a better saving than any incremental scheme could offer and cannot be
	// wrong in the way one could.
	//
	// A setting rather than a Go constant on purpose: which tables are
	// disposable is an operational judgement that changes as the schema does,
	// and it must be answerable without a deploy.
	GetBackupDBExcludeTableData(ctx context.Context) []string
	// GetBackupDBKeep returns how many published dumps stay on PRODUCTION's
	// disk. Not a backup-retention knob — the array holds the real history —
	// just how much local headroom the dump is allowed. <= 0 means the
	// built-in default.
	GetBackupDBKeep(ctx context.Context) int
}

// Deps are the host-provided seams, staged before core.Boot in the worker.
type Deps struct {
	// DB is the pg_dump connection target.
	DB PGConn
	// Config backs the admin-editable mode + retention.
	Config ConfigStore
	// FreeDisk returns free bytes on the volume holding DBDumpDir, and DBSize
	// the database's on-disk size. Together they are the pre-flight: this job
	// writes a full copy of everything it protects onto local disk, and
	// without headroom it fills the volume and takes the SITE down with it —
	// disk-full is not a backup failure, it is an outage. Both are host
	// closures so this module needn't pull gopsutil or a DB driver.
	//
	// Fail-soft on error (skip the run, don't guess): a backup not taken is
	// recoverable, a full disk is not.
	FreeDisk func(ctx context.Context) (int64, error)
	DBSize   func(ctx context.Context) (int64, error)
	// Classes are the asset directories, with their ordering and whether each
	// can be regenerated. Only the host knows which directories are persistent.
	//
	// This drives BOTH the index and the archive. It used to be two fields —
	// a bare StaticDirs []string for the archive and this one for the index —
	// which meant the archive could not tell a 116 GB regenerable class from
	// 7 MB of irreplaceable artwork, and so had to treat them alike.
	Classes []AssetClass
	// Root is the directory the class paths are relative to. Empty means the
	// process working directory, which is what production uses; tests set it.
	Root string
	// DBDumpDir is where `pg_dump -Fd` writes its dated directories.
	//
	// Empty means the database is NOT in the backup — the job says so on every
	// run rather than failing, because an install without a dump volume is a
	// configuration, not a fault.
	//
	// It MUST be a bind mount — on the container overlay the dump is wiped
	// on every recreate — and the host MUST
	// also register it as an AssetClass: writing the dump is only half the
	// job, and the half that makes it a backup is the index walking it into a
	// generation like any other file. A dump directory that is not a class is
	// a 20 GB file nobody ever fetches.
	DBDumpDir string
}

var deps *Deps

// SetDeps stages the host seams. Call once, in the worker process, before
// core.Boot.
func SetDeps(d Deps) { deps = &d }
