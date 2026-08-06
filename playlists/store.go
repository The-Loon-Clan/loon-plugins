package playlists

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// ErrSlugTaken is returned when a slug is already in use. A sentinel rather
// than a string match, so the handler can offer "pick another" without
// inspecting driver errors.
var ErrSlugTaken = errors.New("playlists: slug already taken")

// Store is the plugin's persistence contract. The plugin depends on this, not
// on a concrete DB, so the backing is swappable and mockable — the same rule
// the catalog and store plugins follow.
type Store interface {
	Create(ctx context.Context, p *Playlist) error
	Update(ctx context.Context, p *Playlist) error
	Delete(ctx context.Context, id, userID int64) error
	BySlug(ctx context.Context, slug string) (*Playlist, error)
	// ListVisible returns public playlists plus every playlist owned by
	// viewerID. A viewerID of 0 (anonymous) gets public ones only.
	ListVisible(ctx context.Context, viewerID int64, limit, offset int) ([]*Playlist, int, error)
	ListItems(ctx context.Context, playlistID int64) ([]*Item, error)
	AddItem(ctx context.Context, playlistID, releaseID int64, note string) error
	RemoveItem(ctx context.Context, playlistID, itemID int64) error
}

// PGStore is the Postgres implementation, schema-scoped via core.SchemaDB so
// the unqualified table names resolve inside the "playlists" schema.
type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) Create(ctx context.Context, p *Playlist) error {
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`INSERT INTO playlists (user_id, slug, name, description, cover_url, public)
			 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at, updated_at`,
			p.UserID, p.Slug, p.Name, p.Description, p.CoverURL, p.Public,
		).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	})
	// The slug is UNIQUE in the schema, so a race between two creates is
	// resolved by the database rather than by a check-then-insert that a
	// concurrent request can slip between.
	if err != nil && strings.Contains(err.Error(), "playlists_slug_key") {
		return ErrSlugTaken
	}
	return err
}

// Update writes the editable fields. The WHERE carries user_id so a mismatched
// owner updates nothing — authorisation enforced in the statement rather than
// only in the handler that calls it.
func (s *PGStore) Update(ctx context.Context, p *Playlist) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE playlists SET name = $3, description = $4, cover_url = $5,
			        public = $6, updated_at = now()
			  WHERE id = $1 AND user_id = $2`,
			p.ID, p.UserID, p.Name, p.Description, p.CoverURL, p.Public)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("playlists: no playlist %d owned by %d", p.ID, p.UserID)
		}
		return nil
	})
}

func (s *PGStore) Delete(ctx context.Context, id, userID int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM playlists WHERE id = $1 AND user_id = $2`, id, userID)
		return err
	})
}

func (s *PGStore) BySlug(ctx context.Context, slug string) (*Playlist, error) {
	var p Playlist
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &p,
			`SELECT p.id, p.user_id, p.slug, p.name, p.description, p.cover_url,
			        p.public, p.created_at, p.updated_at,
			        (SELECT COUNT(*) FROM items i WHERE i.playlist_id = p.id) AS item_count
			   FROM playlists p WHERE p.slug = $1`, slug)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func (s *PGStore) ListVisible(ctx context.Context, viewerID int64, limit, offset int) ([]*Playlist, int, error) {
	var rows []*Playlist
	var total int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// One visibility predicate, written once and used by both statements —
		// a count that disagreed with the page would paginate into emptiness.
		if err := tx.GetContext(ctx, &total,
			`SELECT COUNT(*) FROM playlists WHERE public OR user_id = $1`, viewerID); err != nil {
			return err
		}
		return tx.SelectContext(ctx, &rows,
			`SELECT p.id, p.user_id, p.slug, p.name, p.description, p.cover_url,
			        p.public, p.created_at, p.updated_at,
			        (SELECT COUNT(*) FROM items i WHERE i.playlist_id = p.id) AS item_count
			   FROM playlists p
			  WHERE p.public OR p.user_id = $1
			  ORDER BY p.updated_at DESC LIMIT $2 OFFSET $3`, viewerID, limit, offset)
	})
	return rows, total, err
}

func (s *PGStore) ListItems(ctx context.Context, playlistID int64) ([]*Item, error) {
	var rows []*Item
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT id, playlist_id, release_id, position, note, added_at
			   FROM items WHERE playlist_id = $1
			  ORDER BY position ASC, added_at ASC`, playlistID)
	})
	return rows, err
}

// AddItem appends a release. Adding one that is already present is a NO-OP
// rather than an error: the caller's intent ("this release is in this list")
// already holds, and a duplicate-key error would make the obvious UI — an
// "add" button that might be pressed twice — need special handling.
func (s *PGStore) AddItem(ctx context.Context, playlistID, releaseID int64, note string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var pos int
		// COALESCE so the first item in an empty playlist gets 0 rather than
		// a NULL scan error.
		if err := tx.GetContext(ctx, &pos,
			`SELECT COALESCE(MAX(position)+1, 0) FROM items WHERE playlist_id = $1`,
			playlistID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO items (playlist_id, release_id, position, note)
			 VALUES ($1,$2,$3,$4) ON CONFLICT (playlist_id, release_id) DO NOTHING`,
			playlistID, releaseID, pos, note); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE playlists SET updated_at = now() WHERE id = $1`, playlistID)
		return err
	})
}

func (s *PGStore) RemoveItem(ctx context.Context, playlistID, itemID int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// playlist_id is in the WHERE as well as the id: without it, an item id
		// from another playlist would delete a row the caller does not own.
		_, err := tx.ExecContext(ctx,
			`DELETE FROM items WHERE id = $1 AND playlist_id = $2`, itemID, playlistID)
		return err
	})
}

// slugify turns a name into a URL-safe slug. Kept here rather than in the
// handler so the rule and the UNIQUE constraint it feeds stay together.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := true // leading dashes suppressed
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	if out == "" {
		// A name of pure punctuation still needs an address. Time-based rather
		// than random so it is reproducible in a test.
		out = fmt.Sprintf("list-%d", time.Now().UnixNano()%1e9)
	}
	return out
}
