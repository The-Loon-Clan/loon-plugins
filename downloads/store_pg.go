package downloads

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// sel / get / exec are the schema-scoped equivalents of sqlx's Select, Get and
// Exec. They exist so reaching for an unscoped connection is not something a
// caller can do by accident.
func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

func (s *PGStore) get(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, dest, q, args...) })
}

// Record upserts the member's opinion.
//
// The conflict arm is where the design lives. status, detail and client take
// the NEW value — the latest run is what the member currently believes — while
// first_at keeps the original and reports counts up. So a member who fails
// three times and succeeds on the fourth ends with one row saying 'ok' and
// reports = 4, which is exactly the shape an operator wants: it worked, but it
// took four goes.
func (s *PGStore) Record(ctx context.Context, r Report) (Report, error) {
	var out Report
	err := s.get(ctx, &out, `
		INSERT INTO download_reports
		    (user_id, release_id, status, detail, client)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, release_id) DO UPDATE SET
		    status  = EXCLUDED.status,
		    detail  = EXCLUDED.detail,
		    client  = EXCLUDED.client,
		    reports = download_reports.reports + 1,
		    last_at = NOW()
		RETURNING user_id, release_id, status, detail, client, reports, first_at, last_at`,
		r.UserID, r.ReleaseID, r.Status, r.Detail, r.Client)
	if err != nil {
		return Report{}, fmt.Errorf("record report: %w", err)
	}
	return out, nil
}

func (s *PGStore) Tally(ctx context.Context, releaseIDs []int64) (map[int64]ReleaseTally, error) {
	out := map[int64]ReleaseTally{}
	if len(releaseIDs) == 0 {
		// Not an error and not a query: an empty page asks about nothing.
		return out, nil
	}
	var rows []ReleaseTally
	// count(*) FILTER rather than two queries or a sum of CASE: one pass, and
	// the two figures cannot disagree about which rows they counted.
	err := s.sel(ctx, &rows, `
		SELECT release_id,
		       count(*) FILTER (WHERE status = 'failed') AS failed,
		       count(*) FILTER (WHERE status = 'ok')     AS ok,
		       max(last_at)                              AS last_at
		  FROM download_reports
		 WHERE release_id = ANY($1)
		 GROUP BY release_id`, pq.Array(releaseIDs))
	if err != nil {
		return nil, fmt.Errorf("tally %d release(s): %w", len(releaseIDs), err)
	}
	for _, r := range rows {
		out[r.ReleaseID] = r
	}
	return out, nil
}

func (s *PGStore) Recent(ctx context.Context, limit int) ([]Report, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []Report
	// Failures first WITHIN the ordering, not as a filter: an operator opening
	// this page is looking for something to act on, and a page of successes
	// with the one failure below the fold is a page that hid the answer.
	err := s.sel(ctx, &rows, `
		SELECT user_id, release_id, status, detail, client, reports, first_at, last_at
		  FROM download_reports
		 ORDER BY (status = 'failed') DESC, last_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent reports: %w", err)
	}
	return rows, nil
}

func (s *PGStore) Counts(ctx context.Context) (int, int, error) {
	var row struct {
		Failed int `db:"failed"`
		OK     int `db:"ok"`
	}
	err := s.get(ctx, &row, `
		SELECT count(*) FILTER (WHERE status = 'failed') AS failed,
		       count(*) FILTER (WHERE status = 'ok')     AS ok
		  FROM download_reports`)
	if err != nil {
		return 0, 0, fmt.Errorf("report counts: %w", err)
	}
	return row.Failed, row.OK, nil
}
