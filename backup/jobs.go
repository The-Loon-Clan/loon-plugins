package backup

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stampFormat is both the dated folder name and the last-run record. It sorts
// lexically = chronologically, which prune relies on.
const stampFormat = "2006-01-02_150405"

// run is the scheduled path: skip if a recent backup already exists.
func (p *Plugin) run(ctx context.Context) { p.doRun(ctx, false) }

// runForced is the /admin/jobs trigger: always back up. An operator pressing
// the button means "now", not "now unless you'd rather not".
func (p *Plugin) runForced(ctx context.Context) { p.doRun(ctx, true) }

func (p *Plugin) doRun(ctx context.Context, force bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	job := p.job
	job.SetRunning()

	// failures decides idle-vs-error at the end, and it has to be a deferred
	// decision rather than a SetError at the failing line, because SetIdle
	// CLEARS LastError (loon/schedule/registry.go:237-238). This function used
	// to `defer job.SetIdle(...)` unconditionally, so any SetError the body
	// performed was wiped on the way out and every partial failure reported as
	// a clean run.
	var failures []string
	defer func() {
		if len(failures) == 0 {
			job.SetIdle(time.Now().Add(backupIntervalMin * time.Minute))
			return
		}
		job.SetError(fmt.Sprintf("%d part(s) failed: %s", len(failures), strings.Join(failures, ", ")))
	}()

	// The dated folders ARE the last-run record, so no new state is needed to
	// answer "did we back up recently?".
	//
	// This guard is what makes the boot delay safe, and the boot delay is what
	// makes the job work at all. The host service this replaces ran
	// `for { sleep(1 week); run() }` — it never ran at boot, and every restart
	// reset the week, so on any box redeployed more often than weekly the
	// backup simply never happened. Not failed: never started. A boot delay
	// alone would swing to the opposite failure (a full pg_dump + asset zip an
	// hour after every restart), so pair it with this.
	if !force {
		if age, ok := p.newestBackupAge(); ok && age < backupIntervalMin*time.Minute {
			job.Log("Skipped: last backup was %s ago (interval %s) — nothing due",
				age.Round(time.Minute), (backupIntervalMin * time.Minute).String())
			return
		}
	}

	// Free-disk pre-flight. This job writes a full copy of everything it
	// protects onto local disk before anything else can happen — so on a
	// volume without room it does not merely fail, it fills the disk and the
	// SITE starts erroring. That is an outage caused by the backup, which is
	// the worst possible trade: it protects nothing and breaks the thing it
	// was protecting.
	//
	// The estimate is deliberately an upper bound (the zips and the gzipped
	// dump both come out smaller than their sources), so we err toward
	// skipping a backup that would have fit. A backup not taken is
	// recoverable; a full disk on the app server is not.
	//
	// Note this gate is expected to REFUSE on a box that has no room for a
	// full copy — which is the honest answer, and is exactly the condition
	// BACKUP-DESIGN.md's restic rework removes by streaming off-box instead
	// of staging locally. Skipping loudly is the interim behaviour, not a bug.
	if !p.preflightOK(ctx, force) {
		return
	}

	stamp := time.Now().Format(stampFormat)
	dest := filepath.Join(deps.BackupDir, stamp)
	job.Log("Creating backup directory: %s", dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		job.Log("ERROR creating directory: %v", err)
		failures = append(failures, "backup directory")
		return
	}

	mode := deps.Config.GetBackupMode(ctx)

	// ── Static assets ────────────────────────────────────────────────────────
	// One zip per source directory, named after the basename (covers ->
	// covers.zip) so a partial restore can target a single asset class. Skipped
	// in db_only mode — the asset dirs are the disk hog and are re-fetchable, so
	// a db_only run is just the DB.
	if mode == "db_only" {
		job.Log("db_only mode: skipping static-asset zips")
	} else {
		for _, src := range deps.StaticDirs {
			base := filepath.Base(src)
			zipOut := filepath.Join(dest, base+".zip")
			job.SetProgress("Zipping %s…", base)
			if err := zipDir(src, zipOut, job.Log); err != nil {
				job.Log("ERROR zipping %s: %v", base, err)
				failures = append(failures, base+".zip")
				continue
			}
			fi, _ := os.Stat(zipOut)
			job.Log("%s.zip written (%s)", base, fmtFileSize(fi))
		}
	}

	// ── Database dump ────────────────────────────────────────────────────────
	// pg_dump takes the whole database, so every plugin's tables are captured
	// without this plugin knowing any of them exist.
	job.SetProgress("Dumping database…")
	dbOut := filepath.Join(dest, "database.sql.gz")
	if err := dumpDB(deps.DB, dbOut); err != nil {
		job.Log("ERROR dumping database: %v", err)
		failures = append(failures, "database.sql.gz")
	} else {
		fi, _ := os.Stat(dbOut)
		job.Log("database.sql.gz written (%s)", fmtFileSize(fi))
	}

	// Retention: prune the oldest dated folders so backups don't accumulate
	// forever (the original disk-space leak — there was no cleanup). Runs even
	// on a failed pass, so an incomplete folder is still subject to retention
	// and cannot leak disk.
	p.prune(job.Log, deps.Config.GetBackupKeepCount(ctx))

	if len(failures) > 0 {
		job.Log("INCOMPLETE → %s — %d part(s) failed: %s. No completion marker written, so "+
			"the next scheduled run will retry instead of counting this folder as a backup.",
			dest, len(failures), strings.Join(failures, ", "))
		return
	}

	// The marker is written last, and only here. Its absence is what stops a
	// failed run from being mistaken for a good one — see completeMarker.
	if err := os.WriteFile(filepath.Join(dest, completeMarker),
		[]byte(fmt.Sprintf("stamp=%s\nmode=%s\n", stamp, mode)), 0o644); err != nil {
		job.Log("ERROR writing the completion marker: %v", err)
		failures = append(failures, completeMarker)
		return
	}
	job.Log("Backup complete → %s", dest)
}

