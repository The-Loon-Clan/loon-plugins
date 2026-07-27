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
// first, then a running backfill, then the last finished crawl — and maps it
// to the public shape.
func activityFrom(tv workerTelemetry) pluginapi.CrawlActivity {
	ps, backfill := tv.CrawlCur, false
	switch {
	case tv.CrawlCur.InProgress:
	case tv.BackfillCur.InProgress:
		ps, backfill = tv.BackfillCur, true
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
