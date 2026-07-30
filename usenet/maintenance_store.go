package usenet

import (
	"context"
	"strconv"

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
		cursor := sweepCursor(ctx, tx, "tagfill_cursor")
		var rows []struct {
			ID    int64  `db:"id"`
			Title string `db:"title"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, title FROM nzbs
			 WHERE resolution = '' AND source = '' AND video_codec = '' AND audio = '' AND language = ''
			   AND id > $2
			 ORDER BY id
			 LIMIT $1`, limit, cursor); err != nil {
			return err
		}
		next := int64(0) // short page = sweep wrapped; restart from the top next run
		if len(rows) == limit {
			next = rows[len(rows)-1].ID
		}
		saveSweepCursor(ctx, tx, "tagfill_cursor", next)
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
		cursor := sweepCursor(ctx, tx, "recategorize_cursor")
		var rows []struct {
			ID    int64  `db:"id"`
			Group string `db:"group_name"`
			Title string `db:"title"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, group_name, title FROM nzbs
			  WHERE category_id = 8010 AND id > $2
			  ORDER BY id
			  LIMIT $1`, limit, cursor); err != nil {
			return err
		}
		next := int64(0)
		if len(rows) == limit {
			next = rows[len(rows)-1].ID
		}
		saveSweepCursor(ctx, tx, "recategorize_cursor", next)
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

// stagedCount is the staged-article row count — pgStaging's pressure
// numerator (staged rows / staging_max_rows). It uses the planner's estimate,
// not COUNT(*): pressure is consulted every backfill pass and on dashboard
// renders, and an exact count of a 33M-row table is seconds of I/O for a
// ratio that only needs to be roughly right. Falls back to the exact count
// while the table has never been analyzed (reltuples = -1).
func (s *PGStore) stagedCount(ctx context.Context) (int, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &n,
			`SELECT reltuples::bigint FROM pg_class WHERE oid = to_regclass('articles')`); err != nil {
			return err
		}
		if n < 0 {
			return tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM articles`)
		}
		return nil
	})
	return int(n), err
}

// sweepCursor reads a persisted keyset cursor from the settings table; absent
// or unreadable degrades to 0 (restart the sweep from the top — safe, just
// slower). Both maintenance sweeps below need one because their WHERE clauses
// keep matching rows the sweep decides not to change: a plain LIMIT re-reads
// the same first page forever and the sweep silently stops progressing.
func sweepCursor(ctx context.Context, tx *sqlx.Tx, key string) int64 {
	var raw string
	if err := tx.GetContext(ctx, &raw,
		`SELECT value FROM settings WHERE key = $1`, key); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func saveSweepCursor(ctx context.Context, tx *sqlx.Tx, key string, cur int64) {
	// Best-effort: a lost cursor restarts the sweep from id 0.
	_, _ = tx.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, strconv.FormatInt(cur, 10))
}
