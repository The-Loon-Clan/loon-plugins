package catalog

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/core"
)

// Store is catalog's persistence contract (entry upserts, release covers, and
// the admin category on/off set). The plugin depends on this interface, not a
// concrete DB, so the backend is swappable + mockable. PGStore is the Postgres
// impl; the in-memory mock lives in store_test.go.
type Store interface {
	UpsertEntry(ctx context.Context, e catalog.CatalogEntry) error
	SetReleaseCover(ctx context.Context, releaseID int64, coverURL string) error
	ReleaseCover(ctx context.Context, releaseID int64) (string, bool, error)
	ReleaseCovers(ctx context.Context, releaseIDs []int64) (map[int64]string, error)
	DisabledSet(ctx context.Context) (map[int]bool, error)
	SetEnabled(ctx context.Context, categoryID int, enabled bool) error
}

// PGStore is the Postgres implementation of Store (schema-scoped via SchemaDB).
type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// UpsertEntry persists a scraped catalog entry, deduped on (kind, external id).
func (s *PGStore) UpsertEntry(ctx context.Context, e catalog.CatalogEntry) error {
	ns, extID := "", ""
	if len(e.External) > 0 {
		ns, extID = e.External[0].Namespace, e.External[0].Value
	}
	fields, _ := json.Marshal(e.Fields)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO catalog_entry (kind, ext_namespace, ext_id, title, norm_title, cover_url, year, fields, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
			 ON CONFLICT (kind, ext_namespace, ext_id) DO UPDATE SET
			   title = EXCLUDED.title, norm_title = EXCLUDED.norm_title,
			   cover_url = EXCLUDED.cover_url, year = EXCLUDED.year,
			   fields = EXCLUDED.fields, updated_at = now()`,
			e.Ref.Kind, ns, extID, e.Title, catalog.DefaultNormalize(e.Title), e.CoverURL, e.Year, fields)
		return err
	})
}

func (s *PGStore) SetReleaseCover(ctx context.Context, releaseID int64, coverURL string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO release_cover (release_id, cover_url, updated_at) VALUES ($1, $2, now())
			 ON CONFLICT (release_id) DO UPDATE SET cover_url = EXCLUDED.cover_url, updated_at = now()`,
			releaseID, coverURL)
		return err
	})
}

func (s *PGStore) ReleaseCover(ctx context.Context, releaseID int64) (string, bool, error) {
	var url string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		e := tx.QueryRowContext(ctx, `SELECT cover_url FROM release_cover WHERE release_id = $1`, releaseID).Scan(&url)
		if e == sql.ErrNoRows {
			return nil
		}
		return e
	})
	return url, url != "", err
}

// ReleaseCovers reads many covers in ONE query — the batch shape behind
// pluginapi.CatalogCoverBatch, so a home page rendering a poster strip plus a
// listing plus a sidebar does not turn into 30+ round trips. release_cover is
// keyed by release_id (PRIMARY KEY, see migrations/002_entries.sql), so the
// ANY() lookup is an index scan.
//
// Ids with no cover row — or a blank one, matching ReleaseCover's ok=false —
// are absent from the map; a short map is normal, not an error.
func (s *PGStore) ReleaseCovers(ctx context.Context, releaseIDs []int64) (map[int64]string, error) {
	if len(releaseIDs) == 0 {
		return map[int64]string{}, nil
	}
	// Dedupe: callers stitch ids together from several page sections and the
	// same release can legitimately appear in more than one of them.
	ids := make([]int64, 0, len(releaseIDs))
	seen := make(map[int64]struct{}, len(releaseIDs))
	for _, id := range releaseIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	out := make(map[int64]string, len(ids))
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT release_id, cover_url FROM release_cover WHERE release_id = ANY($1)`,
			pq.Array(ids))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id  int64
				url string
			)
			if err := rows.Scan(&id, &url); err != nil {
				return err
			}
			if url != "" {
				out[id] = url
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DisabledSet returns the top-level category ids an admin has turned off.
func (s *PGStore) DisabledSet(ctx context.Context) (map[int]bool, error) {
	out := map[int]bool{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var ids []int
		if err := tx.SelectContext(ctx, &ids, `SELECT category_id FROM category_disabled`); err != nil {
			return err
		}
		for _, id := range ids {
			out[id] = true
		}
		return nil
	})
	return out, err
}

// SetEnabled turns a top-level category on (delete the disabled row) or off
// (insert one).
func (s *PGStore) SetEnabled(ctx context.Context, categoryID int, enabled bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if enabled {
			_, err := tx.ExecContext(ctx, `DELETE FROM category_disabled WHERE category_id = $1`, categoryID)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO category_disabled (category_id) VALUES ($1) ON CONFLICT DO NOTHING`, categoryID)
		return err
	})
}
