// Package forum is the site-wide discussion board — admin-curated
// categories → threads → replies with Discord-style reaction bars,
// plus the recruitment-thread visibility mode (mig 251). Second
// pkg/core plugin after the wiki: models, storage, handlers, admin
// category management, and route registration all live here, wired
// through the Core mediator at Provision time.
//
// NOT in this package: communities (/c/* — Reddit-style user-owned
// sub-forums; a separate domain on its own tables), the member
// directory (/community/members — reads the User repo), and the
// community-moderation auto-hide machinery (content_hide), which
// writes forum_threads/forum_posts.hidden_at from the host's
// generic report system. The forum SQL respects hidden_at; the
// host owns setting it.
//
// Tables (forum_categories, forum_threads, forum_posts,
// forum_post_reactions) stay in the public schema until the PG17
// baseline consolidation, same as the wiki plugin.
package forum

import "context"

// Store is the plugin's storage seam — the full former
// storage.ForumRepository surface plus PostContext (which
// previously lived on the notification repository because the
// quote/like dispatch needed it; it is forum SQL, so it moved
// here with the plugin). Method names kept verbatim from the
// old repository so the handler port stays mechanical.
//
// Two impls: PGStore (production, over core.Storage.DB()) and
// MemStore (in-memory, for tests).
type Store interface {
	// ── Categories ────────────────────────────────────────────────

	// GetForumCategories returns every category ordered by
	// ordinal ASC, with joined thread_count + post_count +
	// last_post_at, plus a LATERAL preview of the latest
	// thread + last-post user for the category-list landing
	// page card.
	GetForumCategories(ctx context.Context) ([]*ForumCategory, error)

	// GetForumCategory returns one category by id with the
	// same aggregate counts minus the latest-thread preview.
	// sql.ErrNoRows on miss.
	GetForumCategory(ctx context.Context, id int) (*ForumCategory, error)

	// CreateForumCategory inserts a new category. name has a
	// UNIQUE index (migration 196) so a duplicate insert
	// errors at the DB layer; the handler surfaces that to the
	// admin as a flash message. icon is a Bootstrap Icons name
	// (rendered as bi-<icon>), color a host palette name — the
	// handler validates/defaults both before they get here.
	CreateForumCategory(ctx context.Context, name, description string, ordinal int, icon, color string) error

	// UpdateForumCategory overwrites name + description +
	// ordinal + icon + color on the category row.
	UpdateForumCategory(ctx context.Context, id int, name, description string, ordinal int, icon, color string) error

	// DeleteForumCategory removes one category. Refuses if
	// any thread points at it (the forum_threads FK is ON
	// DELETE CASCADE, which would silently vaporise threads +
	// posts otherwise).
	DeleteForumCategory(ctx context.Context, id int) error

	// MergeForumCategory re-points every forum_thread on
	// srcID to dstID, then deletes the now-empty source.
	// Both ids must exist and differ.
	MergeForumCategory(ctx context.Context, srcID, dstID int) error

	// ── Threads ───────────────────────────────────────────────────

	// GetForumThreads returns one page of threads for a
	// category, ordered pinned DESC then last_post_at DESC.
	// Excludes hidden_at IS NOT NULL rows. Joins the OP user
	// and a LATERAL last-reply user. Returns (rows, total, error).
	GetForumThreads(ctx context.Context, categoryID, limit, offset int) ([]*ForumThread, int, error)

	// GetRecentForumThreads returns the N most-recently-active
	// non-hidden threads across every category. Drives the
	// Community Spotlight card on the public home page (via the
	// SpotlightName extension this plugin publishes at Provision).
	GetRecentForumThreads(ctx context.Context, limit int) ([]*ForumThread, error)

	// GetForumThread returns one thread by id. hidden_at IS
	// NULL gate — a community-hidden thread returns
	// sql.ErrNoRows so direct URL navigation 404s.
	GetForumThread(ctx context.Context, threadID int) (*ForumThread, error)

	// CreateForumThread inserts the thread row + its first
	// forum_post in one transaction. threadType is one of the
	// ForumThreadType* constants; empty falls back to
	// "discussion" (the DB default).
	CreateForumThread(ctx context.Context, categoryID, userID int, title, body, threadType string) (*ForumThread, error)

	// DeleteForumThread hard-deletes a thread. Admin path
	// bypasses the owner gate; non-admin path matches on
	// (id, user_id) so a non-owner request silently affects
	// zero rows.
	DeleteForumThread(ctx context.Context, threadID, userID int, isAdmin bool) error

	// SetThreadLocked flips the locked flag (mod-only at the
	// route layer). No-op on missing id.
	SetThreadLocked(ctx context.Context, threadID int, locked bool) error

	// SetThreadPinned flips the pinned flag (mod-only).
	// Pinned threads sort first within their category.
	SetThreadPinned(ctx context.Context, threadID int, pinned bool) error

	// ── Posts ─────────────────────────────────────────────────────

	// GetForumPosts returns one page of posts in a thread,
	// ordered created_at ASC, hidden rows excluded, with the
	// quoted-post excerpt joined and reactions batch-loaded.
	// canSeeAllReplies gates recruitment-thread visibility —
	// false returns only the OP plus replies authored by
	// viewerID.
	GetForumPosts(ctx context.Context, threadID, limit, offset int, viewerID int, canSeeAllReplies bool) ([]*ForumPost, int, error)

	// CreateForumPost inserts a reply (optional quote link) and
	// bumps the parent thread's reply_count + last_post_at.
	CreateForumPost(ctx context.Context, threadID, userID int, body string, quotedPostID *int) (*ForumPost, error)

	// UpdateForumPost rewrites a post's body and stamps
	// edited_at. Owner-only gate — (postID, userID) mismatch
	// errors, defeating IDOR via crafted post ids.
	UpdateForumPost(ctx context.Context, postID int64, userID int, body string) error

	// DeleteForumPost hard-deletes a post. Admin path bypasses
	// the owner gate.
	DeleteForumPost(ctx context.Context, postID int64, userID int, isAdmin bool) error

	// PostContext returns the lightweight (author, thread)
	// projection the quote/like notification dispatch needs.
	// sql.ErrNoRows when the post doesn't exist — callers skip
	// the notification.
	PostContext(ctx context.Context, postID int64) (*PostContext, error)

	// ── Reactions ─────────────────────────────────────────────────

	// ToggleForumPostReaction flips one (post, user, emoji)
	// row. Returns added=true when the row was inserted, false
	// when the existing row was removed.
	ToggleForumPostReaction(ctx context.Context, postID int64, userID int, emoji string) (added bool, err error)

	// GetForumPostReactionCounts returns per-emoji counts for
	// one post. Powers the AJAX reaction endpoint.
	GetForumPostReactionCounts(ctx context.Context, postID int64) ([]ForumReactionCount, error)

	// ── Sidebars ──────────────────────────────────────────────────

	// GetRecentForumActivity returns the newest visible posts
	// site-wide for the "Recent Activity" card. Hidden posts
	// AND posts inside hidden threads are excluded.
	GetRecentForumActivity(ctx context.Context, limit int) ([]*ForumActivityItem, error)

	// GetTopForumContributors returns the top-N users by total
	// visible post count. Hidden posts don't count.
	GetTopForumContributors(ctx context.Context, limit int) ([]*ForumContributor, error)
}

// PostContext is the lightweight projection of a forum_posts row
// the quote/like notification path needs — just the author id +
// the thread id/title, no body or reactions.
type PostContext struct {
	PostID     int64  `db:"id"`
	AuthorID   int    `db:"user_id"`
	ThreadID   int64  `db:"thread_id"`
	ThreadName string `db:"thread_title"`
}
