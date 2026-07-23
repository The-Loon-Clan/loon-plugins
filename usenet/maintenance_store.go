package usenet

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// nzbs maintenance: retagging, recategorising, retention prune, and junk sweeps
// (the off-peak cleanup jobs).

// retagUntagged re-parses tags for NZBs that have none set (rows from before a
// parser change, or that genuinely had no tags in the title). Idempotent.
func (s *PGStore) retagUntagged(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}
	updated := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			ID    int64  `db:"id"`
			Title string `db:"title"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, title FROM nzbs
			 WHERE resolution = '' AND source = '' AND video_codec = '' AND audio = '' AND language = ''
			 LIMIT $1`, limit); err != nil {
			return err
		}
		for _, r := range rows {
			t := parseTags(r.Title)
			if t.Empty() {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE nzbs SET resolution=$2, source=$3, video_codec=$4, audio=$5, language=$6 WHERE id=$1`,
				r.ID, t.Resolution, t.Source, t.Codec, t.Audio, t.Language); err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, err
}

// recategorizeDefaults reassigns the category of releases still at the default
// (8010 Other/Misc) — rows built before categorization, or before a rule change.
// fn is the catalog's Categorize; only changed rows are written.
func (s *PGStore) recategorizeDefaults(ctx context.Context, fn func(group, title string) int, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	updated := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			ID    int64  `db:"id"`
			Group string `db:"group_name"`
			Title string `db:"title"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, group_name, title FROM nzbs WHERE category_id = 8010 LIMIT $1`, limit); err != nil {
			return err
		}
		for _, r := range rows {
			cat := fn(r.Group, r.Title)
			if cat == 8010 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE nzbs SET category_id = $2 WHERE id = $1`, r.ID, cat); err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, err
}

func (s *PGStore) pruneNzbs(ctx context.Context, days int) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM nzbs WHERE COALESCE(posted_at, created_at) < now() - make_interval(days => $1)`, days)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// deleteJunkNzbs removes already-built NZBs whose title is junk (rows from
// before junk filtering, or that slipped through). Detection is Go-side, so we
// scan titles then delete by id in chunks.
func (s *PGStore) deleteJunkNzbs(ctx context.Context) (int, error) {
	removed := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			ID    int64  `db:"id"`
			Title string `db:"title"`
			Size  int64  `db:"size"`
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT id, title, size FROM nzbs`); err != nil {
			return err
		}
		var ids []int64
		for _, r := range rows {
			// Sized: built releases have a known payload, so the size-band rules
			// apply — this sweep is what retroactively cleans junk that predates
			// a rule (prod's TagJunkTitlesBatch is the SQL equivalent).
			if isJunkTitleSized(r.Title, r.Size) {
				ids = append(ids, r.ID)
			}
		}
		for start := 0; start < len(ids); start += 1000 {
			end := min(start+1000, len(ids))
			q, args, err := sqlx.In(`DELETE FROM nzbs WHERE id IN (?)`, ids[start:end])
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, tx.Rebind(q), args...); err != nil {
				return err
			}
		}
		removed = len(ids)
		return nil
	})
	return removed, err
}

// deleteJunkStaged removes staged articles whose base_subject is junk (the
// backlog from before ingest filtering). Distinct bases are far fewer than
// rows; deleted in chunks.
func (s *PGStore) deleteJunkStaged(ctx context.Context) (int64, error) {
	var deleted int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var bases []string
		if err := tx.SelectContext(ctx, &bases, `SELECT DISTINCT base_subject FROM articles`); err != nil {
			return err
		}
		var junk []string
		for _, b := range bases {
			if isJunkTitle(b) {
				junk = append(junk, b)
			}
		}
		for start := 0; start < len(junk); start += 1000 {
			end := min(start+1000, len(junk))
			q, args, err := sqlx.In(`DELETE FROM articles WHERE base_subject IN (?)`, junk[start:end])
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, tx.Rebind(q), args...)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			deleted += n
		}
		return nil
	})
	return deleted, err
}

// pruneStagedOlderThan drops staged articles past the horizon. The horizon is
// config-driven (staging_prune_hours, default 6) — pgStaging passes the live
// value. A non-positive hours falls back to 6 so a bad setting can't disable the
// sweep and let staging grow unbounded.
func (s *PGStore) pruneStagedOlderThan(ctx context.Context, hours int) (int64, error) {
	if hours <= 0 {
		hours = 6
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM articles WHERE added_at < now() - make_interval(hours => $1)`, hours)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// stagedCount is the total staged-article row count — pgStaging's pressure
// numerator (staged rows / staging_max_rows).
func (s *PGStore) stagedCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM articles`)
	})
	return n, err
}
