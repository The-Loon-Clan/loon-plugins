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

// ── Verify indexes (amcheck bt_index_check) ──────────────────────────────────

// runVerify reads every eligible btree index against its heap and reports the
// ones that disagree.
//
// This job exists because index corruption is SILENT. A glibc or ICU upgrade
// changes how text sorts; every existing text index is then subtly mis-ordered,
// but stays internally consistent — so it passes a structural check, raises no
// error, and simply stops finding rows. On a UNIQUE index the damage eventually
// announces itself, badly: ON CONFLICT consults the index, misses the duplicate
// it should have found, and the importer inserts it again. On a NON-unique
// index nothing ever announces it at all; queries just quietly return less than
// they should.
//
// Both happened here. The unique case surfaced as duplicate usernames and
// banned_paths, which is what prompted looking; the sweep that followed found
// two more non-unique indexes that had been returning wrong results with
// nothing anywhere reporting a problem. Nothing in the system would have
// noticed either, which is the argument for running this on a schedule rather
// than after someone gets suspicious.
func (p *Plugin) runVerify(ctx context.Context) {
	if !p.verifyMu.TryLock() {
		p.verify.Log("Skipped: another run is already in progress")
		return
	}
	defer p.verifyMu.Unlock()
	if p.verify.IsPaused() {
		return
	}
	p.verify.SetRunning()
	start := time.Now()

	// Pre-flight: amcheck ships with Postgres but is not installed by default.
	installed, err := deps.Diag.IsPGExtensionInstalled(ctx, "amcheck")
	if err != nil {
		p.verify.Log("WARN: couldn't check pg_extension: %v", err)
	}
	if !installed {
		p.verify.Log("amcheck extension not installed — run `CREATE EXTENSION amcheck;` in the target database to enable verification.")
		p.verify.SetIdle(time.Now().Add(time.Duration(verifyIntervalMin) * time.Minute))
		return
	}

	collatableOnly := p.verify.GetConfigBool("collatable_only")
	heapAllIndexed := p.verify.GetConfigBool("heap_all_indexed")
	autoReindex := p.verify.GetConfigBool("auto_reindex")

	maxTableMB := p.verify.GetConfigInt("max_table_mb")
	maxHeapBytes := int64(maxTableMB) * 1024 * 1024

	maxN := p.verify.GetConfigInt("max_indexes_per_run")
	if maxN <= 0 {
		maxN = 200
	}
	perIndexSec := p.verify.GetConfigInt("per_index_timeout_sec")
	if perIndexSec <= 0 {
		perIndexSec = 600
	}

	candidates, err := deps.Diag.ListBtreeIndexes(ctx, collatableOnly, maxHeapBytes, maxN)
	if err != nil {
		p.verify.SetError(fmt.Sprintf("list indexes: %v", err))
		return
	}
	if len(candidates) == 0 {
		p.verify.Log("No eligible indexes (collatable_only=%v, max_table_mb=%d)", collatableOnly, maxTableMB)
		p.verify.SetIdle(time.Now().Add(time.Duration(verifyIntervalMin) * time.Minute))
		return
	}

	p.verify.Log("Verifying %d index(es); collatable_only=%v heap_all_indexed=%v max_table=%dMB",
		len(candidates), collatableOnly, heapAllIndexed, maxTableMB)
	if !heapAllIndexed {
		// Worth saying out loud: the cheap mode cannot find the thing this job
		// was built for. Someone who turned it off to speed up a run should not
		// then read a clean result as "no collation damage".
		p.verify.Log("NOTE heap_all_indexed is off — structural check only, which does NOT detect collation damage")
	}

	var corrupt []string
	var skipped int
	for i, c := range candidates {
		p.verify.SetProgress("[%d/%d] bt_index_check %s (%s heap)",
			i+1, len(candidates), c.IndexName, humanBytes(c.HeapBytes))

		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(perIndexSec)*time.Second)
		err := deps.Diag.VerifyIndex(checkCtx, c.IndexName, heapAllIndexed)
		cancel()

		switch classifyVerifyError(err, ctx.Err()) {
		case verifyClean:
			continue
		case verifyAborted:
			p.verify.Log("Stopping: %v", ctx.Err())
			return
		case verifySkipped:
			skipped++
			p.verify.Log("SKIP %s: check exceeded %ds (table heap %s) — raise per_index_timeout_sec to cover it",
				c.IndexName, perIndexSec, humanBytes(c.HeapBytes))
			continue
		}

		kind := "non-unique"
		if c.IsUnique {
			kind = "UNIQUE"
		}
		corrupt = append(corrupt, c.IndexName)
		p.verify.Log("CORRUPT %s on %s (%s): %v", c.IndexName, c.TableName, kind, err)

		if autoReindex {
			// Deliberately opt-in. Rebuilding a corrupt NON-unique index is
			// safe and fixes silently-wrong results immediately. Rebuilding a
			// corrupt UNIQUE one fails outright when the table already holds
			// the duplicates the broken index let through — which is a real
			// finding, but one that needs a human to dedupe before any rebuild
			// can succeed.
			if c.IsUnique {
				p.verify.Log("  not auto-reindexing %s: a unique index whose table may already hold duplicates must be deduplicated first", c.IndexName)
			} else if rerr := deps.Diag.ReindexIndexConcurrently(ctx, c.IndexName); rerr != nil {
				p.verify.Log("  auto-reindex of %s failed: %v", c.IndexName, rerr)
			} else {
				p.verify.Log("  auto-reindexed %s", c.IndexName)
			}
		}
	}

	dur := time.Since(start)
	if len(corrupt) > 0 {
		// Must outlive the in-memory log ring, which every deploy clears —
		// the same lesson the reindex job learned the hard way.
		err := fmt.Errorf("amcheck found %d corrupt index(es): %s — rebuild with REINDEX INDEX CONCURRENTLY (dedupe first if unique)",
			len(corrupt), strings.Join(corrupt, ", "))
		p.verify.SetError(err.Error())
		if p.core != nil && p.core.Errors != nil {
			p.core.Errors.Report(ctx, "dbmaint/verify-indexes", err)
		}
	} else {
		p.verify.Log("All %d index(es) verified clean in %s (%d skipped)",
			len(candidates)-skipped, dur.Round(time.Second), skipped)
	}

	if err := deps.StatCache.SetStatCache(ctx, verifyStateKey, int64(dur.Seconds()), ""); err != nil {
		p.verify.Log("Failed to persist run duration: %v", err)
	}
	p.verify.SetIdle(time.Now().Add(time.Duration(verifyIntervalMin) * time.Minute))
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

// verifyOutcome is what one bt_index_check result means.
type verifyOutcome int

const (
	verifyClean verifyOutcome = iota
	// verifyCorrupt: the index genuinely disagrees with its table.
	verifyCorrupt
	// verifySkipped: ran out of time. NOT a finding.
	verifySkipped
	// verifyAborted: the process is shutting down.
	verifyAborted
)

// classifyVerifyError decides whether an error from bt_index_check is a
// finding, a timeout, or a shutdown.
//
// The distinction carries the whole credibility of the job. bt_index_check
// signals corruption by RAISING, so "the call returned an error" is the normal
// success path for a bad index — which makes it dangerously easy to also report
// every timeout and every cancelled context as corruption. Those would land
// disproportionately on the biggest, busiest tables, and a corruption alert
// that cries wolf about `nzbs` every week is one nobody reads by the time it is
// telling the truth.
//
// parentErr is the sweep-level context error, checked FIRST: on shutdown the
// per-index context is cancelled too, so without this the final index of every
// deploy-time run would be reported as damaged.
func classifyVerifyError(err, parentErr error) verifyOutcome {
	if err == nil {
		return verifyClean
	}
	if parentErr != nil {
		return verifyAborted
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return verifySkipped
	}
	return verifyCorrupt
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
