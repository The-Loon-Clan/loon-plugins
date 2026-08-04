package dbmaint

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lib/pq"
)

// ── Repack NZBs (online pg_repack) ───────────────────────────────────────────

func (p *Plugin) runRepack(ctx context.Context) {
	if !p.repackMu.TryLock() {
		p.repack.Log("Skipped: another run is already in progress")
		return
	}
	defer p.repackMu.Unlock()
	if p.repack.IsPaused() {
		return
	}
	p.repack.SetRunning()
	start := time.Now()

	// Pre-flight 1: binary on PATH. Not cached — "pg_repack got installed
	// mid-week" is a real (and good) thing to pick up immediately.
	if err := exec.CommandContext(ctx, "pg_repack", "--version").Run(); err != nil {
		p.repack.Log("pg_repack binary not on PATH — skipping. Install postgresql-NN-repack on the host running the worker to enable online repack.")
		p.repack.SetIdle(time.Now().Add(time.Duration(pgRepackIntervalMin) * time.Minute))
		return
	}

	// Pre-flight 2: extension installed in the target DB.
	installed, err := deps.Diag.IsPGExtensionInstalled(ctx, "pg_repack")
	if err != nil {
		p.repack.Log("WARN: couldn't check pg_extension: %v", err)
	}
	if !installed {
		p.repack.Log("pg_repack extension not installed — run `CREATE EXTENSION pg_repack;` in the target database (superuser required).")
		p.repack.SetIdle(time.Now().Add(time.Duration(pgRepackIntervalMin) * time.Minute))
		return
	}

	tables := splitCSV(p.repack.GetConfigString("tables"))
	if len(tables) == 0 {
		tables = []string{"nzbs"}
	}

	// Pre-flight 3: free disk vs the largest single table (pg_repack does
	// them sequentially, so only one shadow copy exists at a time).
	multiplier := p.repack.GetConfigInt("disk_safety_multiplier")
	var maxBytes int64
	for _, t := range tables {
		if size, err := deps.Diag.GetTableTotalSize(ctx, t); err == nil && size > maxBytes {
			maxBytes = size
		}
	}
	if multiplier > 0 && maxBytes > 0 {
		needBytes := maxBytes * int64(multiplier) / 100
		if free, err := deps.FreeDisk(ctx); err != nil {
			p.repack.Log("WARN couldn't query free disk: %v — skipping pre-flight", err)
		} else if free < needBytes {
			msg := fmt.Sprintf("aborting: largest table is %s, need ~%s free (%d%%), only %s available — free more space and retry",
				humanBytes(maxBytes), humanBytes(needBytes), multiplier, humanBytes(free))
			p.repack.Log("%s", msg)
			p.repack.SetError(msg)
			return
		}
	}

	timeoutMin := p.repack.GetConfigInt("soft_timeout_minutes")
	if timeoutMin <= 0 {
		timeoutMin = 240
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	for _, table := range tables {
		p.repack.SetProgress("Repacking %s...", table)
		p.repack.Log("pg_repack: starting on %s", table)
		if err := p.repackOne(runCtx, table); err != nil {
			p.repack.Log("pg_repack on %s failed: %v", table, err)
			p.repack.SetError(fmt.Sprintf("pg_repack %s failed: %v", table, err))
			return
		}
		p.repack.Log("pg_repack: %s done", table)
	}

	dur := time.Since(start)
	p.repack.Log("All repacks complete in %s", dur.Round(time.Second))
	if err := deps.StatCache.SetStatCache(ctx, pgRepackStateKey, int64(dur.Seconds()), ""); err != nil {
		p.repack.Log("Failed to persist run duration: %v", err)
	}
	p.repack.SetIdle(time.Now().Add(time.Duration(pgRepackIntervalMin) * time.Minute))
}

// repackOne shells out to the pg_repack CLI for a single table:
//
//	-h/-p/-U/-d          connection target
//	-t table             the table to repack
//	-x                   also rebuild indexes (the headline win over VACUUM FULL)
//	--no-superuser-check  we authenticate as the app user (has grants, not superuser)
//
// PGPASSWORD is passed via env so it never appears in the process listing.
func (p *Plugin) repackOne(ctx context.Context, table string) error {
	args := []string{
		"-h", deps.Repack.Host,
		"-p", fmt.Sprintf("%d", deps.Repack.Port),
		"-U", deps.Repack.User,
		"-d", deps.Repack.DBName,
		"-t", table,
		"-x",
		"--no-superuser-check",
	}
	cmd := exec.CommandContext(ctx, "pg_repack", args...)
	cmd.Env = append(cmd.Environ(), "PGPASSWORD="+deps.Repack.Password)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		p.repack.Log("pg_repack output:\n%s", string(out))
	}
	return err
}

