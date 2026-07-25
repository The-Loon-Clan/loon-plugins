package usenet

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// runBackfill closes the GAPS below each group's back_watermark.
//
// Coverage is tracked in newsgroup_ranges, so "what is still missing" is the
// complement of the fetched runs rather than a single downward pointer. That
// matters for throughput: gaps from every group go into one flat batch queue and
// are fetched in PARALLEL across the shared connection pool, where a pointer
// walk can only ever fetch one batch at a time per group.
//
// A pass is bounded by BackfillBatchesPerRun across all groups, so a large
// history doesn't monopolise the pool.
func (p *Plugin) runBackfill(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.backfillMu.TryLock() {
		p.backfillJob.Log("backfill already running — skipping overlap")
		return
	}
	defer p.backfillMu.Unlock()
	p.backfillJob.SetRunning()
	cfg := p.effective(ctx)

	if cfg.SkipBackfill {
		p.backfillJob.Log("backfill disabled (skip_backfill) — new articles only")
		p.backfillJob.SetIdle(p.nextBackfill())
		return
	}

	// Back-pressure: the forward crawl never pauses, but backfill yields when the
	// staging buffer fills faster than the NZB builder drains it. Hysteresis
	// (pause at high-water, resume only below low-water) avoids flapping. What
	// pressure measures is the backend's business — pg: staged rows / cap; redis:
	// used_memory / maxmemory.
	if pr, perr := p.staging.pressure(ctx); perr == nil {
		high := float64(cfg.BackfillPressureHighPct) / 100.0
		low := float64(cfg.BackfillPressureLowPct) / 100.0
		switch {
		case p.backfillPaused && pr >= low:
			p.backfillJob.Log("backfill paused: staging pressure %.0f%% (resumes below %d%%)", pr*100, cfg.BackfillPressureLowPct)
			p.backfillJob.SetIdle(p.nextBackfill())
			return
		case pr >= high:
			p.backfillPaused = true
			p.backfillJob.Log("backfill paused: staging pressure %.0f%% >= %d%% — letting the NZB builder drain", pr*100, cfg.BackfillPressureHighPct)
			p.backfillJob.SetIdle(p.nextBackfill())
			return
		default:
			p.backfillPaused = false
		}
	}

	// Same pools as the forward crawl: providers cap connections per account, so
	// a second set would just push each account over its limit.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.backfillJob.Log("no server configured")
			p.backfillJob.SetIdle(p.nextBackfill())
			return
		}
		p.backfillJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/backfill-fleet", err)
		return
	}

	p.tel.backfill.passStart(len(runs))
	defer p.tel.backfill.passEnd()

	total := 0
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		total += p.backfillProvider(ctx, run, cfg)
	}
	p.backfillJob.Log("backfill complete across %d provider(s): %d historical article(s) staged", len(runs), total)
	p.backfillJob.SetIdle(p.nextBackfill())
	if total > 0 {
		go p.runBuild(ctx)
	}
}

