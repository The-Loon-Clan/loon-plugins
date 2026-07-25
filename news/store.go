package news

import (
	"context"
)

// NewsPostRepository is the per-domain interface for the news_posts
// table — the admin-curated front-page announcement stream. Public
// reads filter on published=true; admin sees all rows. Phase 3
// extraction.
type Store interface {
	GetPublishedNewsPosts(ctx context.Context, limit int) ([]*NewsPost, error)
	GetAllNewsPosts(ctx context.Context) ([]*NewsPost, error)
	GetNewsPostByID(ctx context.Context, id int64) (*NewsPost, error)
	GetNewsPostBySlug(ctx context.Context, slug string) (*NewsPost, error)
	CreateNewsPost(ctx context.Context, title, slug, body string, published bool) (*NewsPost, error)
	UpdateNewsPost(ctx context.Context, id int64, title, slug, body string, published bool) error
	DeleteNewsPost(ctx context.Context, id int64) error
}