// ── Reindex (online REINDEX INDEX CONCURRENTLY) ──────────────────────────────

func (p *Plugin) runReindex(ctx context.Context) {
	if !p.reindexMu.TryLock() {
		p.reindex.Log("Skipped: another run is already in progress")
		return
	}
	defer p.reindexMu.Unlock()
	if p.reindex.IsPaused() {
		return
	}
	p.reindex.SetRunning()
	start := time.Now()

	skip := splitCSV(p.reindex.GetConfigString("skip_tables"))
	skipSet := map[string]bool{}
	for _, t := range skip {
		skipSet[t] = true
	}

	minMB := p.reindex.GetConfigInt("min_size_mb")
	if minMB <= 0 {
		minMB = 5
	}
	minBytes := int64(minMB) * 1024 * 1024

	maxN := p.reindex.GetConfigInt("max_indexes_per_run")
	if maxN <= 0 {
		maxN = 50
	}

	indexes, err := deps.Diag.GetIndexUsage(ctx, 500)
	if err != nil {
		p.reindex.SetError(fmt.Sprintf("list indexes: %v", err))
		return
	}

	type cand struct {
		Table string
		Index string
		Size  int64
	}
	var candidates []cand
	for _, ix := range indexes {
		if ix.SizeBytes < minBytes || skipSet[ix.TableName] {
			continue
		}
		// Never-used indexes are dead weight (an operator decision) or freshly
		// created — either way REINDEXing them is wasted churn.
		if ix.Scans == 0 {
			continue
		}
		candidates = append(candidates, cand{Table: ix.TableName, Index: ix.IndexName, Size: ix.SizeBytes})
		if len(candidates) >= maxN {
			break
		}
	}

	if len(candidates) == 0 {
		p.reindex.Log("No candidates this run (size >= %dMB AND used AND not on skip-list)", minMB)
		p.reindex.SetIdle(time.Now().Add(time.Duration(reindexIntervalMin) * time.Minute))
		return
	}

	p.reindex.Log("Reindexing %d index(es); skip-tables=%v, min=%dMB", len(candidates), skip, minMB)
	var permanent []string
	for i, c := range candidates {
		p.reindex.SetProgress("[%d/%d] REINDEX INDEX CONCURRENTLY %s.%s (%s)",
			i+1, len(candidates), c.Table, c.Index, humanBytes(c.Size))
		if err := deps.Diag.ReindexIndexConcurrently(ctx, c.Index); err != nil {
			// Not all failures are alike, and treating them alike is how this
			// job spent months retrying something that could never succeed.
			//
			// A transient one — a concurrent ALTER, a pg_repack overlap — is
			// worth skipping past and trying again next month. A UNIQUE
			// violation is not: it means the table holds duplicates the index
			// cannot represent, which no amount of waiting fixes. Retried
			// monthly, it failed eighty times, left eighty invalid indexes,
			// and reported it only to an in-memory log ring that every deploy
			// cleared.
			if isPermanentReindexFailure(err) {
				permanent = append(permanent, c.Index)
				p.reindex.Log("REINDEX %s CANNOT SUCCEED: %v — the table has duplicates the unique index cannot hold; dedupe first", c.Index, err)
			} else {
				p.reindex.Log("REINDEX %s failed: %v (continuing)", c.Index, err)
			}
			continue
		}
		p.reindex.Log("[%d/%d] %s done", i+1, len(candidates), c.Index)
	}

	// Sweep the debris. A failed REINDEX CONCURRENTLY leaves an invalid
	// <index>_ccnew behind by design; Postgres expects the operator to drop
	// it, and until now nobody did.
	if n, err := deps.Diag.DropInvalidIndexes(ctx); err != nil {
		p.reindex.Log("could not drop invalid indexes left by failed reindexes: %v", err)
	} else if n > 0 {
		p.reindex.Log("dropped %d invalid index(es) left by failed REINDEX CONCURRENTLY", n)
	}

	// A permanent failure must outlive the log ring. This is the one thing
	// that would have surfaced the problem years earlier.
	if len(permanent) > 0 {
		err := fmt.Errorf("REINDEX cannot succeed on %d index(es) until their tables are deduplicated: %s",
			len(permanent), strings.Join(permanent, ", "))
		p.reindex.SetError(err.Error())
		if p.core != nil && p.core.Errors != nil {
			p.core.Errors.Report(ctx, "dbmaint/reindex", err)
		}
	}

	dur := time.Since(start)
	p.reindex.Log("Reindex pass complete in %s", dur.Round(time.Second))
	if err := deps.StatCache.SetStatCache(ctx, reindexStateKey, int64(dur.Seconds()), ""); err != nil {
		p.reindex.Log("Failed to persist run duration: %v", err)
	}
	p.reindex.SetIdle(time.Now().Add(time.Duration(reindexIntervalMin) * time.Minute))
}

