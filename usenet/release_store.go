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

// maxTitleTokens bounds how many words one query turns into predicates. Each
// costs a leading-wildcard ILIKE, which no index accelerates, so an unbounded
// count would let a pasted paragraph become a hundred sequential scans.
const maxTitleTokens = 8

// titleTokens splits a search phrase the way release names are actually
// written.
//
// Scene releases are dot- or underscore-separated and humans type spaces, so a
// contiguous substring match means the two spellings NEVER meet: measured on a
// live index, "Fear the Walking Dead" returned 154 rows of which none were
// dot-named, and "Fear.the.Walking.Dead" returned 316 of which none were
// space-named. Neither spelling showed a member what the index held. Splitting
// on all three separators and ANDing the parts makes them one query, and word
// order and interior text stop mattering as a side effect.
//
// Metacharacters are escaped rather than passed through: '%' and '_' typed
// into a search box are characters somebody meant, not operators. Backslash is
// PostgreSQL's default LIKE escape, so no ESCAPE clause is needed — and
// spelling one out is a trap, because an empty escape silently disables
// escaping altogether.
func titleTokens(q string) []string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '.' || r == '_'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// Backslash first, or escaping '%' manufactures a backslash that then
		// escapes the wrong thing. '_' needs no entry: it is a separator
		// above, so no token can contain one.
		f = strings.NewReplacer(`\`, `\\`, `%`, `\%`).Replace(f)
		if f == "" {
			continue
		}
		out = append(out, "%"+f+"%")
		if len(out) == maxTitleTokens {
			break
		}
	}
	return out
}

// titleClause renders the AND-ed title predicates, starting at $startIdx. An
// empty result means "no usable terms", which callers read as "match
// everything" — the same answer an empty query already gave.
func titleClause(q string, startIdx int) (string, []any) {
	toks := titleTokens(q)
	if len(toks) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(toks))
	args := make([]any, 0, len(toks))
	for i, t := range toks {
		parts = append(parts, "title ILIKE $"+strconv.Itoa(startIdx+i))
		args = append(args, t)
	}
	return "(" + strings.Join(parts, " AND ") + ")", args
}

func (s *PGStore) searchNzbs(ctx context.Context, q string, limit int) ([]pluginapi.Release, error) {
	clause, args := titleClause(q, 1)
	if clause == "" {
		clause = "TRUE"
	}
	return s.queryReleases(ctx, clause, args, limit)
}

func (s *PGStore) browseNzbs(ctx context.Context, group string, limit int) ([]pluginapi.Release, error) {
	return s.queryReleases(ctx, `($1 = '' OR group_name = $1)`, []any{group}, limit)
}

// queryReleases lists completed NZBs newest-first. cond is a fixed literal
// referencing $1..$N; every value flows through a placeholder, so there is no
// injection despite the concatenation.
func (s *PGStore) queryReleases(ctx context.Context, cond string, args []any, limit int) ([]pluginapi.Release, error) {
	// Clamp to the CEILING, not down to the default. Asking for 500 and
	// getting 200 is an obvious truncation; asking for 200 and silently
	// getting 50 reads as "that is all there is", and cost a host real
	// debugging time.
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var rows []releaseRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow cond is a fixed WHERE fragment from the two internal callers (searchNzbs/browseNzbs); every value flows through $N
		return tx.SelectContext(ctx, &rows,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id,
			        series_key, series_name, season, episode, is_pack
			 FROM nzbs
			 WHERE status = 'completed' AND `+cond+`
			 ORDER BY COALESCE(posted_at, created_at) DESC LIMIT $`+strconv.Itoa(len(args)+1),
			append(append([]any{}, args...), limit)...)
	})
	if err != nil {
		return nil, err
	}
	return releasesToAPI(rows), nil
}

// feedFilter is what one Newznab feed request narrows by. A struct rather
// than a fifth and sixth positional argument, because season and episode are
// optional in a way cats and query are not.
type feedFilter struct {
	Query string
	Cats  []int
	// Season / Episode are nil when the client did not ask.
	Season  *int
	Episode *int
}

// episodeClause renders the tvsearch narrowing, starting at $startIdx.
//
// season and episode are NOT NULL DEFAULT 0 in this schema, and 0 means
// UNPARSED rather than "specials" (see migration 042). So a season=0 filter
// would match every release the parser has never read — the opposite of
// narrowing — and it is ignored instead. That costs Sonarr's specials search,
// which this schema cannot express at all; returning everything unparsed
// would be a worse answer than returning the unfiltered series.
//
// An episode filter also matches SEASON PACKS of that season, because a pack
// contains the episode: a client asking for S04E01 and being handed only the
// single-episode releases would miss the complete-season upload that is often
// the only one there.
func episodeClause(f feedFilter, startIdx int) (string, []any) {
	var parts []string
	var args []any
	idx := startIdx
	if f.Season != nil && *f.Season > 0 {
		parts = append(parts, "season = $"+strconv.Itoa(idx))
		args = append(args, *f.Season)
		idx++
	}
	if f.Episode != nil && *f.Episode > 0 {
		parts = append(parts, "(episode = $"+strconv.Itoa(idx)+" OR is_pack)")
		args = append(args, *f.Episode)
		idx++
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}

// feedReleases pages completed releases for the Newznab feed: optional title
// and episode filters (empty = recent-all), newest first, with the matching
// total for the newznab:response offset/total attrs. Every value flows through
// a placeholder.
func (s *PGStore) feedReleases(ctx context.Context, f feedFilter, limit, offset int) ([]pluginapi.Release, int, error) {
	query, cats := f.Query, f.Cats
	// Clamp to the ceiling rather than down to the default, for the same
	// reason as queryReleases: a host that asked for 200 and silently got 50
	// reads it as "that is all there is".
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	// The same tokenised title match searchNzbs uses. This is the path Sonarr
	// and Radarr take: they send `q=Breaking Bad` because that is how their
	// own database stores a title, and against a contiguous substring that
	// reached ~22% of the matching releases while `q=Dead to Me` reached
	// none at all.
	titleCond, titleArgs := titleClause(query, 1)
	if titleCond == "" {
		titleCond = "TRUE"
	}
	epCond, epArgs := episodeClause(f, len(titleArgs)+1)
	filterArgs := append(append([]any{}, titleArgs...), epArgs...)
	nArgs := len(filterArgs)

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
			 WHERE status = 'completed' AND `+titleCond+epCond+catClause+`
			 ORDER BY COALESCE(posted_at, created_at) DESC
			 LIMIT $`+strconv.Itoa(nArgs+1)+` OFFSET $`+strconv.Itoa(nArgs+2),
			append(append([]any{}, filterArgs...), limit, offset)...); err != nil {
			return err
		}
		if len(rows) < limit {
			total = offset + len(rows)
			return nil
		}
		if query == "" && len(cats) == 0 && epCond == "" {
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
			 WHERE status = 'completed' AND `+titleCond+epCond+catClause,
			filterArgs...)
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
