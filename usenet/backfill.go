package usenet

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// runBackfill drains each active group's history: it walks back_watermark down
// toward server_low within the retention window and keeps going — chunk after
// chunk — until every active group is caught up, rather than one bounded pass
// then a long idle. Between chunks (BackfillBatchesPerRun batches each) it
// releases the shared connection and runs the NZB Builder *synchronously*, so
// backfill applies its own back-pressure: it never stages history faster than
// the Builder assembles it (the staging table stays bounded), and the forward
// crawler gets the connection in the gap. Only when the whole backlog is drained
// does it idle until the next interval (a cheap re-check for freshly-aged
// history). ctx cancellation (worker shutdown) breaks the loop cleanly.
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

	srv, ok, err := p.st.getServer(ctx)
	if err != nil {
		p.backfillJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/backfill-server", err)
		return
	}
	if !ok || srv.Host == "" {
		p.backfillJob.Log("no server configured")
		p.backfillJob.SetIdle(p.nextBackfill())
		return
	}

	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	grandTotal := 0
	for {
		if ctx.Err() != nil {
			break
		}
		groups, err := p.st.groupsNeedingBackfill(ctx, cfg.MaxGroups)
		if err != nil {
			p.backfillJob.SetError(err.Error())
			p.core.Errors.Report(ctx, "usenet/backfill-groups", err)
			break
		}
		if len(groups) == 0 {
			p.backfillJob.Log("backfill complete — all active groups caught up to the retention horizon (%d article(s) staged this run)", grandTotal)
			break
		}

		conn, err := dialServer(srv)
		if err != nil {
			p.backfillJob.SetError(err.Error())
			p.core.Errors.Report(ctx, "usenet/backfill-dial", err)
			break
		}
		budget := cfg.BackfillBatchesPerRun
		usedTotal, staged := 0, 0
		for _, g := range groups {
			if ctx.Err() != nil || budget <= 0 {
				break
			}
			used, s, err := p.backfillGroup(ctx, conn, g, cutoff, budget, cfg)
			budget -= used
			usedTotal += used
			staged += s
			if err != nil {
				p.core.Errors.Report(ctx, "usenet/backfill", fmt.Errorf("%s: %w", g.Name, err))
				p.backfillJob.Log("%s: error — %v", g.Name, err)
				continue
			}
		}
		// Release the shared connection so the forward crawler can use it while
		// we assemble (the Builder is DB-only).
		conn.Quit()
		grandTotal += staged

		if usedTotal == 0 {
			// Groups were listed but no batch ran (all past retention / at the
			// floor) — nothing left to do; don't spin.
			break
		}
		// Back-pressure: assemble what we just staged before pulling the next
		// chunk, so backfill paces itself to the NZB Builder instead of flooding
		// the staging table.
		if staged > 0 {
			p.backfillJob.Log("staged %d so far — assembling before the next chunk", grandTotal)
			p.runBuild(ctx)
		}
	}
	p.backfillJob.SetIdle(p.nextBackfill())
}

func (p *Plugin) nextBackfill() time.Time {
	return time.Now().Add(time.Duration(p.cfg.BackfillIntervalMin) * time.Minute)
}

// backfillGroup fetches batches below the group's back_watermark, advancing it
// downward. Returns batches consumed and articles staged. Marks the group done
// when it reaches the server's oldest article or crosses the retention horizon.
func (p *Plugin) backfillGroup(ctx context.Context, conn *nntp.Conn, g backfillRow, cutoff time.Time, budget int, cfg Config) (used, staged int, err error) {
	if _, low, _, err := conn.Group(g.Name); err != nil {
		return 0, 0, err
	} else if int64(low) > g.ServerLow {
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
		if _, _, _, err := conn.Group(g.Name); err != nil {
			return used, staged, err
		}
		ovs, _, err := conn.Overview(int(start), int(end))
		if err != nil {
			return used, staged, err
		}
		used++

		arts := parseOverviews(ovs, g.Name, cutoff)
		if len(arts) > 0 {
			n, err := p.st.stageArticles(ctx, arts)
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
