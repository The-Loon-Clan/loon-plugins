package usenet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// runBackfill walks each active group's back_watermark downward toward server_low,
// staging historical overviews within the retention window. The crawl is serial
// and monotonic, so a single pointer per group is exact — no gap tracking. Work is
// capped at BackfillBatchesPerRun batches across all groups so a pass is bounded
// and the forward crawler isn't starved of the shared connection.
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

	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	budget := cfg.BackfillBatchesPerRun
	totalStaged := 0
	for _, g := range groups {
		if ctx.Err() != nil || budget <= 0 {
			break
		}
		used, staged, err := p.backfillGroup(ctx, pool, g, cutoff, budget, cfg)
		budget -= used
		totalStaged += staged
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/backfill", fmt.Errorf("%s: %w", g.Name, err))
			p.backfillJob.Log("%s: error — %v", g.Name, err)
			continue
		}
	}
	p.backfillJob.Log("backfill pass complete: %d historical article(s) staged", totalStaged)
	p.backfillJob.SetIdle(p.nextBackfill())
	if totalStaged > 0 {
		go p.runBuild(ctx) // assemble any newly-complete historical sets
	}
}

func (p *Plugin) nextBackfill() time.Time {
	return time.Now().Add(time.Duration(p.cfg.BackfillIntervalMin) * time.Minute)
}

// backfillGroup fetches batches below the group's back_watermark, advancing it
// downward. Returns batches consumed and articles staged. Marks the group done
// when it reaches the server's oldest article or crosses the retention horizon.
func (p *Plugin) backfillGroup(ctx context.Context, pool *nntp.Pool, g backfillRow, cutoff time.Time, budget int, cfg Config) (used, staged int, err error) {
	var low int
	if err := pool.Do(ctx, func(c *nntp.Conn) error {
		_, l, _, err := c.Group(g.Name)
		if err != nil {
			return err
		}
		low = l
		return nil
	}); err != nil {
		return 0, 0, err
	}
	if int64(low) > g.ServerLow {
		// Server has expired articles since we last crawled; never dip below the
		// current low.
		g.ServerLow = int64(low)
	}

	back := g.BackWatermark
	if back <= g.ServerLow {
		return 0, 0, p.st.markBackfillDone(ctx, g.Name)
	}

	batch := int64(cfg.Batch)
	for used < budget {
		if ctx.Err() != nil {
			break
		}
		end := back - 1
		if end < g.ServerLow {
			break
		}
		start := end - batch + 1
		if start < g.ServerLow {
			start = g.ServerLow
		}
		var ovs []nntp.MessageOverview
		if err := pool.Do(ctx, func(c *nntp.Conn) error {
			// Whichever connection we get may be selected on another group.
			if _, _, _, err := c.Group(g.Name); err != nil {
				return err
			}
			got, _, err := c.Overview(int(start), int(end))
			if err != nil {
				return err
			}
			ovs = got
			return nil
		}); err != nil {
			return used, staged, err
		}
		used++

		arts := parseOverviews(ovs, g.Name, cutoff)
		if len(arts) > 0 {
			n, err := p.staging.stageArticles(ctx, arts)
			if err != nil {
				return used, staged, err
			}
			staged += n
		}
		back = start
		if err := p.st.updateBackWatermark(ctx, g.Name, back, oldestDate(ovs)); err != nil {
			return used, staged, err
		}
		p.backfillJob.Log("%s: backfilled down to article %d (%d staged this pass)", g.Name, back, staged)

		if back <= g.ServerLow {
			return used, staged, p.st.markBackfillDone(ctx, g.Name) // reached the bottom
		}
		// If even the newest article in this (older) batch is past retention,
		// everything below it is too — stop.
		if newest := newestDate(ovs); !newest.IsZero() && newest.Before(cutoff) {
			return used, staged, p.st.markBackfillDone(ctx, g.Name)
		}
	}
	return used, staged, nil
}
