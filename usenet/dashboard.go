package usenet

import (
	"context"
	"time"
)

// Live-activity reads for the crawlers page, plus the machine-readable status
// endpoint.
//
// These answer the question the coverage table cannot: "is anything happening
// RIGHT NOW". Coverage moves in watermarks, which barely change over a single
// pass; a list of what was staged and built in the last minutes is what tells
// an operator the crawler is alive and what it is chewing on.
//
// Every query here is a small indexed LIMIT. That is deliberate — the prod site
// learned this the expensive way: its dashboard ran a full-table GROUP BY and a
// TOAST-scanning SUM on the 60-second refresh, and they became the second-worst
// source of heap I/O on the database. Nothing on this page may scan a table.

// The old recentArticles/recentNZBs queries are gone on purpose: they read the
// PG staging and plugin nzbs tables, which are empty in redis-staging and
// host-sink modes respectively — the modes prod runs. "What did the crawler
// just build" now comes from the telemetry ring (telemetry.go noteBuilt),
// which is correct in every mode.

// ── status endpoint ─────────────────────────────────────────────────

// StatusReport is the machine-readable crawler status. Exposed as JSON so a run
// can be watched without scraping the admin HTML — useful for a first live run,
// for an external monitor, and for the operator's own scripts.
//
// Field names are stable; treat this as an API, not a view model.
type StatusReport struct {
	GeneratedAt time.Time `json:"generated_at"`

	Crawl    PassReport `json:"crawl"`
	Backfill PassReport `json:"backfill"`

	Providers []ProviderReport `json:"providers"`
	Workers   []WorkerReport   `json:"workers"`

	Groups          int   `json:"active_groups"`
	StagedArticles  int   `json:"staged_articles"`
	PendingReleases int   `json:"pending_releases"`
	ReadyReleases   int   `json:"ready_releases"`
	TotalNZBs       int   `json:"total_nzbs"`
	BackfillLeft    int64 `json:"backfill_remaining"`
	// BackfillETASeconds is 0 when there is nothing left or no measured rate.
	// A zero here means "unknown", never "done" — check backfill_remaining.
	BackfillETASeconds int64 `json:"backfill_eta_seconds"`

	// Jobs is the scheduler's view of the plugin's own jobs — status, last
	// activity line, and next scheduled run. On a split deployment these come
	// from the worker's published telemetry, so the web poll shows the truth.
	Jobs []JobReport `json:"jobs"`
	// ReadyGroups is redis staging's assembly queue depth (LLEN — O(1)).
	// Always 0 in pg mode: the equivalent there is a COUNT scan, which this
	// endpoint is forbidden from running per poll.
	ReadyGroups int64 `json:"ready_groups"`
	// Evicted counts hopeless sets shed by redis staging since worker start.
	Evicted int64 `json:"evicted"`
	// PendingCount is the size of the last incomplete-sets sample.
	PendingCount int `json:"pending_count"`

	RecentErrors []ErrorReport `json:"recent_errors"`
}

// PassReport is one job's current or last pass.
type PassReport struct {
	Running bool `json:"running"`
	Groups  int  `json:"groups"`
	// GroupsDone / BatchesTotal / Reading are the legacy dashboard's live
	// progress trio: "Group N / M — <what it is reading>" plus the bar's
	// denominator (batches/batches_total).
	GroupsDone   int     `json:"groups_done"`
	Batches      int     `json:"batches"`
	BatchesTotal int     `json:"batches_total"`
	Reading      string  `json:"reading"`
	Failed       int     `json:"failed_batches"`
	Articles     int     `json:"articles"`
	Staged       int     `json:"staged"`
	WireBytes    int64   `json:"wire_bytes"`
	DurationSec  float64 `json:"duration_seconds"`
	ArticlesSec  float64 `json:"articles_per_second"`
}

