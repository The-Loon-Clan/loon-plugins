package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The database dump, written as ORDINARY FILES into an ordinary asset class.
//
// That is the whole idea. The pull pipeline already indexes files, hashes them,
// packs them, serves them with Range and verifies them on arrival; a database
// that arrives as a special case would need a second copy of every one of those
// behaviours. `pg_dump -Fd` emits a directory of per-table files, so the dump
// simply becomes another class the index walks — per-file hashes, per-file
// resume, and the same manifest, for no new transport code at all.
//
// Why a full dump and not an incremental: over 31 days `nzbs` took 588k inserts
// against 193.6 MILLION updates, and it has no updated_at column — its
// timestamps are created_at/deleted_at/posted_at/hidden_at/last_health_check_at.
// A row-id incremental therefore cannot see updates AT ALL: it would restore a
// database missing every edit and resurrecting every junk-cleanup deletion. The
// same arithmetic (~377M tuple rewrites a month) kills WAL archiving, which
// would ship four dumps' worth of WAL with a far more fragile restore.
//
// Format `-Fd -j N` is the only format with parallel dump AND parallel restore,
// and restore is plain `pg_restore -j N` — zero bespoke code on the path that
// only ever runs under duress.

// dumpDirLayout: each run publishes an immutable dated directory.
//
//	<DBDumpDir>/20260731T014500Z/{toc.dat,1234.dat.gz,...}
//	<DBDumpDir>/.incoming-<stamp>/   (in flight)
//
// The in-flight directory is dot-prefixed deliberately: the index walk skips
// dot-directories, so a half-written dump is invisible to it and cannot be
// hashed, packed and served as though it were a backup.
//
// Immutability is what makes the dump safe to pack. Packs are streamed from the
// live files at pull time, so a member that changes between indexing and
// transfer breaks its pack — and a dump written repeatedly to one path would do
// exactly that, every week, to the largest class in the backup.
const (
	dumpStampFormat = "20060102T150405Z"
	dumpIncoming    = ".incoming-"
)

