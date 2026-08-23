package usenet

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// PGStore is the Postgres implementation of Store. Every method runs through
// the SchemaDB's WithTx, which scopes search_path to "usenet" so unqualified
// table names resolve into the plugin's own schema.
type PGStore struct{ db *core.SchemaDB }

// NewPGStore builds the Postgres-backed store over a plugin-scoped SchemaDB.
func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// stats returns crawl progress: total NZBs, total staged articles, and per
// active-group status (NZBs, staged, last crawl, watermark vs server high).
// forwardBacklog sums server_high - high_watermark across active groups: how
// many articles the servers hold that we have not crawled forward to yet.
// A handful of indexed rows — safe to consult once per catch-up iteration.
// forwardBacklog is what the catch-up loop measures to decide whether to go
// again immediately instead of sleeping out the interval.
//
// holdLow must match the crawl's own hold, or the loop chases work it has
// decided not to do. With the hold enabled and a low-tier group 299M articles
// behind, an unfiltered backlog told the loop it was hopelessly behind after a
// pass that had legitimately finished everything available — so it re-rounded
// every two seconds, doing a handful of batches each time and never sleeping.
// The stall guard eventually caught it (the figure stops falling), but only
// after a burst of empty rounds, and it re-armed on every interval.
//
// The EXISTS clause is what keeps this honest: low-tier groups are discounted
// only while critical backfill is actually outstanding, which is exactly when
// the crawl is holding them. The moment that clears they count again, in the
// same query, with no second source of truth to drift.
func (s *PGStore) forwardBacklog(ctx context.Context, holdLow bool) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n,
			`SELECT COALESCE(SUM(GREATEST(s.server_high - s.high_watermark, 0)), 0)
			   FROM newsgroup_state s
			   JOIN newsgroups g ON g.name = s.group_name AND g.active
			  WHERE NOT ($1::boolean
			         AND g.tier = 'low'
			         AND EXISTS (SELECT 1
			                       FROM newsgroups gc
			                       JOIN newsgroup_state sc ON sc.group_name = gc.name
			                      WHERE gc.active
			                        AND gc.tier = 'critical'
			                        AND sc.backfill_done = FALSE))`, holdLow)
	})
	return n, err
}

// indexTotals is the poll-safe subset of stats(): exactly the four scalars
// status() consumes, at estimate precision.
type indexTotals struct {
	Groups            int
	TotalNZBs         int
	TotalStaged       int
	BackfillRemaining int64
}