// backfillProvider closes one provider's gaps. Gaps are per-server: another
// provider's coverage says nothing about this one's, because the article numbers
// are not the same articles.
func (p *Plugin) backfillProvider(ctx context.Context, run providerRun, cfg Config) int {
	pool, bb := run.pool, run.prov.backboneKey()
	pool.TopUp(ctx)

	groups, err := p.st.groupsNeedingBackfillForBackbone(ctx, bb, cfg.MaxGroups)
	if err != nil {
		p.reportErr(ctx, "usenet/backfill-groups", err)
		return 0
	}
	if len(groups) == 0 {
		p.backfillJob.Log("%s: nothing to backfill — caught up to the retention horizon", run.prov.label())
		return 0
	}
	// Backfill leases the same (backbone, group) keys as the forward crawl: both
	// advance the same row, so they must not run on it from two workers at once.
	rows := make([]groupRow, len(groups))
	for i, g := range groups {
		rows[i] = groupRow{Name: g.Name}
	}
	// From here the pass runs on the lease context: losing the lease mid-pass
	// cancels it (a sibling may own these groups). jobCtx distinguishes lease
	// loss from shutdown when deciding whether to record coverage below.
	jobCtx := ctx
	heldRows, ctx, release := p.claimGroupLeases(ctx, bb, rows, p.leaseTTL(cfg))
	defer release()
	if len(heldRows) == 0 {
		return 0
	}
	mine := make(map[string]bool, len(heldRows))
	for _, r := range heldRows {
		mine[r.Name] = true
	}
	kept := groups[:0]
	for _, g := range groups {
		if mine[g.Name] {
			kept = append(kept, g)
		}
	}
	groups = kept

	// Build one flat job list from every group's gaps, oldest-work-last, bounded
	// by the shared budget so no single group can consume the whole pass.
	budget := cfg.BackfillBatchesPerRun
	var jobs []batchJob
	targets := make(map[string]backfillRow, len(groups))
	for _, g := range groups {
		if ctx.Err() != nil {
			return 0
		}
		if budget <= 0 {
			break
		}
		gaps, err := p.st.backfillGapsFor(ctx, bb, g.Name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.reportErr(ctx, "usenet/backfill-gaps", fmt.Errorf("%s/%s: %w", run.prov.label(), g.Name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDoneForBackbone(ctx, bb, g.Name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s/%s: backfill complete — no gaps remain", run.prov.label(), g.Name)
			continue
		}
		gj := gapJobs(g.Name, gaps, cfg.Batch, budget)
		gc := groupRow{Name: g.Name, RetentionDays: g.RetentionDays, ThrottleMs: g.ThrottleMs}.cutoff(cfg)
		for i := range gj {
			gj[i].cutoff = gc
			gj[i].throttle = time.Duration(g.ThrottleMs) * time.Millisecond
		}
		if len(gj) == 0 {
			continue
		}
		budget -= len(gj)
		jobs = append(jobs, gj...)
		targets[g.Name] = g
	}
	if len(jobs) == 0 {
		p.backfillJob.Log("%s: nothing to do this pass", run.prov.label())
		return 0
	}

	p.backfillJob.Log("%s: backfilling %d group(s), %d batch(es) over %d connection(s)…",
		run.prov.label(), len(targets), len(jobs), run.size)
	p.tel.backfill.noteGroups(len(targets))
	// nil onGroup: backfill passes are bounded (backfill_batches_per_run) and
	// recordBackfill needs the complete result set — with no callback,
	// runBatches returns every result.
	results := p.runBatches(ctx, pool, jobs, cfg, nil)
	for _, r := range results {
		p.tel.backfill.noteBatch(r.articles, r.staged, r.wire, r.ok)
	}

	// On lease loss the coverage writes are skipped, not just doomed to fail:
	// a sibling may own these groups now, and coverage-IS-state means the
	// unrecorded batches are simply refetched next pass.
	if ctx.Err() != nil && jobCtx.Err() == nil {
		p.backfillJob.Log("%s: lease lost mid-pass — coverage not recorded; batches will refetch next pass", run.prov.label())
		return 0
	}
	staged := p.recordBackfill(ctx, bb, targets, results, cfg)

	st := pool.Stats()
	p.backfillJob.Log("%s: %d historical article(s) staged from %d batch(es) (conns %d/%d, resets %d)",
		run.prov.label(), staged, len(jobs), st.Open, st.Target, st.Resets)
	return staged
}

func (p *Plugin) nextBackfill() time.Time {
	return time.Now().Add(time.Duration(p.cfg.BackfillIntervalMin) * time.Minute)
}

// recordBackfill persists coverage for successful batches, then re-derives each
// group's remaining work. Unlike the forward crawl there is no contiguity rule:
// a failed batch simply leaves its gap unrecorded, so the next pass recomputes
// it and tries again — coverage IS the state, so nothing can be silently
// skipped.
func (p *Plugin) recordBackfill(ctx context.Context, backbone string, targets map[string]backfillRow, results []batchResult, cfg Config) (staged int) {
	byGroup := make(map[string][]batchResult, len(targets))
	for _, r := range results {
		staged += r.staged
		byGroup[r.group] = append(byGroup[r.group], r)
	}

	for name, rs := range byGroup {
		g, ok := targets[name]
		if !ok {
			continue
		}
		var oldest time.Time
		for _, r := range rs {
			if !r.ok {
				continue
			}
			if err := p.st.recordFetchedRangeFor(ctx, backbone, name, int64(r.lo), int64(r.hi)); err != nil {
				p.reportErr(ctx, "usenet/backfill-range-record", fmt.Errorf("%s: %w", name, err))
				continue
			}
			if !r.minDate.IsZero() && (oldest.IsZero() || r.minDate.Before(oldest)) {
				oldest = r.minDate
			}
		}

		// Reached this group's retention horizon: everything below is older still.
		cutoff := groupRow{Name: name, RetentionDays: g.RetentionDays}.cutoff(cfg)
		if !oldest.IsZero() && oldest.Before(cutoff) {
			if err := p.st.markBackfillDoneForBackbone(ctx, backbone, name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: reached the retention horizon (oldest %s)", name, oldest.Format("2006-01-02"))
			continue
		}

		// Re-derive what is left; the newest remaining gap is where the next pass
		// picks up, which is what back_watermark means to the coverage view.
		gaps, err := p.st.backfillGapsFor(ctx, backbone, name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.reportErr(ctx, "usenet/backfill-gaps", fmt.Errorf("%s: %w", name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDoneForBackbone(ctx, backbone, name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: backfill complete", name)
			continue
		}
		if err := p.st.updateBackWatermarkForBackbone(ctx, backbone, name, gaps[0].End, oldest); err != nil {
			p.reportErr(ctx, "usenet/backfill-watermark", fmt.Errorf("%s: %w", name, err))
		}
	}
	return staged
}
