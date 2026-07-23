package usenet

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// store is the usenet-schema data layer. Every method runs through the
// SchemaDB's WithTx, which scopes search_path to "usenet" so unqualified table
// names resolve into the plugin's own schema.
// PGStore is the Postgres implementation of Store (schema-scoped via SchemaDB;
// every method runs through WithTx so search_path resolves the usenet schema).
type PGStore struct{ db *core.SchemaDB }

// NewPGStore builds the Postgres-backed store over a plugin-scoped SchemaDB.
func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

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
	type row struct {
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
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow cond is a fixed WHERE fragment from the two internal callers (searchNzbs/browseNzbs); the search value flows through $1
		return tx.SelectContext(ctx, &rows,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id
			 FROM nzbs
			 WHERE status = 'completed' AND `+cond+`
			 ORDER BY COALESCE(posted_at, created_at) DESC LIMIT $2`, arg, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.Release, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.Release{
			ID: r.ID, Title: r.Title, Size: r.Size, Group: r.Group,
			Resolution: r.Resolution, Source: r.Source, Codec: r.Codec,
			Audio: r.Audio, Language: r.Language, CategoryID: r.CategoryID,
		}
		if r.Posted.Valid {
			out[i].Posted = r.Posted.Time
		}
	}
	return out, nil
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
	type row struct {
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
	}
	var rows []row
	var total int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow catClause is a literal built from int-only category ids (strconv.Itoa); all values flow through $N
		if err := tx.GetContext(ctx, &total,
			`SELECT COUNT(*) FROM nzbs
			 WHERE status = 'completed' AND ($1 = '' OR title ILIKE '%' || $1 || '%')`+catClause, query); err != nil {
			return err
		}
		// sqllint:allow catClause is a literal built from int-only category ids (strconv.Itoa); all values flow through $N
		return tx.SelectContext(ctx, &rows,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id
			 FROM nzbs
			 WHERE status = 'completed' AND ($1 = '' OR title ILIKE '%' || $1 || '%')`+catClause+`
			 ORDER BY COALESCE(posted_at, created_at) DESC
			 LIMIT $2 OFFSET $3`, query, limit, offset)
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]pluginapi.Release, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.Release{
			ID: r.ID, Title: r.Title, Size: r.Size, Group: r.Group,
			Resolution: r.Resolution, Source: r.Source, Codec: r.Codec,
			Audio: r.Audio, Language: r.Language, CategoryID: r.CategoryID,
		}
		if r.Posted.Valid {
			out[i].Posted = r.Posted.Time
		}
	}
	return out, total, nil
}