// statsTotals answers the 5-second status poll without scanning anything.
//
// status() used to call stats(), which runs two exact COUNT(*)s and two
// whole-table GROUP BYs — then threw away everything but four scalars. This
// endpoint is polled every 5s by both admin tabs and documented for external
// monitors, and dashboard.go's own rule is that nothing on a poll path may
// scan a table (slow polls pile up until the DB saturates; the 2026-07-24
// prod timeout was this exact shape at 33M staged rows). Row counts use the
// planner's estimate like stagedCount — a liveness readout needs rough, not
// exact — and the backfill remainder aggregates newsgroup_state, which holds
// one row per (backbone, group).
func (s *PGStore) statsTotals(ctx context.Context) (indexTotals, error) {
	var t indexTotals
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &t.Groups,
			`SELECT COUNT(*) FROM newsgroups WHERE active`); err != nil {
			return err
		}
		est := func(table string, dst *int) error {
			var n int64
			if err := tx.GetContext(ctx, &n,
				`SELECT COALESCE((SELECT reltuples::bigint FROM pg_class WHERE oid = to_regclass($1)), 0)`,
				table); err != nil {
				return err
			}
			if n <= 0 {
				// No usable estimate. The never-analyzed sentinel is -1 only on
				// PG 14+; PG 13 (prod) leaves reltuples at 0, where a `n < 0`
				// guard is dead code and a freshly bulk-loaded table reads as
				// "0 staged" on the liveness poll until autoanalyze lands. 0 is
				// also what a genuinely empty table reports, and counting that
				// is instant — so count, but BOUNDED: this runs on the 5s poll
				// path, and un-analyzed is exactly when the table might hold a
				// bulk load. Past the cap the readout briefly shows the cap,
				// which beats both a false zero and an unbounded scan.
				if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM (SELECT 1 FROM `+table+` LIMIT 5000000) b`); err != nil { // sqllint:allow table is one of two literals passed below
					return err
				}
			}
			*dst = int(n)
			return nil
		}
		if err := est("nzbs", &t.TotalNZBs); err != nil {
			return err
		}
		if err := est("articles", &t.TotalStaged); err != nil {
			return err
		}
		// Mirrors the accumulation in stats(): remaining = back - server_low
		// for groups whose backfill is still open.
		return tx.GetContext(ctx, &t.BackfillRemaining,
			`SELECT COALESCE(SUM(COALESCE(s.back_watermark, s.high_watermark, 0) - COALESCE(s.server_low, 0)), 0)
			   FROM newsgroups g
			   JOIN newsgroup_state s ON s.group_name = g.name
			  WHERE g.active
			    AND NOT COALESCE(s.backfill_done, FALSE)
			    AND COALESCE(s.back_watermark, s.high_watermark, 0) > COALESCE(s.server_low, 0)`)
	})
	return t, err
}

// statsTotalsExact is statsTotals with the two row counts COUNTED.
//
// Same three scalars, and the difference is who asks. statsTotals answers a
// 5-second liveness poll, where an exact count is a table scan on every tick
// and rough is the right answer -- see its comment, and the 2026-07-24 prod
// timeout it records.
//
// The stats SNAPSHOT is a different question asked once an hour by a
// background job (stats/plugin.go: RunLoop with a one-hour interval, and the
// page says so). A scan an hour is affordable where a scan every five seconds
// is not, and the snapshot is published beside the host's own /stats, which
// runs COUNT(*). Two pages headed "Site stats" printing 160,692 and 160,980
// with nothing to say which is which is the report being wrong about itself --
// both plausible, which is worse than one being obviously broken. See
// docs/BACKLOG.md #12 in loon-demo-site.
func (s *PGStore) statsTotalsExact(ctx context.Context) (indexTotals, error) {
	t, err := s.statsTotals(ctx)
	if err != nil {
		return t, err
	}
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &t.TotalNZBs, `SELECT COUNT(*) FROM nzbs`); err != nil {
			return err
		}
		return tx.GetContext(ctx, &t.TotalStaged, `SELECT COUNT(*) FROM articles`)
	})
	return t, err
}

func (s *PGStore) stats(ctx context.Context) (pluginapi.IndexStats, error) {
	var st pluginapi.IndexStats
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &st.TotalNZBs, `SELECT COUNT(*) FROM nzbs`); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &st.TotalStaged, `SELECT COUNT(*) FROM articles`); err != nil {
			return err
		}
		// Crawl state is per BACKBONE, so one row per (backbone, group): article
		// numbers from two backbones describe different articles and must never be
		// merged into one bar. Groups with no state yet still appear (LEFT JOIN)
		// so a freshly added group is visible before its first crawl.
		//
		// NZB and staged counts are group-wide, not per backbone — an article is
		// indexed once no matter which backbone fetched it — so those repeat across
		// a group's backbone rows.
		//
		// The counts are LEFT JOINed single-pass aggregates, NOT per-group
		// correlated subqueries: this renders on the dashboard and feeds the 5s
		// status poll, and with pg staging holding tens of millions of rows the
		// correlated form ran 2 counts x N groups per render — the page took
		// minutes and timed out (2026-07-24, prod, 33M staged rows). One scan
		// per table is the ceiling here.
		type row struct {
			Backbone   string       `db:"backbone"`
			Name       string       `db:"name"`
			NZBs       int          `db:"nzbs"`
			Staged     int          `db:"staged"`
			LastCrawl  sql.NullTime `db:"last_crawl"`
			Watermark  int64        `db:"high_watermark"`
			HWDate     sql.NullTime `db:"high_watermark_date"`
			Back       int64        `db:"back_watermark"`
			BackDate   sql.NullTime `db:"back_watermark_date"`
			ServerLow  int64        `db:"server_low"`
			ServerHigh int64        `db:"server_high"`
			Done       bool         `db:"backfill_done"`
		}
		var rows []row
		if err := tx.SelectContext(ctx, &rows,
			`SELECT COALESCE(s.backbone, '') AS backbone, g.name,
			        COALESCE(s.high_watermark, 0) AS high_watermark,
			        s.high_watermark_date,
			        COALESCE(s.back_watermark, s.high_watermark, 0) AS back_watermark,
			        s.back_watermark_date,
			        COALESCE(s.server_low, 0)  AS server_low,
			        COALESCE(s.server_high, 0) AS server_high,
			        s.last_crawl,
			        COALESCE(s.backfill_done, FALSE) AS backfill_done,
			        COALESCE(nc.n, 0) AS nzbs,
			        COALESCE(ac.n, 0) AS staged
			 FROM newsgroups g
			 LEFT JOIN newsgroup_state s ON s.group_name = g.name
			 LEFT JOIN (SELECT group_name, COUNT(*) AS n FROM nzbs
			            GROUP BY group_name) nc ON nc.group_name = g.name
			 LEFT JOIN (SELECT group_name, COUNT(*) AS n FROM articles
			            GROUP BY group_name) ac ON ac.group_name = g.name
			 WHERE g.active = TRUE
			 ORDER BY COALESCE(s.backbone, ''), g.name`); err != nil {
			return err
		}
		for _, r := range rows {
			gs := pluginapi.GroupStat{
				Backbone: r.Backbone,
				Name:     r.Name, NZBs: r.NZBs, Staged: r.Staged,
				HighWatermark: r.Watermark, BackWatermark: r.Back,
				ServerLow: r.ServerLow, ServerHigh: r.ServerHigh, BackfillDone: r.Done,
			}
			if r.LastCrawl.Valid {
				gs.LastCrawl = r.LastCrawl.Time
			}
			if r.HWDate.Valid {
				gs.HighWatermarkDate = r.HWDate.Time
			}
			if r.BackDate.Valid {
				gs.BackWatermarkDate = r.BackDate.Time
			}
			if !r.Done && r.Back > r.ServerLow {
				st.TotalBackfillRemaining += r.Back - r.ServerLow
			}
			st.Groups = append(st.Groups, gs)
		}
		return nil
	})
	return st, err
}

func (s *PGStore) getSettings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			Key   string `db:"key"`
			Value string `db:"value"`
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT key, value FROM settings`); err != nil {
			return err
		}
		for _, r := range rows {
			out[r.Key] = r.Value
		}
		return nil
	})
	return out, err
}

func (s *PGStore) setSetting(ctx context.Context, key, value string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			key, value)
		return err
	})
}

// backfillRow is one active group that still has history to fetch below its
// back_watermark.
type backfillRow struct {
	Name string
	// Tier is the group's priority. Backfill spends its budget on the highest
	// tier that still has history, because a lower tier can be gated on a
	// higher one finishing (hold_low_until_backfilled) — and splitting effort
	// across tiers then makes the gate last proportionally longer.
	Tier          string
	BackWatermark int64
	ServerLow     int64
	// Per-group tuning, so backfill honours the same retention horizon and
	// pacing as the forward crawl (migration 013).
	RetentionDays int
	ThrottleMs    int
}
