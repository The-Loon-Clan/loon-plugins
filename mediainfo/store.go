package mediainfo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// Report row.
type ReportRow struct {
	ID        int64      `db:"id"`
	ReleaseID int64      `db:"release_id"`
	UserID    int64      `db:"user_id"`
	Raw       string     `db:"raw"`
	Parsed    []byte     `db:"parsed"`
	CreatedAt time.Time  `db:"created_at"`
	EditedAt  *time.Time `db:"edited_at"`
	DeletedAt *time.Time `db:"deleted_at"`
	DeletedBy *int64     `db:"deleted_by"`
}

// Deleted reports whether staff withheld this one.
func (r ReportRow) Deleted() bool { return r.DeletedAt != nil }

// Report decodes the stored parse.
//
// From the STORED parse rather than by re-reading Raw, so a page render is not
// re-parsing every report on it — and so a report keeps rendering exactly as it
// did the day it was posted, even after the parser changes.
func (r ReportRow) Report() Report {
	var rep Report
	if len(r.Parsed) > 0 {
		_ = json.Unmarshal(r.Parsed, &rep)
	}
	return rep
}

// Shot row.
type Shot struct {
	ID        int64      `db:"id"`
	ReleaseID int64      `db:"release_id"`
	UserID    int64      `db:"user_id"`
	SourceURL string     `db:"source_url"`
	CachePath string     `db:"cache_path"`
	Bytes     int64      `db:"bytes"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`
	DeletedBy *int64     `db:"deleted_by"`
}

type Store interface {
	// Upsert writes a member's report for a release, replacing their own.
	Upsert(ctx context.Context, releaseID, userID int64, raw string, rep Report) error

	// Reports lists a release's live reports, newest first. Withheld rows are
	// INCLUDED so staff can see what was removed; the caller decides who sees
	// a body, exactly as the comments plugin does.
	Reports(ctx context.Context, releaseID int64) ([]ReportRow, error)

	// MineFor is a member's own report, for pre-filling the form.
	MineFor(ctx context.Context, releaseID, userID int64) (ReportRow, bool, error)

	// RemoveReport withholds one. byUser must be the author unless staff.
	RemoveReport(ctx context.Context, id, byUser int64, staff bool) (bool, error)

	// AddShot records a stored screenshot.
	AddShot(ctx context.Context, s Shot) error

	// Shots lists a release's live screenshots, oldest first — the order they
	// were added, which for a set of frames is the order somebody chose.
	Shots(ctx context.Context, releaseID int64) ([]Shot, error)

	// ShotCount is how many a member has already added to one release, for the
	// per-release cap.
	ShotCount(ctx context.Context, releaseID, userID int64) (int, error)

	// RemoveShot withholds one. byUser must be the author unless staff.
	RemoveShot(ctx context.Context, id, byUser int64, staff bool) (bool, error)

	// SummariesFor is the one-line answer for a set of releases, for a listing
	// that wants to say "HEVC at 10.4 Mb/s" beside a row.
	//
	// A batch because the caller is a page of forty rows.
	SummariesFor(ctx context.Context, releaseIDs []int64) (map[int64]string, error)
}

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

