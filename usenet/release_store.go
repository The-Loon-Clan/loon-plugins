package usenet

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Release reads: search, browse, the Newznab feed, and single-release detail.

func (s *PGStore) searchNzbs(ctx context.Context, q string, limit int) ([]pluginapi.Release, error) {
	return s.queryReleases(ctx, `title ILIKE '%' || $1 || '%'`, q, limit)
}

func (s *PGStore) browseNzbs(ctx context.Context, group string, limit int) ([]pluginapi.Release, error) {
	return s.queryReleases(ctx, `($1 = '' OR group_name = $1)`, group, limit)
}

// queryReleases lists completed NZBs newest-first. cond is a fixed literal
// referencing $1 (the search term or group name); arg flows through the
// placeholder, so there is no injection despite the concatenation.
func (s *PGStore) queryReleases(ctx context.Context, cond, arg string, limit int) ([]pluginapi.Release, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []releaseRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow cond is a fixed WHERE fragment from the two internal callers (searchNzbs/browseNzbs); the search value flows through $1
		return tx.SelectContext(ctx, &rows,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id,
			        series_key, series_name, season, episode, is_pack
			 FROM nzbs
			 WHERE status = 'completed' AND `+cond+`
			 ORDER BY COALESCE(posted_at, created_at) DESC LIMIT $2`, arg, limit)
	})
	if err != nil {
		return nil, err
	}
	return releasesToAPI(rows), nil
}

// feedReleases pages completed releases for the Newznab feed: optional title
// filter (empty = recent-all), newest first, with the matching total for the
// newznab:response offset/total attrs. query flows through $1 (no injection).
func (s *PGStore) feedReleases(ctx context.Context, query string, cats []int, limit, offset int) ([]pluginapi.Release, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	// cat filter: category ids are ints, so an inlined IN() list is injection-safe.
	catClause := ""
	if len(cats) > 0 {
		parts := make([]string, 0, len(cats))
		for _, c := range cats {
			parts = append(parts, strconv.Itoa(c))
		}
		catClause = " AND category_id IN (" + strings.Join(parts, ",") + ")"
	}
	var rows []releaseRow
	var total int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Page first, count only when the page cannot answer the total itself.
		// The COUNT is a full pass over completed rows (a leading-wildcard
		// ILIKE defeats the title index), and this runs per API request. A
		// short page means offset+len(rows) IS the exact total — which covers
		// searches whose results fit one page. It does NOT cover the recurring
		// request: Sonarr/Hydra poll the EMPTY-query RSS form about once a
		// minute, and any catalog deeper than one page returns a full page
		// there — that case is answered below from a planner estimate instead,
		// because its count is the constant "all completed releases", the most
		// expensive possible value of this query.
		// sqllint:allow catClause is a literal built from int-only category ids (strconv.Itoa); all values flow through $N
		if err := tx.SelectContext(ctx, &rows,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id,
			        series_key, series_name, season, episode, is_pack
			 FROM nzbs
			 WHERE status = 'completed' AND ($1 = '' OR title ILIKE '%' || $1 || '%')`+catClause+`
			 ORDER BY COALESCE(posted_at, created_at) DESC
			 LIMIT $2 OFFSET $3`, query, limit, offset); err != nil {
			return err
		}
		if len(rows) < limit {
			total = offset + len(rows)
			return nil
		}
		if query == "" && len(cats) == 0 {
			// Unfiltered full page: total ≈ every completed release. The feed
			// total is a pagination hint, not an accounting figure, so the
			// planner's estimate serves; the offset+len floor keeps it
			// consistent with the page just returned. reltuples counts all
			// rows regardless of status — close enough for a hint, and the
			// exact COUNT stays available to every filtered query below.
			var est int64
			if err := tx.GetContext(ctx, &est,
				`SELECT COALESCE((SELECT reltuples::bigint FROM pg_class WHERE oid = to_regclass('nzbs')), 0)`); err != nil {
				return err
			}
			if est > int64(offset+len(rows)) {
				total = int(est)
				return nil
			}
			// Young or never-analyzed table: fall through to the exact count.
		}
		// sqllint:allow catClause is a literal built from int-only category ids (strconv.Itoa); all values flow through $N
		return tx.GetContext(ctx, &total,
			`SELECT COUNT(*) FROM nzbs
			 WHERE status = 'completed' AND ($1 = '' OR title ILIKE '%' || $1 || '%')`+catClause, query)
	})
	if err != nil {
		return nil, 0, err
	}
	return releasesToAPI(rows), total, nil
}

// releaseRow is the scan shape for a completed release across search, browse,
// and the Newznab feed — the columns pluginapi.Release exposes.
type releaseRow struct {
	ID         int64        `db:"id"`
	Title      string       `db:"title"`
	Size       int64        `db:"size"`
	Posted     sql.NullTime `db:"posted_at"`
	Group      string       `db:"group_name"`
	Resolution string       `db:"resolution"`
	Source     string       `db:"source"`
	Codec      string       `db:"video_codec"`
	Audio      string       `db:"audio"`
	Language   string       `db:"language"`
	CategoryID int          `db:"category_id"`
	// Where the title said this sits in a series. Empty/zero for the two
	// thirds of an index that says nothing — see episode.go.
	SeriesKey  string `db:"series_key"`
	SeriesName string `db:"series_name"`
	Season     int    `db:"season"`
	Episode    int    `db:"episode"`
	Pack       bool   `db:"is_pack"`
}

func (r releaseRow) toAPI() pluginapi.Release {
	rel := pluginapi.Release{
		ID: r.ID, Title: r.Title, Size: r.Size, Group: r.Group,
		SeriesKey: r.SeriesKey, SeriesName: r.SeriesName,
		Season: r.Season, Episode: r.Episode, Pack: r.Pack,
		Resolution: r.Resolution, Source: r.Source, Codec: r.Codec,
		Audio: r.Audio, Language: r.Language, CategoryID: r.CategoryID,
	}
	if r.Posted.Valid {
		rel.Posted = r.Posted.Time
	}
	return rel
}

func releasesToAPI(rows []releaseRow) []pluginapi.Release {
	out := make([]pluginapi.Release, len(rows))
	for i, r := range rows {
		out[i] = r.toAPI()
	}
	return out
}

// detailRow is a release row with its gzipped NZB blob, for the detail page.
type detailRow struct {
	releaseRow
	Data []byte `db:"nzb_data"`
}

// releaseByID loads one completed release + its NZB blob; nil if absent.
func (s *PGStore) releaseByID(ctx context.Context, id int64) (*detailRow, error) {
	var r detailRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &r,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id,
			        series_key, series_name, season, episode, is_pack, nzb_data
			 FROM nzbs WHERE id = $1 AND status = 'completed'`, id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PGStore) nzbData(ctx context.Context, id int64) ([]byte, string, error) {
	var data []byte
	var filename string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT nzb_data, filename FROM nzbs WHERE id = $1`, id).Scan(&data, &filename)
	})
	if err != nil {
		return nil, "", err
	}
	return data, filename, nil
}
