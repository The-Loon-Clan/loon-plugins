package usenet

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// healthRow is one release due a check, with its stored NZB blob.
type healthRow struct {
	ID   int64
	Data []byte
}

// nzbsNeedingHealthCheck returns releases due a check: never-checked first, then
// OLDEST CONTENT first. The second key matters — old articles are the ones most
// likely to have expired, so checking them first finds real losses soonest.
// (Prod has no tiebreak here at all, so among its hundreds of thousands of
// never-checked rows the order is whatever the index scan happens to yield.)
//
// minAgeHours is a propagation guard, not a performance tweak: a release posted
// minutes ago may not have reached every server yet, so STATting it would report
// missing articles that are simply still in flight and wrongly mark a brand-new
// upload dead.
func (s *PGStore) nzbsNeedingHealthCheck(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if recheckDays <= 0 {
		recheckDays = 30
	}
	if minAgeHours < 0 {
		minAgeHours = 0
	}
	type row struct {
		ID   int64  `db:"id"`
		Data []byte `db:"nzb_data"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT id, nzb_data
			   FROM nzbs
			  WHERE status = 'completed'
			    AND nzb_data IS NOT NULL
			    AND COALESCE(posted_at, created_at) < now() - make_interval(hours => $2)
			    AND (last_health_check_at IS NULL
			         OR last_health_check_at < now() - make_interval(days => $3))
			  ORDER BY last_health_check_at NULLS FIRST,
			           COALESCE(posted_at, created_at) ASC, id
			  LIMIT $1`, limit, minAgeHours, recheckDays)
	})
	if err != nil {
		return nil, err
	}
	out := make([]healthRow, len(rows))
	for i, r := range rows {
		out[i] = healthRow{ID: r.ID, Data: r.Data}
	}
	return out, nil
}

// updateNzbHealth records a trustworthy verdict.
func (s *PGStore) updateNzbHealth(ctx context.Context, id int64, status string, total, missing, par2 int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nzbs
			    SET health_status = $2, total_segments = $3, missing_segments = $4,
			        par2_segments = $5, last_health_check_at = now()
			  WHERE id = $1`, id, status, total, missing, par2)
		return err
	})
}

// touchHealthChecked marks a release as looked-at WITHOUT changing its verdict.
// Used when a check was too inconclusive (or the blob unreadable) to trust —
// it stops that row from jamming the front of the queue forever while
// preserving whatever we last knew for certain.
func (s *PGStore) touchHealthChecked(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nzbs SET last_health_check_at = now() WHERE id = $1`, id)
		return err
	})
}

// healthBreakdown counts releases by verdict, for the stats surface.
// catalogTotals counts the plugin's own catalogue (internal sink mode).
func (s *PGStore) catalogTotals(ctx context.Context) (count, size int64, err error) {
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(size), 0)
			   FROM nzbs WHERE status = 'completed'`).Scan(&count, &size)
	})
	return count, size, err
}

func (s *PGStore) healthBreakdown(ctx context.Context) (map[string]int, error) {
	type row struct {
		Status string `db:"health_status"`
		N      int    `db:"n"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT health_status, COUNT(*) AS n
			   FROM nzbs WHERE status = 'completed' GROUP BY health_status`)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}