// Upsert replaces a member's own report for a release.
//
// REPLACES rather than appends, because the unique key is (release, member):
// somebody who pasted the wrong thing should be able to fix it, and somebody
// who posts six reports on one release is not contributing. A revision resets
// edited_at so the page can say the words changed.
func (s *PGStore) Upsert(ctx context.Context, releaseID, userID int64, raw string, rep Report) error {
	parsed, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO reports (release_id, user_id, raw, parsed)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (release_id, user_id) DO UPDATE
			   SET raw = EXCLUDED.raw,
			       parsed = EXCLUDED.parsed,
			       edited_at = NOW(),
			       -- A member reposting over their own withheld report brings
			       -- it back, which is correct: staff removed those words, and
			       -- these are different ones. A repeat offender is visible in
			       -- the moderation log rather than by leaving them silenced.
			       deleted_at = NULL,
			       deleted_by = NULL`,
			releaseID, userID, raw, parsed)
		return err
	})
}

const reportCols = `id, release_id, user_id, raw, parsed, created_at, edited_at, deleted_at, deleted_by`

func (s *PGStore) Reports(ctx context.Context, releaseID int64) ([]ReportRow, error) {
	var rows []ReportRow
	if err := s.sel(ctx, &rows, `
		SELECT `+reportCols+` FROM reports
		 WHERE release_id = $1
		 ORDER BY created_at DESC
		 LIMIT 20`, releaseID); err != nil {
		return nil, fmt.Errorf("reports: %w", err)
	}
	return rows, nil
}

func (s *PGStore) MineFor(ctx context.Context, releaseID, userID int64) (ReportRow, bool, error) {
	var rows []ReportRow
	if err := s.sel(ctx, &rows, `
		SELECT `+reportCols+` FROM reports
		 WHERE release_id = $1 AND user_id = $2`, releaseID, userID); err != nil {
		return ReportRow{}, false, fmt.Errorf("my report: %w", err)
	}
	if len(rows) == 0 {
		return ReportRow{}, false, nil
	}
	return rows[0], true, nil
}

// RemoveReport withholds one.
//
// The author check is IN the statement — `(user_id = $2 OR $3)` — so a forged
// id belonging to somebody else matches no row rather than being caught by a
// read this could race.
func (s *PGStore) RemoveReport(ctx context.Context, id, byUser int64, staff bool) (bool, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE reports SET deleted_at = NOW(), deleted_by = $2
			 WHERE id = $1 AND deleted_at IS NULL AND (user_id = $2 OR $3)`,
			id, byUser, staff)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("remove report: %w", err)
	}
	return n > 0, nil
}

func (s *PGStore) AddShot(ctx context.Context, sh Shot) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO shots (release_id, user_id, source_url, cache_path, bytes)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (release_id, source_url) DO UPDATE
			   -- The same picture posted again after staff withheld it comes
			   -- back under whoever posts it: the row is keyed on the release
			   -- and the source, and re-adding is a fresh contribution.
			   SET cache_path = EXCLUDED.cache_path,
			       deleted_at = NULL, deleted_by = NULL`,
			sh.ReleaseID, sh.UserID, sh.SourceURL, sh.CachePath, sh.Bytes)
		return err
	})
}

const shotCols = `id, release_id, user_id, source_url, cache_path, bytes, created_at, deleted_at, deleted_by`

func (s *PGStore) Shots(ctx context.Context, releaseID int64) ([]Shot, error) {
	var rows []Shot
	if err := s.sel(ctx, &rows, `
		SELECT `+shotCols+` FROM shots
		 WHERE release_id = $1 AND deleted_at IS NULL AND cache_path <> ''
		 ORDER BY created_at, id
		 LIMIT 24`, releaseID); err != nil {
		return nil, fmt.Errorf("shots: %w", err)
	}
	return rows, nil
}

func (s *PGStore) ShotCount(ctx context.Context, releaseID, userID int64) (int, error) {
	var n []int
	if err := s.sel(ctx, &n, `
		SELECT count(*)::int FROM shots
		 WHERE release_id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		releaseID, userID); err != nil {
		return 0, fmt.Errorf("shot count: %w", err)
	}
	if len(n) == 0 {
		return 0, nil
	}
	return n[0], nil
}

func (s *PGStore) RemoveShot(ctx context.Context, id, byUser int64, staff bool) (bool, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE shots SET deleted_at = NOW(), deleted_by = $2
			 WHERE id = $1 AND deleted_at IS NULL AND (user_id = $2 OR $3)`,
			id, byUser, staff)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("remove shot: %w", err)
	}
	return n > 0, nil
}

// SummariesFor answers "which copy is this" for a set of releases at once.
//
// The NEWEST live report per release wins, through DISTINCT ON. Two members
// describing one release is useful on the release page — a re-encode and the
// original often differ — but a listing row has space for one line, and the
// most recent is the least likely to describe a file that has since been
// replaced.
func (s *PGStore) SummariesFor(ctx context.Context, releaseIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(releaseIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ReleaseID int64  `db:"release_id"`
		Parsed    []byte `db:"parsed"`
	}
	if err := s.sel(ctx, &rows, `
		SELECT DISTINCT ON (release_id) release_id, parsed
		  FROM reports
		 WHERE release_id = ANY($1) AND deleted_at IS NULL
		 ORDER BY release_id, created_at DESC`, pq.Array(releaseIDs)); err != nil {
		return nil, fmt.Errorf("summaries: %w", err)
	}
	for _, r := range rows {
		var rep Report
		if err := json.Unmarshal(r.Parsed, &rep); err != nil {
			continue
		}
		if sum := rep.Summary(); sum != "" {
			out[r.ReleaseID] = sum
		}
	}
	return out, nil
}
