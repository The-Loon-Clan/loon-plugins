package wiki

import "context"

// Store is the plugin's own storage seam — the topics + posts
// surface that used to be the knowledge-base half of
// storage.WikiRepository before the plugin extraction. Two impls:
// PGStore (production, over the shared pool from
// core.Storage.DB()) and MemStore (in-memory, for tests).
//
// Absence contract matches the old repo: single-row lookups
// return sql.ErrNoRows on miss (callers map to 404); mutating
// calls on missing rows are zero-rows-affected no-ops.
type Store interface {
	// ── Topics ────────────────────────────────────────────────────

	// Topics returns every topic with a joined post_count,
	// ordered by sort_order ASC then name ASC.
	Topics(ctx context.Context) ([]*Topic, error)

	// TopicBySlug fetches one topic. sql.ErrNoRows on miss.
	TopicBySlug(ctx context.Context, slug string) (*Topic, error)

	// CreateTopic inserts a new topic and returns the persisted
	// row with server-stamped id + created_at.
	CreateTopic(ctx context.Context, name, slug, description string, sortOrder int) (*Topic, error)

	// UpdateTopic overwrites name/slug/description/sort_order.
	UpdateTopic(ctx context.Context, id int, name, slug, description string, sortOrder int) error

	// DeleteTopic hard-deletes the row. FK cascades drop
	// dependent wiki_posts.
	DeleteTopic(ctx context.Context, id int) error

	// ── Posts ─────────────────────────────────────────────────────

	// PostsByTopic returns every post in one topic, ordered
	// created_at ASC (chronological reading order).
	PostsByTopic(ctx context.Context, topicID int) ([]*Post, error)

	// AllPosts returns every post across every topic for the
	// sidebar tree, ordered topic_id ASC then created_at ASC.
	// Content is intentionally NOT materialised — pulling 100KB
	// markdown blobs for a nav panel is wasted bandwidth.
	AllPosts(ctx context.Context) ([]*Post, error)

	// RecentPosts returns the N most-recently-updated posts across
	// every topic (edits surface alongside fresh posts).
	// limit <= 0 defaults to 10.
	RecentPosts(ctx context.Context, limit int) ([]*RecentPost, error)

	// PopularPosts returns the N most-viewed posts, ties broken by
	// updated_at DESC. limit <= 0 defaults to 5.
	PopularPosts(ctx context.Context, limit int) ([]*RecentPost, error)

	// IncrementPostView bumps view_count by 1. Fire-and-forget
	// from the Post handler — failures only mean the popular
	// ranking drifts. No-op on missing rows.
	IncrementPostView(ctx context.Context, postID int) error

	// RandomPost returns one randomly chosen post for the "Random
	// Page" shortcut. sql.ErrNoRows when the table is empty.
	RandomPost(ctx context.Context) (*RecentPost, error)

	// PostBySlug returns one post matching (topic_id, slug).
	// sql.ErrNoRows on miss.
	PostBySlug(ctx context.Context, topicID int, slug string) (*Post, error)

	// PostByID returns one post by primary key. sql.ErrNoRows on
	// miss.
	PostByID(ctx context.Context, id int) (*Post, error)

	// CreatePost inserts a new post. Returns the persisted row
	// with server-stamped id + created_at + updated_at.
	CreatePost(ctx context.Context, topicID int, title, slug, content string, createdBy int) (*Post, error)

	// UpdatePost overwrites title/slug/content and bumps
	// updated_at.
	UpdatePost(ctx context.Context, id int, title, slug, content string) error

	// DeletePost hard-deletes the row.
	DeletePost(ctx context.Context, id int) error
}