// preflightOK reports whether there is room to write this backup without
// filling the volume. false = skip (already logged).
//
// force (a manual /admin/jobs trigger) overrides the last-run guard but NOT
// this: an operator asking for a backup is not asking for an outage, and they
// cannot see the free-space arithmetic from the button.
func (p *Plugin) preflightOK(ctx context.Context, force bool) bool {
	job := p.job
	if deps.FreeDisk == nil || deps.DBSize == nil {
		job.Log("WARN no disk pre-flight wired — proceeding blind")
		return true
	}

	var need int64
	if deps.Config.GetBackupMode(ctx) != "db_only" {
		for _, src := range deps.StaticDirs {
			sz, err := dirSize(src)
			if err != nil {
				job.Log("WARN couldn't size %s for the disk pre-flight: %v", src, err)
				continue
			}
			need += sz
		}
	}
	dbBytes, err := deps.DBSize(ctx)
	if err != nil {
		job.Log("ERROR couldn't query database size for the disk pre-flight: %v — skipping this run rather than risk filling the disk", err)
		return false
	}
	need += dbBytes

	// 120%: the same shape as dbmaint's vacuum multiplier — headroom so we
	// don't land on exactly zero free.
	need = need * 120 / 100

	free, err := deps.FreeDisk(ctx)
	if err != nil {
		job.Log("ERROR couldn't read free disk for the pre-flight: %v — skipping this run rather than risk filling the disk", err)
		return false
	}
	if free < need {
		job.Log("SKIPPED: need ~%s free to stage a full backup (assets + database, +20%% headroom), only %s available on the backup volume. "+
			"Refusing rather than filling the disk and taking the site down. "+
			"This box cannot hold a full local copy — see BACKUP-DESIGN.md (restic streams off-box instead of staging here).",
			humanBytes(need), humanBytes(free))
		if force {
			job.Log("(manual trigger does not override the disk pre-flight)")
		}
		return false
	}
	return true
}

// dirSize sums the regular files under root. Missing dirs contribute 0 rather
// than erroring — a not-yet-created asset dir is not a reason to skip.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// completeMarker is written into a dated run folder only when every part of
// that run succeeded, and its presence is what makes the folder count as a
// backup.
//
// Without it the job could not tell a good run from a bad one, and the damage
// was not cosmetic. A run whose pg_dump failed still created the folder, still
// logged "Backup complete", and newestBackupAge then read that folder as a
// successful backup — so the skip guard suppressed the next SEVEN DAYS of
// scheduled attempts. A failure bought itself a week of silence, and the one
// artifact that proved it had failed was the same artifact that silenced it.
//
// One deploy-time effect worth knowing: folders written before this existed
// carry no marker, so the first run after upgrading treats the history as
// empty and considers a backup due. The disk pre-flight still gates it.
const completeMarker = "COMPLETE"

