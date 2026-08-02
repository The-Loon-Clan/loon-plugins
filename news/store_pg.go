package news

// PGStore is the Postgres-backed implementation of
// PGStore. Extracted from *Storage in Phase 3.

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

func (r *PGStore) GetPublishedNewsPosts(ctx context.Context, limit int) ([]*NewsPost, error) {
	var posts []*NewsPost
	err := r.db.SelectContext(ctx, &posts,
		`SELECT id, title, slug, body, published, created_at, updated_at
		 FROM news_posts WHERE published = true ORDER BY created_at DESC LIMIT $1`, limit)
	return posts, err
}

func (r *PGStore) GetAllNewsPosts(ctx context.Context) ([]*NewsPost, error) {
	var posts []*NewsPost
	err := r.db.SelectContext(ctx, &posts,
		`SELECT id, title, slug, body, published, created_at, updated_at
		 FROM news_posts ORDER BY created_at DESC`)
	return posts, err
}

func (r *PGStore) GetNewsPostByID(ctx context.Context, id int64) (*NewsPost, error) {
	var p NewsPost
	err := r.db.GetContext(ctx, &p,
		`SELECT id, title, slug, body, published, created_at, updated_at FROM news_posts WHERE id = $1`, id)
	return &p, err
}

// GetNewsPostBySlug is the PUBLIC read, and it filters on published.
//
// It did not, and the only caller is the public /news/:slug page — so an
// unpublished post was readable by anyone who knew or guessed its slug. That
// was already wrong for a draft an admin was still writing; it becomes
// load-bearing now that an agent can create drafts (see ai.go), because the
// whole propose tier rests on a draft being inert until a human approves it.
//
// Filtered in the QUERY rather than checked in the handler: the guarantee then
// holds for every future caller instead of for every caller that remembers.
// Admin paths read by ID and are unaffected.
func (r *PGStore) GetNewsPostBySlug(ctx context.Context, slug string) (*NewsPost, error) {
	var p NewsPost
	err := r.db.GetContext(ctx, &p,
		`SELECT id, title, slug, body, published, created_at, updated_at
		   FROM news_posts WHERE slug = $1 AND published = TRUE`, slug)
	return &p, err
}

func (r *PGStore) CreateNewsPost(ctx context.Context, title, slug, body string, published bool) (*NewsPost, error) {
	var p NewsPost
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO news_posts (title, slug, body, published) VALUES ($1, $2, $3, $4)
		 RETURNING id, title, slug, body, published, created_at, updated_at`,
		title, slug, body, published,
	).Scan(&p.ID, &p.Title, &p.Slug, &p.Body, &p.Published, &p.CreatedAt, &p.UpdatedAt)
	return &p, err
}

func (r *PGStore) UpdateNewsPost(ctx context.Context, id int64, title, slug, body string, published bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE news_posts SET title=$1, slug=$2, body=$3, published=$4, updated_at=NOW() WHERE id=$5`,
		title, slug, body, published, id)
	return err
}

func (r *PGStore) DeleteNewsPost(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM news_posts WHERE id = $1`, id)
	return err
}
