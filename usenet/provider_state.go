package usenet

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

// Per-(provider, group) crawl state. Every number here — watermarks, server
// bounds, coverage — is meaningful only for the server that produced it, because
// NNTP assigns article numbers per server. These accessors are the reason the
// plugin can run several providers where prod can only safely run providers that
// share a backbone.

// activeGroupsForProvider lists the groups to crawl along with THIS provider's
// progress. A group with no state row yet reports watermark 0, which the crawler
// reads as "first pass" and caps accordingly.
func (s *PGStore) activeGroupsForProvider(ctx context.Context, serverID, limit int) ([]groupRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name string `db:"name"`
		HW   int64  `db:"high_watermark"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, COALESCE(st.high_watermark, 0) AS high_watermark
			   FROM newsgroups g
			   LEFT JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.server_id = $1
			  WHERE g.active = TRUE
			  ORDER BY g.name
			  LIMIT $2`, serverID, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]groupRow, len(rows))
	for i, r := range rows {
		out[i] = groupRow{Name: r.Name, HighWatermark: r.HW}
	}
	return out, nil
}

// updateGroupStateForProvider records this provider's view of a group: its
// server bounds, and (when watermark > 0) an advance. GREATEST keeps the
// watermark monotonic; back_watermark is seeded once, on the first crawl, so
// backfill knows where this provider's history begins.
func (s *PGStore) updateGroupStateForProvider(ctx context.Context, serverID int, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var hw sql.NullTime
		if !hwDate.IsZero() {
			hw = sql.NullTime{Time: hwDate, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroup_state
			   (server_id, group_name, high_watermark, high_watermark_date,
			    back_watermark, server_low, server_high, last_crawl)
			 VALUES ($1,$2,$3,$4,$5,$6,$7, now())
			 ON CONFLICT (server_id, group_name) DO UPDATE SET
			   high_watermark      = GREATEST(newsgroup_state.high_watermark, EXCLUDED.high_watermark),
			   high_watermark_date = COALESCE(EXCLUDED.high_watermark_date, newsgroup_state.high_watermark_date),
			   back_watermark      = COALESCE(newsgroup_state.back_watermark, EXCLUDED.back_watermark),
			   server_low          = EXCLUDED.server_low,
			   server_high         = EXCLUDED.server_high,
			   last_crawl          = now()`,
			serverID, name, watermark, hw, backSeed, serverLow, serverHigh)
		return err
	})
}

// groupsNeedingBackfillForProvider lists groups with history left below this
// provider's back watermark.
func (s *PGStore) groupsNeedingBackfillForProvider(ctx context.Context, serverID, limit int) ([]backfillRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name string `db:"name"`
		Back int64  `db:"back_watermark"`
		Low  int64  `db:"server_low"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, st.back_watermark, st.server_low
			   FROM newsgroups g
			   JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.server_id = $1
			  WHERE g.active = TRUE
			    AND st.backfill_done = FALSE
			    AND st.back_watermark IS NOT NULL
			    AND st.back_watermark > st.server_low
			  ORDER BY g.name
			  LIMIT $2`, serverID, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]backfillRow, len(rows))
	for i, r := range rows {
		out[i] = backfillRow{Name: r.Name, BackWatermark: r.Back, ServerLow: r.Low}
	}
	return out, nil
}

func (s *PGStore) updateBackWatermarkForProvider(ctx context.Context, serverID int, name string, back int64, oldest time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var od sql.NullTime
		if !oldest.IsZero() {
			od = sql.NullTime{Time: oldest, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state
			    SET back_watermark = $3,
			        back_watermark_date = COALESCE($4, back_watermark_date)
			  WHERE server_id = $1 AND group_name = $2`, serverID, name, back, od)
		return err
	})
}

func (s *PGStore) markBackfillDoneForProvider(ctx context.Context, serverID int, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state SET backfill_done = TRUE
			  WHERE server_id = $1 AND group_name = $2`, serverID, name)
		return err
	})
}

// recordFetchedRangeFor marks a span covered FOR ONE PROVIDER. Coverage must be
// per-server or one provider's fetched ranges would mark another's gaps as
// already done, and that provider would skip content it never fetched.
func (s *PGStore) recordFetchedRangeFor(ctx context.Context, serverID int, group string, start, end int64) error {
	if start > end {
		start, end = end, start
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`WITH absorbed AS (
			     DELETE FROM newsgroup_ranges
			      WHERE server_id = $1 AND group_name = $2
			        AND range_start <= $4 + 1
			        AND range_end   >= $3 - 1
			  RETURNING range_start, range_end
			 )
			 INSERT INTO newsgroup_ranges (server_id, group_name, range_start, range_end)
			 SELECT $1, $2,
			        LEAST($3, COALESCE(MIN(range_start), $3)),
			        GREATEST($4, COALESCE(MAX(range_end), $4))
			   FROM absorbed`,
			serverID, group, start, end)
		return err
	})
}

// backfillGapsFor returns this provider's uncovered spans, newest first.
func (s *PGStore) backfillGapsFor(ctx context.Context, serverID int, group string, low, high int64) ([]articleRange, error) {
	var rows []articleRange
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT range_start AS start, range_end AS end
			   FROM newsgroup_ranges
			  WHERE server_id = $1 AND group_name = $2
			  ORDER BY range_start`, serverID, group)
	})
	if err != nil {
		return nil, err
	}
	return gapsBetween(rows, low, high), nil
}