type ProviderReport struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Backbone string `json:"backbone"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}

type WorkerReport struct {
	ID     string `json:"id"`
	Groups int    `json:"groups_held"`
}

type ErrorReport struct {
	At  time.Time `json:"at"`
	Op  string    `json:"op"`
	Msg string    `json:"message"`
}

// JobReport is one scheduler job's live state (mirrors crawlerJobVM).
type JobReport struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Activity string `json:"activity"`
	Next     string `json:"next_run"`
	Running  bool   `json:"running"`
}

func passReport(st passStats) PassReport {
	return PassReport{
		Running: st.InProgress, Groups: st.Groups, GroupsDone: st.GroupsDone,
		Batches: st.Batches, BatchesTotal: st.BatchesTotal, Reading: st.Reading,
		Failed: st.Failed, Articles: st.Articles, Staged: st.Staged,
		WireBytes:   st.WireBytes,
		DurationSec: st.Duration().Seconds(),
		ArticlesSec: st.Rate(),
	}
}

// status assembles the report. Errors on individual sections are tolerated: a
// status endpoint that returns nothing because one query failed is useless
// precisely when it is most needed.
func (p *Plugin) status(ctx context.Context) StatusReport {
	rep := StatusReport{GeneratedAt: time.Now()}

	// telemetryView, not p.tel directly: on a split deployment this endpoint is
	// served by the web process, whose own trackers never move — the worker's
	// published snapshot is the real story there.
	tv := p.telemetryView(ctx)
	rep.Crawl = passReport(pickPass(tv.CrawlCur, tv.CrawlLast))
	rep.Backfill = passReport(pickPass(tv.BackfillCur, tv.BackfillLast))

	if st, err := p.st.stats(ctx); err == nil {
		rep.Groups = len(st.Groups)
		rep.TotalNZBs = st.TotalNZBs
		rep.StagedArticles = st.TotalStaged
		rep.BackfillLeft = st.TotalBackfillRemaining
		if d, ok := backfillETA(st.TotalBackfillRemaining, tv.BackfillRate); ok {
			rep.BackfillETASeconds = int64(d.Seconds())
		}
	}
	// PG staging only: builderInfo is a GROUP BY over the pg staging table —
	// meaningless zeros under redis staging, and at real volume (33M rows,
	// prod 2026-07-24) a 30s+ disk-spilling aggregation that this endpoint
	// would re-run EVERY 5s poll. Redis installs read ready_groups instead.
	if p.cfg.Staging != StagingRedis {
		if bi, err := p.st.builderInfo(ctx, 1); err == nil {
			rep.PendingReleases = bi.Releases
			rep.ReadyReleases = bi.Ready
		}
	}
	if provs, err := p.st.listServers(ctx); err == nil {
		for _, pr := range provs {
			rep.Providers = append(rep.Providers, ProviderReport{
				Name: pr.Name, Host: pr.Host, Backbone: pr.backboneKey(),
				Role: pr.Role, Enabled: pr.Enabled,
			})
		}
	}
	for _, w := range p.workerVMs(ctx) {
		rep.Workers = append(rep.Workers, WorkerReport{ID: w.ID, Groups: w.Groups})
	}
	for _, e := range tv.Errors {
		rep.RecentErrors = append(rep.RecentErrors, ErrorReport{At: e.At, Op: e.Op, Msg: e.Msg})
	}
	for _, j := range tv.Jobs {
		rep.Jobs = append(rep.Jobs, JobReport{
			Name: j.Name, Status: j.Status, Activity: j.Activity,
			Next: j.Next, Running: j.Running,
		})
	}
	rep.Evicted = tv.Evicted
	rep.PendingCount = len(tv.Pending)
	// Redis only: LLEN is O(1); the pg equivalent is a COUNT scan, and nothing
	// on a poll endpoint may scan a table.
	if p.cfg.Staging == StagingRedis {
		if si, err := p.staging.stagingInfo(ctx); err == nil {
			rep.ReadyGroups = si.ReadyGroups
		}
	}
	return rep
}
