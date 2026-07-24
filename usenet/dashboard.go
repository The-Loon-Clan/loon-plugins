package usenet

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
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

// recentArticle is one just-staged article.
type recentArticle struct {
	Subject string    `db:"subject"`
	Group   string    `db:"group_name"`
	Poster  string    `db:"poster"`
	Bytes   int64     `db:"bytes"`
	Posted  time.Time `db:"posted"`
}

// recentNZB is one just-built release.
type recentNZB struct {
	Title   string    `db:"title"`
	Group   string    `db:"group_name"`
	Size    int64     `db:"size_bytes"`
	Created time.Time `db:"created_at"`
}

// recentArticles returns the newest staged articles. Ordered by ctid rather
// than a timestamp: staging has no insertion-time column, and posted-date order
// would show a backfill pass working through 2015 rather than what just landed.
func (s *PGStore) recentArticles(ctx context.Context, limit int) ([]recentArticle, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var rows []recentArticle
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT subject, group_name, poster, bytes, posted
			   FROM articles ORDER BY ctid DESC LIMIT $1`, limit)
	})
	return rows, err
}

// recentNZBs returns the newest assembled releases.
func (s *PGStore) recentNZBs(ctx context.Context, limit int) ([]recentNZB, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	var rows []recentNZB
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT title, group_name, size_bytes, created_at
			   FROM nzbs ORDER BY id DESC LIMIT $1`, limit)
	})
	return rows, err
}

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

	RecentErrors []ErrorReport `json:"recent_errors"`
}

// PassReport is one job's current or last pass.
type PassReport struct {
	Running     bool    `json:"running"`
	Groups      int     `json:"groups"`
	Batches     int     `json:"batches"`
	Failed      int     `json:"failed_batches"`
	Articles    int     `json:"articles"`
	Staged      int     `json:"staged"`
	WireBytes   int64   `json:"wire_bytes"`
	DurationSec float64 `json:"duration_seconds"`
	ArticlesSec float64 `json:"articles_per_second"`
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

func passReport(st passStats) PassReport {
	return PassReport{
		Running: st.InProgress, Groups: st.Groups, Batches: st.Batches,
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
	if bi, err := p.st.builderInfo(ctx, 1); err == nil {
		rep.PendingReleases = bi.Releases
		rep.ReadyReleases = bi.Ready
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
	return rep
}
