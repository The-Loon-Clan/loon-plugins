package usenet

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

// Per-(backbone, group) crawl state. Every number here — watermarks, server
// bounds, coverage — is meaningful only within the backbone that produced it,
// because NNTP article numbers are assigned per backbone. Two accounts on the
// same backbone therefore SHARE this state (the second is extra connections, not
// extra coverage); two different backbones must never share it.

// activeGroupsForBackbone lists the groups to crawl along with THIS backbone's
// progress. A group with no state row yet reports watermark 0, which the crawler
// reads as "first pass" and caps accordingly.
func (s *PGStore) activeGroupsForBackbone(ctx context.Context, backbone string, limit int) ([]groupRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name      string        `db:"name"`
		HW        int64         `db:"high_watermark"`
		Retention sql.NullInt64 `db:"retention_days"`
		Throttle  int           `db:"throttle_ms"`
		LowPri    bool          `db:"low_priority"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Ordered by tier first: low-priority groups are crawled only after the
		// normal ones, so a huge low-value group cannot starve the rest.
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, COALESCE(st.high_watermark, 0) AS high_watermark,
			        g.retention_days, g.throttle_ms, g.low_priority
			   FROM newsgroups g
			   LEFT JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.backbone = $1
			  WHERE g.active = TRUE
			  ORDER BY g.low_priority, g.sort_order, g.name
			  LIMIT $2`, backbone, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]groupRow, len(rows))
	for i, r := range rows {
		out[i] = groupRow{
			Name: r.Name, HighWatermark: r.HW,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle, LowPriority: r.LowPri,
		}
	}
	return out, nil
}

// updateGroupStateForBackbone records this backbone's view of a group: its
// server bounds, and (when watermark > 0) an advance. GREATEST keeps the
// watermark monotonic; back_watermark is seeded once, on the first crawl, so
// backfill knows where this provider's history begins.
func (s *PGStore) updateGroupStateForBackbone(ctx context.Context, backbone, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var hw sql.NullTime
		if !hwDate.IsZero() {
			hw = sql.NullTime{Time: hwDate, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroup_state
			   (backbone, group_name, high_watermark, high_watermark_date,
			    back_watermark, server_low, server_high, last_crawl)
			 VALUES ($1,$2,$3,$4,$5,$6,$7, now())
			 ON CONFLICT (backbone, group_name) DO UPDATE SET
			   high_watermark      = GREATEST(newsgroup_state.high_watermark, EXCLUDED.high_watermark),
			   high_watermark_date = COALESCE(EXCLUDED.high_watermark_date, newsgroup_state.high_watermark_date),
			   back_watermark      = COALESCE(newsgroup_state.back_watermark, EXCLUDED.back_watermark),
			   server_low          = EXCLUDED.server_low,
			   server_high         = EXCLUDED.server_high,
			   last_crawl          = now()`,
			backbone, name, watermark, hw, backSeed, serverLow, serverHigh)
		return err
	})
}

// groupsNeedingBackfillForBackbone lists groups with history left below this
// backbone's back watermark.
func (s *PGStore) groupsNeedingBackfillForBackbone(ctx context.Context, backbone string, limit int) ([]backfillRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name      string        `db:"name"`
		Back      int64         `db:"back_watermark"`
		Low       int64         `db:"server_low"`
		Retention sql.NullInt64 `db:"retention_days"`
		Throttle  int           `db:"throttle_ms"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, st.back_watermark, st.server_low,
			        g.retention_days, g.throttle_ms
			   FROM newsgroups g
			   JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.backbone = $1
			  WHERE g.active = TRUE
			    AND st.backfill_done = FALSE
			    AND st.back_watermark IS NOT NULL
			    AND st.back_watermark > st.server_low
			  ORDER BY g.low_priority, g.sort_order, g.name
			  LIMIT $2`, backbone, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]backfillRow, len(rows))
	for i, r := range rows {
		out[i] = backfillRow{
			Name: r.Name, BackWatermark: r.Back, ServerLow: r.Low,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle,
		}
	}
	return out, nil
}

func (s *PGStore) updateBackWatermarkForBackbone(ctx context.Context, backbone, name string, back int64, oldest time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var od sql.NullTime
		if !oldest.IsZero() {
			od = sql.NullTime{Time: oldest, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state
			    SET back_watermark = $3,
			        back_watermark_date = COALESCE($4, back_watermark_date)
			  WHERE backbone = $1 AND group_name = $2`, backbone, name, back, od)
		return err
	})
}

func (s *PGStore) markBackfillDoneForBackbone(ctx context.Context, backbone, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state SET backfill_done = TRUE
			  WHERE backbone = $1 AND group_name = $2`, backbone, name)
		return err
	})
}

// recordFetchedRangeFor marks a span covered for ONE BACKBONE. Coverage cannot
// be global: another backbone's fetched ranges would mark these gaps as done and
// the crawler would skip content it never fetched.
func (s *PGStore) recordFetchedRangeFor(ctx context.Context, backbone, group string, start, end int64) error {
	if start > end {
		start, end = end, start
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`WITH absorbed AS (
			     DELETE FROM newsgroup_ranges
			      WHERE backbone = $1 AND group_name = $2
			        AND range_start <= $4 + 1
			        AND range_end   >= $3 - 1
			  RETURNING range_start, range_end
			 )
			 INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
			 SELECT $1, $2,
			        LEAST($3, COALESCE(MIN(range_start), $3)),
			        GREATEST($4, COALESCE(MAX(range_end), $4))
			   FROM absorbed`,
			backbone, group, start, end)
		return err
	})
}

// backfillGapsFor returns this backbone's uncovered spans, newest first.
func (s *PGStore) backfillGapsFor(ctx context.Context, backbone, group string, low, high int64) ([]articleRange, error) {
	var rows []articleRange
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT range_start AS start, range_end AS end
			   FROM newsgroup_ranges
			  WHERE backbone = $1 AND group_name = $2
			  ORDER BY range_start`, backbone, group)
	})
	if err != nil {
		return nil, err
	}
	return gapsBetween(rows, low, high), nil
}
