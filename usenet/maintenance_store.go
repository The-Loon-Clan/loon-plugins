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

// recategorizeSweep re-runs the categoriser over the whole table and writes the
// rows whose answer changed. fn is the catalog's Categorize.
//
// It used to consider only rows still at the default (8010 Other/Misc), on the
// assumption that a categorised row is a settled row. It is not: a rule change
// corrects rows that matched the WRONG rule just as often as rows that matched
// none, and those never sat at 8010 to be found.
//
// Measured on the reference index at the time of the change — 8,277 of 118,180
// rows (7.00%) disagreed with the current rules, and only a tenth of them were
// at the default:
//
//	5,723  TV/HD          -> TV/UHD       2160p episodes, filed HD because the
//	                                      old code returned a flat 5040 for any
//	                                      episode regardless of resolution
//	  780  Audio/Lossless -> video        FLAC in the name of a video release
//	  638  TV/HD          -> TV/SD
//	  202  Movies/HD      -> TV/Anime     fansub checksum
//
// A row the categoriser has NO opinion on is left alone rather than moved to
// 8010: fn already consults the newsgroup as a fallback, so 8010 back from it
// means neither the title nor the group knew — which is not a reason to
// discard a category that may have been set by something better informed.
//
// Cursored and batched like the other sweeps, so one run is bounded and a
// restart resumes rather than starting over.
func (s *PGStore) recategorizeSweep(ctx context.Context, fn func(group, title string) int, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	updated := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		cursor := sweepCursor(ctx, tx, "recategorize_cursor")
		var rows []struct {
			ID       int64  `db:"id"`
			Group    string `db:"group_name"`
			Title    string `db:"title"`
			Category int    `db:"category_id"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, group_name, title, category_id FROM nzbs
			  WHERE id > $2
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
			if !shouldRewrite(r.Category, cat) {
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

// sweepVerdict is the junk sweep's per-row decision, extracted so the
// build/sweep parity below is unit-testable without a database.
type sweepVerdict int

const (
	sweepSpare sweepVerdict = iota
	sweepDelete
	sweepNeedsBlob
)

// junkSweepVerdict mirrors classifyRelease's decision order, because the
// sweep re-judging with rules the build deliberately excused was deleting
// real releases every prune pass: a 2.2 MB magazine whose ARTICLES named
// .pdf (stored via the kind vouch, which demotes to the unsized rules) died
// to under_5mib because the sweep saw only the title; a 4 MB "[hentai]"
// tagged release (stored via the tag bypass) died the same way. The articles
// are pruned on a ~6h horizon in this same job, so the build-then-delete
// cycle repeated for every future copy.
//
//   - The tag bypass spares outright — those rows were stored on purpose.
//   - Structural (unsized) junk deletes regardless: a recognised kind never
//     excused a junk NAME at build either.
//   - A verdict only the SIZE rules produce needs the blob: the build may
//     have vouched the kind from the article filenames, and the stored NZB's
//     <file subject> attributes are the only surviving copy of them.
func junkSweepVerdict(title string, size int64) sweepVerdict {
	if parseCategoryTag(title) != "" {
		return sweepSpare
	}
	if isJunkTitle(title) {
		return sweepDelete
	}
	if isJunkTitleSized(title, size) {
		return sweepNeedsBlob
	}
	return sweepSpare
}

// deleteJunkNzbs removes already-built NZBs whose title is junk (rows from
// before junk filtering, or that slipped through). Detection is Go-side, so we
// scan titles then delete by id in chunks. Rows only a size rule condemns get
// a second look at their stored blob — see junkSweepVerdict.
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
		var ids, needsBlob []int64
		for _, r := range rows {
			switch junkSweepVerdict(r.Title, r.Size) {
			case sweepDelete:
				ids = append(ids, r.ID)
			case sweepNeedsBlob:
				needsBlob = append(needsBlob, r.ID)
			}
		}
		// Second pass, chunked like the DELETE below: fetch the blobs and
		// spare every row whose articles vouch a content kind — exactly the
		// input classifyRelease had at build. A nil or unreadable blob
		// spares too: this decision deletes a release.
		for start := 0; start < len(needsBlob); start += 1000 {
			end := min(start+1000, len(needsBlob))
			q, args, err := sqlx.In(`SELECT id, nzb_data FROM nzbs WHERE id IN (?)`, needsBlob[start:end])
			if err != nil {
				return err
			}
			var blobs []struct {
				ID   int64  `db:"id"`
				Data []byte `db:"nzb_data"`
			}
			if err := tx.SelectContext(ctx, &blobs, tx.Rebind(q), args...); err != nil {
				return err
			}
			for _, b := range blobs {
				if contentKindFromNZB(b.Data) == "" {
					ids = append(ids, b.ID)
				}
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

// shouldRewrite is the sweep's per-row decision, split out so it can be tested
// without a database — the SQL around it is only a cursor and an UPDATE.
//
// Getting it wrong is expensive in opposite directions: too eager and the
// sweep overwrites a category with a shrug, too shy and a rule change never
// reaches the rows it was written for.
func shouldRewrite(current, proposed int) bool {
	// 8010 back from the categoriser means neither the title NOR the newsgroup
	// had an opinion — Categorize already consults the group as a fallback.
	// That is not a reason to discard a category something better informed may
	// have set, and for a row already at 8010 it would mean an UPDATE on every
	// sweep forever.
	if proposed == 8010 {
		return false
	}
	return proposed != current
}

// fillEpisodes reads the series, season and episode out of titles that have
// never been read, in one batch.
//
// Folded into the Tag Fill job rather than given its own: it is the same
// operation on the same rows — re-read the title, store what it says — and
// that job already carries the write gate, the lease, the overlap lock and an
// admin card. A second job would be that ceremony copied for no gain, which is
// the duplication SEAMS.md exists to argue against.
//
// The cursor is episode_parsed_at IS NULL rather than a saved offset: a row is
// read exactly once, a row that parses to nothing is still DONE (or it would
// be picked up on every pass forever), and a parser improvement re-reads
// everything by clearing the column — no cursor to reset and no ordering to
// get right.
func (s *PGStore) fillEpisodes(ctx context.Context, limit int) (parsed, seen int, err error) {
	if limit <= 0 {
		limit = 500
	}
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			ID    int64  `db:"id"`
			Title string `db:"title"`
		}
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, title FROM nzbs
			  WHERE episode_parsed_at IS NULL
			  ORDER BY id
			  LIMIT $1`, limit); err != nil {
			return err
		}
		seen = len(rows)
		for _, r := range rows {
			e := ParseEpisode(r.Title)
			// Stamped either way. The stamp means "read", not "filed" — the
			// two-thirds of an index that is films and software must not be
			// re-read on every pass for the rest of time.
			if _, err := tx.ExecContext(ctx, `
				UPDATE nzbs
				   SET series_key = $2, series_name = $3, season = $4, episode = $5,
				       is_pack = $6, episode_parsed_at = now()
				 WHERE id = $1`,
				r.ID, e.SeriesKey, e.Series, e.Season, e.Episode, e.Pack); err != nil {
				return err
			}
			if e.Found() {
				parsed++
			}
		}
		return nil
	})
	return parsed, seen, err
}
