package usenet

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The series reads: shows rather than releases.
//
// Every query here is bounded by `series_key <> ''`, which is also the partial
// index's condition (migration 042) — two thirds of a real index has no series
// and must not be scanned to answer a question about shows.

// seriesList returns shows matching a name query, most releases first.
func (s *PGStore) seriesList(ctx context.Context, query string, limit, offset int) ([]pluginapi.SeriesRow, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if offset < 0 {
		offset = 0
	}
	// Folded the same way seriesKey folds a name, so typing "the blacklist"
	// finds "theblacklist" — a reader searching for a show should not have to
	// know that punctuation was dropped.
	q := seriesKey(query)

	var rows []struct {
		Key      string `db:"series_key"`
		Name     string `db:"series_name"`
		Releases int    `db:"releases"`
		Seasons  int    `db:"seasons"`
		Latest   string `db:"latest"`
	}
	var total int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &total, `
			SELECT count(DISTINCT series_key) FROM nzbs
			 WHERE series_key <> '' AND status = 'completed'
			   AND ($1 = '' OR series_key LIKE '%' || $1 || '%')`, q); err != nil {
			return err
		}
		// mode() — the spelling MOST of the show's releases use.
		//
		// One show appears under several spellings that fold to one key, so a
		// name has to be chosen, and it has to be chosen the same way every
		// run or the list flickers. max() was deterministic and wrong: it
		// takes the lexicographic maximum, lowercase sorts after uppercase in
		// C collation, and the index therefore listed "the simpsons", "friends"
		// and "dexter" — the one spelling in each group that looked like a
		// mistake. mode() is just as deterministic (ties break on the ORDER BY)
		// and picks what the releases actually say.
		return tx.SelectContext(ctx, &rows, `
			SELECT series_key,
			       mode() WITHIN GROUP (ORDER BY series_name) AS series_name,
			       count(*)                       AS releases,
			       count(DISTINCT season)         AS seasons,
			       to_char(max(COALESCE(posted_at, created_at)), 'YYYY-MM-DD') AS latest
			  FROM nzbs
			 WHERE series_key <> '' AND status = 'completed'
			   AND ($1 = '' OR series_key LIKE '%' || $1 || '%')
			 GROUP BY series_key
			 ORDER BY count(*) DESC, series_key
			 LIMIT $2 OFFSET $3`, q, limit, offset)
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]pluginapi.SeriesRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, pluginapi.SeriesRow{
			Key: r.Key, Name: r.Name, Releases: r.Releases,
			Seasons: r.Seasons, Latest: r.Latest,
		})
	}
	return out, total, nil
}

// seriesName resolves one show's display name, and whether it exists.
func (s *PGStore) seriesName(ctx context.Context, key string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, nil
	}
	var name string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &name, `
			SELECT mode() WITHIN GROUP (ORDER BY series_name) FROM nzbs
			 WHERE series_key = $1 AND status = 'completed'`, key)
	})
	if err != nil || name == "" {
		// A key nobody has released under is "no such show", not an error: it
		// arrives from a URL and a typo must render a page, not a 500.
		return "", false, nil
	}
	return name, true, nil
}

// seriesSeasons lists one show's seasons with their counts — the numbers the
// season chips carry.
func (s *PGStore) seriesSeasons(ctx context.Context, key string) ([]pluginapi.SeriesSeason, error) {
	var rows []struct {
		Season   int `db:"season"`
		Releases int `db:"releases"`
		Episodes int `db:"episodes"`
	}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Episodes counts DISTINCT episode and excludes the packs, whose
		// episode is 0: "8 episodes" must not become 9 because somebody posted
		// the season boxed.
		return tx.SelectContext(ctx, &rows, `
			SELECT season,
			       count(*) AS releases,
			       count(DISTINCT episode) FILTER (WHERE NOT is_pack) AS episodes
			  FROM nzbs
			 WHERE series_key = $1 AND status = 'completed'
			 GROUP BY season
			 ORDER BY season`, key)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.SeriesSeason, 0, len(rows))
	for _, r := range rows {
		out = append(out, pluginapi.SeriesSeason{
			Season: r.Season, Releases: r.Releases, Episodes: r.Episodes,
		})
	}
	return out, nil
}

// seriesReleases lists one show's releases, newest first, optionally narrowed
// to a season and then to an episode.
//
// Negative means "every": the page's filter is removed by dropping a query
// parameter, and -1 is what an absent parameter reads as. Zero cannot mean
// that, because season 0 (specials) and episode 0 (a pack) are both real.
func (s *PGStore) seriesReleases(ctx context.Context, key string, season, episode, limit int) ([]pluginapi.Release, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []releaseRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT id, title, size, posted_at, group_name,
			       resolution, source, video_codec, audio, language, category_id,
			       series_key, series_name, season, episode, is_pack
			  FROM nzbs
			 WHERE series_key = $1 AND status = 'completed'
			   AND ($2 < 0 OR season = $2)
			   AND ($3 < 0 OR episode = $3)
			 ORDER BY season DESC, episode DESC, COALESCE(posted_at, created_at) DESC
			 LIMIT $4`, key, season, episode, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.Release, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toAPI())
	}
	return out, nil
}