// stats returns crawl progress: total NZBs, total staged articles, and per
// active-group status (NZBs, staged, last crawl, watermark vs server high).
func (s *PGStore) stats(ctx context.Context) (pluginapi.IndexStats, error) {
	var st pluginapi.IndexStats
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &st.TotalNZBs, `SELECT COUNT(*) FROM nzbs`); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &st.TotalStaged, `SELECT COUNT(*) FROM articles`); err != nil {
			return err
		}
		type row struct {
			Name       string       `db:"name"`
			NZBs       int          `db:"nzbs"`
			Staged     int          `db:"staged"`
			LastCrawl  sql.NullTime `db:"last_crawl"`
			Watermark  int64        `db:"high_watermark"`
			HWDate     sql.NullTime `db:"high_watermark_date"`
			Back       int64        `db:"back_watermark"`
			BackDate   sql.NullTime `db:"back_watermark_date"`
			ServerLow  int64        `db:"server_low"`
			ServerHigh int64        `db:"server_high"`
			Done       bool         `db:"backfill_done"`
		}
		var rows []row
		if err := tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.high_watermark, g.high_watermark_date,
			        COALESCE(g.back_watermark, g.high_watermark) AS back_watermark,
			        g.back_watermark_date, g.server_low, g.server_high, g.last_crawl, g.backfill_done,
			        (SELECT COUNT(*) FROM nzbs n WHERE n.group_name = g.name) AS nzbs,
			        (SELECT COUNT(*) FROM articles a WHERE a.group_name = g.name) AS staged
			 FROM newsgroups g WHERE g.active = TRUE ORDER BY g.name`); err != nil {
			return err
		}
		for _, r := range rows {
			gs := pluginapi.GroupStat{
				Name: r.Name, NZBs: r.NZBs, Staged: r.Staged,
				HighWatermark: r.Watermark, BackWatermark: r.Back,
				ServerLow: r.ServerLow, ServerHigh: r.ServerHigh, BackfillDone: r.Done,
			}
			if r.LastCrawl.Valid {
				gs.LastCrawl = r.LastCrawl.Time
			}
			if r.HWDate.Valid {
				gs.HighWatermarkDate = r.HWDate.Time
			}
			if r.BackDate.Valid {
				gs.BackWatermarkDate = r.BackDate.Time
			}
			if !r.Done && r.Back > r.ServerLow {
				st.TotalBackfillRemaining += r.Back - r.ServerLow
			}
			st.Groups = append(st.Groups, gs)
		}
		return nil
	})
	return st, err
}

func (s *PGStore) groups(ctx context.Context) ([]pluginapi.GroupInfo, error) {
	type row struct {
		Name   string `db:"name"`
		Active bool   `db:"active"`
		NZBs   int64  `db:"nzbs"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.active, COUNT(n.id) AS nzbs
			 FROM newsgroups g LEFT JOIN nzbs n ON n.group_name = g.name
			 WHERE g.active = TRUE
			 GROUP BY g.name, g.active ORDER BY g.name`)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.GroupInfo, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.GroupInfo{Name: r.Name, Active: r.Active, NZBs: r.NZBs}
	}
	return out, nil
}

// detailRow is a release row with its gzipped NZB blob, for the detail page.
type detailRow struct {
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
	Data       []byte       `db:"nzb_data"`
}

// releaseByID loads one completed release + its NZB blob; nil if absent.
func (s *PGStore) releaseByID(ctx context.Context, id int64) (*detailRow, error) {
	var r detailRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &r,
			`SELECT id, title, size, posted_at, group_name,
			        resolution, source, video_codec, audio, language, category_id, nzb_data
			 FROM nzbs WHERE id = $1 AND status = 'completed'`, id)
	})
	if err == sql.ErrNoRows {
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

func (s *PGStore) getServer(ctx context.Context) (pluginapi.Server, bool, error) {
	var srv pluginapi.Server
	found := false
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		e := tx.QueryRowContext(ctx,
			`SELECT host, port, tls, username, password, enabled, backbone FROM servers ORDER BY id LIMIT 1`).
			Scan(&srv.Host, &srv.Port, &srv.TLS, &srv.Username, &srv.Password, &srv.Enabled, &srv.Backbone)
		if e == sql.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		found = true
		return nil
	})
	return srv, found, err
}

// saveServer updates the FIRST configured server, or creates one if there are
// none. It deliberately does not touch any other row: the wizard edits a single
// server, but the fleet may hold several (providers.go), and this used to
// DELETE FROM servers first — so saving the wizard would have silently wiped
// every additional provider an operator had added.
func (s *PGStore) saveServer(ctx context.Context, srv pluginapi.Server) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var id int
		err := tx.GetContext(ctx, &id, `SELECT id FROM servers ORDER BY id LIMIT 1`)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			_, err = tx.ExecContext(ctx,
				`INSERT INTO servers (host, port, tls, username, password, enabled, backbone, name)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$1)`,
				srv.Host, srv.Port, srv.TLS, srv.Username, srv.Password, srv.Enabled, srv.Backbone)
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE servers
			    SET host=$2, port=$3, tls=$4, username=$5, password=$6, enabled=$7, backbone=$8
			  WHERE id=$1`,
			id, srv.Host, srv.Port, srv.TLS, srv.Username, srv.Password, srv.Enabled, srv.Backbone)
		return err
	})
}

// upsertGroups inserts each name as an inactive group, ignoring duplicates.
// Returns how many were newly added.
func (s *PGStore) upsertGroups(ctx context.Context, names []string) (int, error) {
	added := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, name := range names {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroups (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, name)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
		return nil
	})
	return added, err
}

// allGroups returns up to limit groups, active first then alphabetical, for the
// admin picker. query filters by name substring so a 100k-group server is
// searchable instead of truncated to the first page.
func (s *PGStore) allGroups(ctx context.Context, query string, limit int) ([]pluginapi.GroupInfo, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	type row struct {
		Name   string `db:"name"`
		Active bool   `db:"active"`
		NZBs   int64  `db:"nzbs"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, g.active, COUNT(n.id) AS nzbs
			 FROM newsgroups g LEFT JOIN nzbs n ON n.group_name = g.name
			 WHERE ($1 = '' OR g.name ILIKE '%' || $1 || '%')
			 GROUP BY g.name, g.active
			 ORDER BY g.active DESC, g.name LIMIT $2`, query, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.GroupInfo, len(rows))
	for i, r := range rows {
		out[i] = pluginapi.GroupInfo{Name: r.Name, Active: r.Active, NZBs: r.NZBs}
	}
	return out, nil
}

// groupCount returns the total number of fetched groups (so the picker can show
// "showing N of M" and reassure that a big LIST was fully imported).
func (s *PGStore) groupCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM newsgroups`).Scan(&n)
	})
	return n, err
}

func (s *PGStore) setGroupActive(ctx context.Context, name string, active bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE newsgroups SET active = $2 WHERE name = $1`, name, active)
		return err
	})
}

// BuilderInfo is the NZB Builder's view of staging: how many articles are
// staged, how many distinct releases they form, how many are ready to assemble,
// and the largest still-incomplete releases (with unit progress) — so an admin
// can see WHY nothing is building (usually huge multi-file releases only
// partly crawled).
type BuilderInfo struct {
	StagedArticles int
	Releases       int
	Ready          int
	Pending        []PendingRelease
}