// ── Vacuum NZBs (VACUUM FULL, maintenance-mode gated; paused by default) ─────

func (p *Plugin) runVacuum(ctx context.Context) {
	if !p.vacuumMu.TryLock() {
		p.vacuum.Log("Skipped: another run is already in progress")
		return
	}
	defer p.vacuumMu.Unlock()
	if p.vacuum.IsPaused() {
		return
	}
	p.vacuum.SetRunning()
	start := time.Now()

	// Free-disk pre-flight. VACUUM FULL writes a full second copy before the
	// swap; without headroom it grinds for hours then errors out late.
	multiplier := p.vacuum.GetConfigInt("disk_safety_multiplier")
	if multiplier > 0 {
		tableBytes, err := deps.Diag.GetTableTotalSize(ctx, "nzbs")
		if err != nil {
			p.vacuum.Log("WARN couldn't query nzbs size for disk pre-flight: %v", err)
		} else {
			needBytes := tableBytes * int64(multiplier) / 100
			if free, err := deps.FreeDisk(ctx); err != nil {
				p.vacuum.Log("WARN couldn't query free disk: %v — skipping pre-flight", err)
			} else if free < needBytes {
				msg := fmt.Sprintf("aborting: nzbs is %s, need ~%s free (%d%%), only %s available — free more space and retry",
					humanBytes(tableBytes), humanBytes(needBytes), multiplier, humanBytes(free))
				p.vacuum.Log("%s", msg)
				p.vacuum.SetError(msg)
				return
			} else {
				// Success log only on the genuine ok path (not on a probe
				// error), and report the measured free bytes — matches the
				// pre-extraction service.
				p.vacuum.Log("Pre-flight ok: nzbs is %s, %s free (need %s)", humanBytes(tableBytes), humanBytes(free), humanBytes(needBytes))
			}
		}
	}

	// Best-effort ETA from the previous run for the maintenance-page progress bar.
	prevSecs, _, _ := deps.StatCache.GetStatCache(ctx, vacuumFullStateKey)
	if prevSecs > 0 {
		p.vacuum.Log("Previous run took %ds — using as ETA", prevSecs)
	} else {
		p.vacuum.Log("No previous run on record — ETA unknown")
	}

	// Engage maintenance mode: non-admin traffic now hits /maintenance (503).
	// The deferred End() lifts it on any return path.
	deps.Maintenance.Begin("Reclaiming disk space (VACUUM FULL nzbs)", prevSecs)
	defer deps.Maintenance.End()

	p.vacuum.SetProgress("Maintenance mode engaged, running VACUUM FULL nzbs...")
	p.vacuum.Log("Maintenance mode engaged at %s", start.Format(time.RFC3339))

	timeoutMin := p.vacuum.GetConfigInt("timeout_minutes")
	if timeoutMin <= 0 {
		timeoutMin = 30
	}
	vacCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	if err := deps.Nzbs.VacuumFullNzbs(vacCtx); err != nil {
		p.vacuum.Log("VACUUM FULL nzbs failed: %v", err)
		p.vacuum.SetError(fmt.Sprintf("vacuum full failed: %v", err))
		return
	}

	dur := time.Since(start)
	p.vacuum.Log("VACUUM FULL nzbs done in %s", dur.Round(time.Second))
	if err := deps.StatCache.SetStatCache(ctx, vacuumFullStateKey, int64(dur.Seconds()), ""); err != nil {
		p.vacuum.Log("Failed to persist run duration: %v", err)
	}
	p.vacuum.SetIdle(time.Now().Add(time.Duration(vacuumFullIntervalMin) * time.Minute))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func humanBytes(b int64) string {
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

// isPermanentReindexFailure reports whether retrying is pointless.
//
// 23505 is unique_violation: the table holds rows the unique index cannot
// represent, so the build fails at the same row every time. 23P01 is
// exclusion_violation, which fails for the same structural reason.
//
// Matched on SQLSTATE rather than message text, which is localised and moves
// between releases.
func isPermanentReindexFailure(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505" || pqErr.Code == "23P01"
	}
	// Fall back to the text when the driver error did not survive wrapping.
	// Deliberately narrow: a false positive here downgrades a transient
	// failure into a loud one, which is the harmless direction.
	return strings.Contains(err.Error(), "could not create unique index")
}
