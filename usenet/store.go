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
			        (SELECT COUNT(*) FROM nzbs n WHERE n.group_name = g.name) AS nzbs,
			        (SELECT COUNT(*) FROM articles a WHERE a.group_name = g.name) AS staged
			 FROM newsgroups g
			 LEFT JOIN newsgroup_state s ON s.group_name = g.name
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
	Name          string
	BackWatermark int64
	ServerLow     int64
	// Per-group tuning, so backfill honours the same retention horizon and
	// pacing as the forward crawl (migration 013).
	RetentionDays int
	ThrottleMs    int
}
