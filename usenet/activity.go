package usenet

import (
	"context"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// activitySurface publishes pluginapi.UsenetActivity: the counts-only liveness
// snapshot a host may serve on a non-admin stats endpoint. It reads the same
// telemetry the admin dashboard renders (local in the worker, the published
// settings row elsewhere) but strips everything that is not a number —
// status.json stays the admin-gated surface for group names and error text.
type activitySurface struct{ p *Plugin }

func (a activitySurface) Activity(ctx context.Context) (pluginapi.CrawlActivity, error) {
	return activityFrom(a.p.telemetryView(ctx)), nil
}

// activityFrom picks the pass the widget should describe — a running crawl
// first, then a running backfill, then whichever pass FINISHED most recently.
// The idle fallback used to be hardwired to the last crawl, and during a
// months-long continuous backfill on 5-minute intervals the public widget
// sawtoothed: the backfill's large pass-cumulative counters, then a snap down
// to the last forward crawl's small ones at every pass boundary, then back up
// — which reads as the crawler losing work.
func activityFrom(tv workerTelemetry) pluginapi.CrawlActivity {
	ps, backfill := tv.CrawlCur, false
	switch {
	case tv.CrawlCur.InProgress:
	case tv.BackfillCur.InProgress:
		ps, backfill = tv.BackfillCur, true
	case tv.BackfillLast.Finished.After(tv.CrawlLast.Finished):
		ps, backfill = tv.BackfillLast, true
	default:
		ps = tv.CrawlLast
	}
	return pluginapi.CrawlActivity{
		InProgress:   ps.InProgress,
		Backfill:     backfill,
		UpdatedAt:    tv.UpdatedAt,
		Started:      ps.Started,
		Round:        ps.Round,
		Groups:       ps.Groups,
		GroupsDone:   ps.GroupsDone,
		Batches:      ps.Batches,
		BatchesTotal: ps.BatchesTotal,
		Articles:     ps.Articles,
		Staged:       ps.Staged,
		WireBytes:    ps.WireBytes,
	}
}
