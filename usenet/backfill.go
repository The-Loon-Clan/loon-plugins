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

	// Same pool as the forward crawl: the provider caps connections per account,
	// so a separate pool would just push us over the limit.
	pool, err := p.ensurePool(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.backfillJob.Log("no server configured")
			p.backfillJob.SetIdle(p.nextBackfill())
			return
		}
		p.backfillJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/backfill-pool", err)
		return
	}
	pool.TopUp(ctx)

	groups, err := p.st.groupsNeedingBackfill(ctx, cfg.MaxGroups)
	if err != nil {
		p.backfillJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/backfill-groups", err)
		return
	}
	if len(groups) == 0 {
		p.backfillJob.Log("nothing to backfill — all active groups caught up to the retention horizon")
		p.backfillJob.SetIdle(p.nextBackfill())
		return
	}

	// Build one flat job list from every group's gaps, oldest-work-last, bounded
	// by the shared budget so no single group can consume the whole pass.
	budget := cfg.BackfillBatchesPerRun
	var jobs []batchJob
	targets := make(map[string]backfillRow, len(groups))
	for _, g := range groups {
		if ctx.Err() != nil {
			return
		}
		if budget <= 0 {
			break
		}
		gaps, err := p.st.backfillGaps(ctx, g.Name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/backfill-gaps", fmt.Errorf("%s: %w", g.Name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDone(ctx, g.Name); err != nil {
				p.core.Errors.Report(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: backfill complete — no gaps remain above the server's oldest article", g.Name)
			continue
		}
		gj := gapJobs(g.Name, gaps, cfg.Batch, budget)
		if len(gj) == 0 {
			continue
		}
		budget -= len(gj)
		jobs = append(jobs, gj...)
		targets[g.Name] = g
	}
	if len(jobs) == 0 {
		p.backfillJob.Log("backfill: nothing to do this pass")
		p.backfillJob.SetIdle(p.nextBackfill())
		return
	}

	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	p.backfillJob.Log("backfilling %d group(s), %d batch(es) over %d connection(s)…",
		len(targets), len(jobs), cfg.Connections)
	results := p.runBatches(ctx, pool, jobs, cutoff, cfg)

	staged := p.recordBackfill(ctx, targets, results, cutoff)

	st := pool.Stats()
	p.backfillJob.Log("backfill pass complete: %d historical article(s) staged from %d batch(es) (conns %d/%d, resets %d)",
		staged, len(jobs), st.Open, st.Target, st.Resets)
	p.backfillJob.SetIdle(p.nextBackfill())
	if staged > 0 {
		go p.runBuild(ctx) // assemble any newly-complete historical sets
	}
}

func (p *Plugin) nextBackfill() time.Time {
	return time.Now().Add(time.Duration(p.cfg.BackfillIntervalMin) * time.Minute)
}

// recordBackfill persists coverage for successful batches, then re-derives each
// group's remaining work. Unlike the forward crawl there is no contiguity rule:
// a failed batch simply leaves its gap unrecorded, so the next pass recomputes
// it and tries again — coverage IS the state, so nothing can be silently
// skipped.
func (p *Plugin) recordBackfill(ctx context.Context, targets map[string]backfillRow, results []batchResult, cutoff time.Time) (staged int) {
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
			if err := p.st.recordFetchedRange(ctx, name, int64(r.lo), int64(r.hi)); err != nil {
				p.core.Errors.Report(ctx, "usenet/backfill-range-record", fmt.Errorf("%s: %w", name, err))
				continue
			}
			if !r.minDate.IsZero() && (oldest.IsZero() || r.minDate.Before(oldest)) {
				oldest = r.minDate
			}
		}

		// Reached the retention horizon: everything below is older still.
		if !oldest.IsZero() && oldest.Before(cutoff) {
			if err := p.st.markBackfillDone(ctx, name); err != nil {
				p.core.Errors.Report(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: reached the retention horizon (oldest %s)", name, oldest.Format("2006-01-02"))
			continue
		}

		// Re-derive what is left; the newest remaining gap is where the next pass
		// picks up, which is what back_watermark means to the coverage view.
		gaps, err := p.st.backfillGaps(ctx, name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/backfill-gaps", fmt.Errorf("%s: %w", name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDone(ctx, name); err != nil {
				p.core.Errors.Report(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: backfill complete", name)
			continue
		}
		if err := p.st.updateBackWatermark(ctx, name, gaps[0].End, oldest); err != nil {
			p.core.Errors.Report(ctx, "usenet/backfill-watermark", fmt.Errorf("%s: %w", name, err))
		}
	}
	return staged
}