// pgIdentifier bounds what may reach pg_dump's argv from an admin setting.
// Schema-qualified table names only; anything else is dropped with a log line
// rather than passed through. The setting is admin-only and pg_dump is not a
// shell, so this is defence in depth, not the primary control.
var pgIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$`)

func (p *Plugin) runDBDump(ctx context.Context) {
	if !p.dumpMu.TryLock() {
		p.dumpJob.Log("dump already running — skipping overlap")
		return
	}
	defer p.dumpMu.Unlock()
	p.dumpJob.SetRunning()

	if deps.DBDumpDir == "" {
		// Not configured is not an error: an install that has not wired the
		// dump directory simply has no database class. Say so once per run
		// rather than failing the job red forever.
		p.dumpJob.Log("no DBDumpDir configured — the database is NOT in the backup; wire Deps.DBDumpDir to a bind mount")
		p.dumpJob.SetIdle(time.Now().Add(time.Duration(dbDumpIntervalMin) * time.Minute))
		return
	}
	if err := os.MkdirAll(deps.DBDumpDir, 0o755); err != nil {
		p.dumpFailed(fmt.Errorf("create dump dir: %w", err))
		return
	}

	// Pre-flight. A dump that fills prod's disk is an OUTAGE, not a failed
	// backup, and the peak is two dumps: the new one is written alongside the
	// previous so a failure never leaves us with neither. pg_dump -Fd
	// compresses per table, so the on-disk database size is a deliberately
	// conservative estimate of what one dump costs.
	if deps.FreeDisk != nil && deps.DBSize != nil {
		free, ferr := deps.FreeDisk(ctx)
		size, serr := deps.DBSize(ctx)
		if ferr != nil || serr != nil {
			// Fail-soft: skip rather than guess. A backup not taken is
			// recoverable; a full disk is not.
			p.dumpJob.Log("skipping: cannot read free disk / database size (%v / %v)", ferr, serr)
			p.dumpJob.SetIdle(time.Now().Add(time.Duration(dbDumpIntervalMin) * time.Minute))
			return
		}
		if free < size {
			p.dumpJob.Log("REFUSING to dump: %s free, database is %s. "+
				"The dump is written beside the previous one, so plan for two. "+
				"Free space, lower backup_db_keep, or add exclusions (backup_db_exclude_table_data).",
				humanBytes(free), humanBytes(size))
			p.dumpJob.SetError("insufficient disk for a database dump")
			return
		}
	}

	stamp := time.Now().UTC().Format(dumpStampFormat)
	incoming := filepath.Join(deps.DBDumpDir, dumpIncoming+stamp)
	final := filepath.Join(deps.DBDumpDir, stamp)
	// A leftover incoming directory is a crashed earlier run. pg_dump refuses a
	// non-empty target, so clear it rather than failing every run after a crash.
	_ = os.RemoveAll(incoming)

	excl := p.dumpExclusions(ctx)
	started := time.Now()
	p.dumpJob.SetProgress("Dumping database…")
	if err := dumpDBDirectory(ctx, deps.DB, incoming, excl, dbDumpJobs); err != nil {
		_ = os.RemoveAll(incoming)
		p.dumpFailed(fmt.Errorf("pg_dump: %w", err))
		return
	}

	// Publish atomically. Until this rename the directory is dot-prefixed and
	// therefore invisible to the index walk, so a half-written dump can never
	// be hashed, packed, and served as if it were a backup.
	if err := os.Rename(incoming, final); err != nil {
		_ = os.RemoveAll(incoming)
		p.dumpFailed(fmt.Errorf("publish dump: %w", err))
		return
	}

	// The manifest is what a restore is CHECKED against. Written after the
	// rename so it describes a published dump, and best-effort: failing the
	// whole dump because its self-description could not be written would be
	// the tail wagging the dog.
	if err := p.writeDumpManifest(ctx, final, stamp, excl); err != nil {
		p.dumpJob.Log("dump manifest not written (%v) — the dump is fine, but a restore "+
			"drill has nothing to check completeness against", err)
		if p.core != nil && p.core.Errors != nil {
			p.core.Errors.Report(ctx, "backup/db-dump-manifest", err)
		}
	}

	files, bytes := dirTotals(final)
	p.dumpJob.Log("dumped %s in %d file(s) to %s in %s%s",
		humanBytes(bytes), files, stamp, time.Since(started).Round(time.Second),
		exclusionNote(excl))

	if pruned := p.pruneDumps(ctx); pruned > 0 {
		p.dumpJob.Log("pruned %d older dump(s)", pruned)
	}
	// The dump is not BACKED UP until the index walks it, packs it, and the
	// puller fetches it. Say so, because "dump succeeded" reads as "safe".
	p.dumpJob.Log("dump published — it becomes a backup once the index seals a generation and the puller fetches it")
	p.dumpJob.SetIdle(time.Now().Add(time.Duration(dbDumpIntervalMin) * time.Minute))
}

// dumpManifest travels inside the dump directory, so it is packed, pulled and
// restored with the data it describes rather than living in a database the
// reader may not have.
type dumpManifest struct {
	Stamp         string      `json:"stamp"`
	Database      string      `json:"database"`
	ServerVersion string      `json:"server_version"`
	Format        string      `json:"format"`
	ParallelJobs  int         `json:"parallel_jobs"`
	DataExcluded  []string    `json:"data_excluded"`
	Tables        []tableStat `json:"tables"`
	// RestoreWith is here for the human who finds this directory in five years
	// with no access to any of this repository.
	RestoreWith string `json:"restore_with"`
}

// dumpManifestName is read by indexer-tools/backup_drill.py. Renaming it breaks
// the drill, which is the only thing that ever proves these dumps restore.
const dumpManifestName = "dump-manifest.json"

func (p *Plugin) writeDumpManifest(ctx context.Context, dir, stamp string, excl []string) error {
	m := dumpManifest{
		Stamp:        stamp,
		Database:     deps.DB.DBName,
		Format:       "directory (pg_dump -Fd)",
		ParallelJobs: dbDumpJobs,
		DataExcluded: excl,
		RestoreWith:  "pg_restore -j 4 --no-owner --no-privileges -d <target> <this directory>",
	}
	if v, err := p.st.serverVersion(ctx); err == nil {
		m.ServerVersion = v
	}
	// Row estimates are the point of the file: a restored database whose nzbs
	// table holds 400k rows against a recorded 430k is fine, and one holding
	// zero is a catastrophe wearing a successful exit code.
	stats, err := p.st.tableStats(ctx)
	if err != nil {
		return fmt.Errorf("table stats: %w", err)
	}
	m.Tables = stats

	blob, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	// Temp + rename inside the published directory: a half-written manifest
	// would be indexed and packed exactly like a good one.
	tmp := filepath.Join(dir, "."+dumpManifestName+".tmp")
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, dumpManifestName))
}

func (p *Plugin) dumpFailed(err error) {
	p.dumpJob.SetError(err.Error())
	p.dumpJob.Log("ERROR: %v", err)
	if p.core != nil && p.core.Errors != nil {
		p.core.Errors.Report(context.Background(), "backup/db-dump", err)
	}
}

// dumpExclusions reads the admin-set exclusion list and drops anything that is
// not a plain table identifier.
//
// These tables are ~3.9 GB of the dump and none of them is data a restore
// needs: request logs, a metadata CACHE, job history, advisory locks, search
// terms. Their SCHEMA is still dumped — only the rows are skipped — so a
// restore produces a working database, not one missing tables.
func (p *Plugin) dumpExclusions(ctx context.Context) []string {
	if deps.Config == nil {
		return nil
	}
	var out []string
	for _, t := range deps.Config.GetBackupDBExcludeTableData(ctx) {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !pgIdentifier.MatchString(t) {
			p.dumpJob.Log("ignoring exclusion %q — not a table identifier", t)
			continue
		}
		out = append(out, t)
	}
	return out
}

func exclusionNote(excl []string) string {
	if len(excl) == 0 {
		return ""
	}
	return fmt.Sprintf(" (data excluded for %d table(s), schema retained)", len(excl))
}

// pruneDumps keeps the newest N published dumps.
//
// Retention here is about PROD's disk, not about the backup's: once the puller
// has fetched a dump it lives on the array, and the array's own retention is a
// separate decision with its own time floor. Keeping more than a couple of
// dumps on the production box protects nothing that the array does not already
// protect better.
func (p *Plugin) pruneDumps(ctx context.Context) int {
	keep := 2
	if deps.Config != nil {
		if n := deps.Config.GetBackupDBKeep(ctx); n > 0 {
			keep = n
		}
	}
	dumps := publishedDumps(deps.DBDumpDir)
	if len(dumps) <= keep {
		return 0
	}
	pruned := 0
	// Newest first, so the tail is the oldest.
	for _, name := range dumps[keep:] {
		if err := os.RemoveAll(filepath.Join(deps.DBDumpDir, name)); err != nil {
			p.dumpJob.Log("could not prune %s: %v", name, err)
			continue
		}
		pruned++
	}
	return pruned
}

// publishedDumps lists published dump directories, newest first. Incoming
// directories are excluded by name — they are not dumps yet.
func publishedDumps(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), dumpIncoming) {
			continue
		}
		if _, err := time.Parse(dumpStampFormat, e.Name()); err != nil {
			continue // not ours; never delete what we did not create
		}
		out = append(out, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out))) // stamp format sorts lexically
	return out
}

// dumpDBDirectory runs pg_dump in directory format.
//
// Directory format rather than the previous plain-SQL-through-gzip: -Fp would
// hex-encode ~21 GB of bytea into ~42 GB of text, and it cannot restore in
// parallel. (BACKUP-DESIGN.md's -Fp recommendation was correct for restic,
// whose deduplication wants uncompressed text; with no dedup engine it
// inverts.) Each table becomes its own file, which is also what lets the pack
// pipeline hash and resume the dump per file instead of as one 20 GB blob.
func dumpDBDirectory(ctx context.Context, conn PGConn, dest string, excludeData []string, jobs int) error {
	if jobs < 1 {
		jobs = 1
	}
	args := []string{
		"-h", conn.Host,
		"-p", fmt.Sprintf("%d", conn.Port),
		"-U", conn.User,
		"-d", conn.DBName,
		"--no-password",
		"-Fd",
		"-j", fmt.Sprintf("%d", jobs),
		"-f", dest,
	}
	for _, t := range excludeData {
		args = append(args, "--exclude-table-data="+t)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	// PGPASSWORD rather than an argv flag: argv is world-readable via /proc.
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password)
	// pg_dump's diagnostics are the only explanation of a failure, and they go
	// to stderr. Capturing them bounded keeps a real message in the job log
	// without letting a pathological run flood it.
	var stderr boundedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// dirTotals sums a published dump for the log line.
func dirTotals(dir string) (files int, bytes int64) {
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || fi.IsDir() {
			return nil
		}
		files++
		bytes += fi.Size()
		return nil
	})
	return files, bytes
}

// boundedBuffer keeps the FIRST n bytes written to it. First, not last: the
// first pg_dump error is the cause and everything after it is consequence.
type boundedBuffer struct {
	b   []byte
	max int
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if w.max == 0 {
		w.max = 4096
	}
	if room := w.max - len(w.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.b = append(w.b, p[:room]...)
	}
	return len(p), nil // never fail the command on a full buffer
}

func (w *boundedBuffer) String() string { return string(w.b) }