// newestBackupAge reports how long ago the most recent COMPLETED run folder was
// written. ok=false means there are none (or the dir is unreadable), which must
// be treated as "never backed up" — i.e. do run.
func (p *Plugin) newestBackupAge() (time.Duration, bool) {
	entries, err := os.ReadDir(deps.BackupDir)
	if err != nil {
		return 0, false
	}
	var newest time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// An unmarked folder is a failed or half-written run. Skipping it here
		// is the whole point: it means a broken backup cannot satisfy the
		// "did we back up recently?" question it has no business answering.
		if _, serr := os.Stat(filepath.Join(deps.BackupDir, e.Name(), completeMarker)); serr != nil {
			continue
		}
		// ParseInLocation, not Parse: stampFormat carries no zone, so Format
		// writes local time while Parse would read it back as UTC. The original
		// code never noticed because it only parsed to validate the name shape
		// and then sorted lexically — this is the first code to compare the
		// stamp against now, and a plain Parse skews the age by the local
		// offset (measured: 9h where 2h was true, at UTC-7). East of Greenwich
		// the sign flips and a due backup gets skipped.
		t, perr := time.ParseInLocation(stampFormat, e.Name(), time.Local)
		if perr != nil {
			continue // not one of ours
		}
		if t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return 0, false
	}
	return time.Since(newest), true
}

// prune deletes the oldest dated backup folders, keeping the most recent
// `keep` (0 = keep all / pruning disabled). Only folders whose name parses as
// the backup stamp are touched, so unrelated files in BackupDir are safe.
func (p *Plugin) prune(logf func(string, ...any), keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(deps.BackupDir)
	if err != nil {
		logf("prune: read backup dir: %v", err)
		return
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, perr := time.Parse(stampFormat, e.Name()); perr == nil {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return
	}
	sort.Strings(dirs) // stamp sorts lexically = chronologically; oldest first
	for _, d := range dirs[:len(dirs)-keep] {
		if err := os.RemoveAll(filepath.Join(deps.BackupDir, d)); err != nil {
			logf("prune: failed to remove %s: %v", d, err)
		} else {
			logf("Pruned old backup %s", d)
		}
	}
}

// zipDir walks src and writes every file into a zip archive at dest.
func zipDir(src, dest string, logf func(string, ...any)) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return fmt.Errorf("source directory does not exist: %s", src)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	count := 0
	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		r, err := os.Open(path)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		count++
		return err
	})
	if err != nil {
		return err
	}
	logf("Zipped %d files from %s", count, src)
	return nil
}

// dumpDB runs pg_dump and pipes the output through gzip into dest.
func dumpDB(conn PGConn, dest string) error {
	args := []string{
		"-h", conn.Host,
		"-p", fmt.Sprintf("%d", conn.Port),
		"-U", conn.User,
		"-d", conn.DBName,
		"--no-password",
	}
	cmd := exec.Command("pg_dump", args...)
	// PGPASSWORD rather than an argv flag: argv is world-readable via /proc.
	cmd.Env = append(os.Environ(), "PGPASSWORD="+conn.Password)

	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw

	f, err := os.Create(dest)
	if err != nil {
		pw.Close()
		pr.Close()
		return err
	}

	gz := gzip.NewWriter(f)

	// Copy pg_dump stdout → gzip → file in a goroutine.
	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(gz, pr)
		pr.Close()
		gz.Close()
		f.Close()
		done <- copyErr
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("pg_dump not found or failed to start: %w", err)
	}

	cmdErr := cmd.Wait()
	pw.Close() // signal EOF to the copy goroutine
	copyErr := <-done

	if cmdErr != nil {
		return fmt.Errorf("pg_dump: %w", cmdErr)
	}
	return copyErr
}

func fmtFileSize(fi os.FileInfo) string {
	if fi == nil {
		return "?"
	}
	b := fi.Size()
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