// PendingRelease is one incomplete staged release. Units are files for
// multi-file releases, else segments.
type PendingRelease struct {
	Base     string
	Have     int
	Need     int
	Segments int
	Multi    bool
}

// Pct is the unit-completion percentage (0-100).
func (p PendingRelease) Pct() int {
	if p.Need <= 0 {
		return 0
	}
	v := p.Have * 100 / p.Need
	if v > 100 {
		v = 100
	}
	return v
}

func (s *PGStore) builderInfo(ctx context.Context, limit int) (BuilderInfo, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var bi BuilderInfo
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &bi.StagedArticles, `SELECT COUNT(*) FROM articles`); err != nil {
			return err
		}
		// One GROUP BY over staging; the derived per-release "have/need units"
		// mirrors candidateGroups (files for multi-file, parts otherwise).
		const setsCTE = `
			WITH sets AS (
			  SELECT bool_or(file_parts) AS multi,
			         CASE WHEN bool_or(file_parts) THEN COUNT(DISTINCT file_num) ELSE COUNT(DISTINCT part_num) END AS have,
			         CASE WHEN bool_or(file_parts) THEN MAX(total_files)          ELSE MAX(total_parts)          END AS need,
			         base_subject, COUNT(*) AS segs
			  FROM articles GROUP BY group_name, base_subject
			)`
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Releases, setsCTE+` SELECT COUNT(*) FROM sets`); err != nil {
			return err
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Ready, setsCTE+` SELECT COUNT(*) FROM sets WHERE need > 0 AND have >= need`); err != nil {
			return err
		}
		var rows []struct {
			Base  string `db:"base_subject"`
			Have  int    `db:"have"`
			Need  int    `db:"need"`
			Segs  int    `db:"segs"`
			Multi bool   `db:"multi"`
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.SelectContext(ctx, &rows, setsCTE+`
			SELECT base_subject, have, need, segs, multi FROM sets
			WHERE NOT (need > 0 AND have >= need)
			ORDER BY segs DESC LIMIT $1`, limit); err != nil {
			return err
		}
		for _, r := range rows {
			bi.Pending = append(bi.Pending, PendingRelease{
				Base: r.Base, Have: r.Have, Need: r.Need, Segments: r.Segs, Multi: r.Multi,
			})
		}
		return nil
	})
	return bi, err
}

// ── plugin settings (admin-editable knob overrides) ─────────────────

func (s *PGStore) getSettings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []struct {
			Key   string `db:"key"`
			Value string `db:"value"`
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT key, value FROM settings`); err != nil {
			return err
		}
		for _, r := range rows {
			out[r.Key] = r.Value
		}
		return nil
	})
	return out, err
}

func (s *PGStore) setSetting(ctx context.Context, key, value string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			key, value)
		return err
	})
}

// ── backfill ────────────────────────────────────────────────────────

// backfillRow is one active group that still has history to fetch below its
// back_watermark.
type backfillRow struct {
	Name          string
	BackWatermark int64
	ServerLow     int64
	// Per-group tuning, so backfill honours the same retention horizon and
	// pacing as the forward crawl (migration 013).
	RetentionDays int
	ThrottleMs    int
}

// groupsNeedingBackfill lists active groups not yet marked done whose backfill
// pointer is still above the server's oldest article.
func (s *PGStore) groupsNeedingBackfill(ctx context.Context, limit int) ([]backfillRow, error) {
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
			`SELECT name, COALESCE(back_watermark, high_watermark) AS back_watermark, server_low
			 FROM newsgroups
			 WHERE active = TRUE AND NOT backfill_done
			   AND COALESCE(back_watermark, high_watermark) > server_low
			 ORDER BY name LIMIT $1`, limit)
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

// updateBackWatermark lowers a group's backfill pointer and records the oldest
// posting date reached (kept if the batch had no dated articles).
func (s *PGStore) updateBackWatermark(ctx context.Context, name string, back int64, oldest time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var d sql.NullTime
		if !oldest.IsZero() {
			d = sql.NullTime{Time: oldest, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			   SET back_watermark = $2,
			       back_watermark_date = COALESCE($3, back_watermark_date)
			 WHERE name = $1`, name, back, d)
		return err
	})
}

func (s *PGStore) markBackfillDone(ctx context.Context, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE newsgroups SET backfill_done = TRUE WHERE name = $1`, name)
		return err
	})
}

// resetBackfill re-arms a group: backfill restarts just below the forward
// watermark and walks down again (dupes are ignored on insert).
func (s *PGStore) resetBackfill(ctx context.Context, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			   SET back_watermark = GREATEST(high_watermark - 1, server_low),
			       back_watermark_date = NULL, backfill_done = FALSE
			 WHERE name = $1`, name)
		return err
	})
}

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
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT id, title FROM nzbs`); err != nil {
			return err
		}
		var ids []int64
		for _, r := range rows {
			if isJunkTitle(r.Title) {
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
