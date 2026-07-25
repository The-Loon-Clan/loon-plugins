package forum

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// PGStore is the Postgres-backed implementation for
// the categories + admin sub-slice. Threads, posts, reactions,
// and sidebars land in a follow-up sub-slice.
type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

var _ Store = (*PGStore)(nil)

// ── Categories ─────────────────────────────────────────────────────

func (r *PGStore) GetForumCategories(ctx context.Context) ([]*ForumCategory, error) {
	var cats []*ForumCategory
	err := r.db.SelectContext(ctx, &cats, `
		SELECT fc.id, fc.name, fc.description, fc.ordinal, fc.color, fc.icon, fc.created_at,
		       COUNT(DISTINCT ft.id)   AS thread_count,
		       COUNT(DISTINCT fp.id)   AS post_count,
		       MAX(ft.last_post_at)    AS last_post_at,
		       lt.id    AS last_thread_id,
		       lt.title AS last_thread_title,
		       lu.username AS last_post_user
		FROM forum_categories fc
		LEFT JOIN forum_threads ft ON ft.category_id = fc.id
		LEFT JOIN forum_posts fp ON fp.thread_id = ft.id
		LEFT JOIN LATERAL (
		    SELECT id, title, last_post_at
		    FROM forum_threads
		    WHERE category_id = fc.id
		    ORDER BY last_post_at DESC
		    LIMIT 1
		) lt ON true
		LEFT JOIN LATERAL (
		    SELECT u.username
		    FROM forum_posts fp2
		    JOIN user_display u ON u.id = fp2.user_id
		    WHERE fp2.thread_id = lt.id
		    ORDER BY fp2.created_at DESC
		    LIMIT 1
		) lu ON true
		GROUP BY fc.id, lt.id, lt.title, lu.username
		ORDER BY fc.ordinal`)
	return cats, err
}

func (r *PGStore) GetForumCategory(ctx context.Context, id int) (*ForumCategory, error) {
	var cat ForumCategory
	err := r.db.GetContext(ctx, &cat, `
		SELECT fc.id, fc.name, fc.description, fc.ordinal, fc.color, fc.icon, fc.created_at,
		       COUNT(DISTINCT ft.id) AS thread_count,
		       COUNT(DISTINCT fp.id) AS post_count,
		       MAX(ft.last_post_at)  AS last_post_at
		FROM forum_categories fc
		LEFT JOIN forum_threads ft ON ft.category_id = fc.id
		LEFT JOIN forum_posts fp ON fp.thread_id = ft.id
		WHERE fc.id = $1
		GROUP BY fc.id`, id)
	return &cat, err
}

func (r *PGStore) CreateForumCategory(ctx context.Context, name, description string, ordinal int, icon, color string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO forum_categories (name, description, ordinal, icon, color) VALUES ($1, $2, $3, $4, $5)`,
		name, description, ordinal, icon, color)
	return err
}

func (r *PGStore) UpdateForumCategory(ctx context.Context, id int, name, description string, ordinal int, icon, color string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE forum_categories SET name = $2, description = $3, ordinal = $4, icon = $5, color = $6 WHERE id = $1`,
		id, name, description, ordinal, icon, color)
	return err
}

func (r *PGStore) DeleteForumCategory(ctx context.Context, id int) error {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forum_threads WHERE category_id = $1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("category has %d thread(s) — move or delete them first", n)
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM forum_categories WHERE id = $1`, id)
	return err
}

func (r *PGStore) MergeForumCategory(ctx context.Context, srcID, dstID int) error {
	if srcID == dstID {
		return fmt.Errorf("source and destination are the same category")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE forum_threads SET category_id = $1 WHERE category_id = $2`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM forum_categories WHERE id = $1`, srcID); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Threads ────────────────────────────────────────────────────────

func (r *PGStore) GetForumThreads(ctx context.Context, categoryID, limit, offset int) ([]*ForumThread, int, error) {
	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM forum_threads WHERE category_id = $1 AND hidden_at IS NULL`, categoryID); err != nil {
		return nil, 0, err
	}
	var threads []*ForumThread
	err := r.db.SelectContext(ctx, &threads, `
		SELECT ft.id, ft.category_id, ft.user_id,
		       u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		       COALESCE(ft.thread_type, 'discussion') AS thread_type,
		       ft.title, ft.pinned, ft.locked, ft.reply_count,
		       ft.last_post_at, ft.created_at,
		       lpu.id          AS last_post_user_id,
		       lpu.username    AS last_post_username,
		       lpu.role        AS last_post_role,
		       lpu.avatar_path AS last_post_avatar_path,
		       fc.name AS category_name
		FROM forum_threads ft
		JOIN user_display u  ON u.id  = ft.user_id
		JOIN forum_categories fc ON fc.id = ft.category_id
		LEFT JOIN LATERAL (
		    SELECT p.user_id
		      FROM forum_posts p
		     WHERE p.thread_id = ft.id
		       AND p.user_id   <> ft.user_id
		       AND p.hidden_at IS NULL
		     ORDER BY p.created_at DESC
		     LIMIT 1
		) lp ON true
		LEFT JOIN user_display lpu ON lpu.id = lp.user_id
		WHERE ft.category_id = $1 AND ft.hidden_at IS NULL
		ORDER BY ft.pinned DESC, ft.last_post_at DESC
		LIMIT $2 OFFSET $3`, categoryID, limit, offset)
	return threads, total, err
}

// GetRecentForumThreads returns the most-recently-active non-hidden
// threads across every category. Backs the home-page Community
// Spotlight card. Cheaper than GetForumThreads (no LATERAL last-
// reply lookup) — the spotlight only needs the OP + reply count
// + category name to render a row.
func (r *PGStore) GetRecentForumThreads(ctx context.Context, limit int) ([]*ForumThread, error) {
	if limit <= 0 {
		limit = 5
	}
	var threads []*ForumThread
	err := r.db.SelectContext(ctx, &threads, `
		SELECT ft.id, ft.category_id, ft.user_id,
		       u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		       COALESCE(ft.thread_type, 'discussion') AS thread_type,
		       ft.title, ft.pinned, ft.locked, ft.reply_count,
		       ft.last_post_at, ft.created_at,
		       fc.name AS category_name
		FROM forum_threads ft
		JOIN user_display u ON u.id = ft.user_id
		JOIN forum_categories fc ON fc.id = ft.category_id
		WHERE ft.hidden_at IS NULL
		ORDER BY ft.last_post_at DESC
		LIMIT $1`, limit)
	return threads, err
}

func (r *PGStore) GetForumThread(ctx context.Context, threadID int) (*ForumThread, error) {
	var t ForumThread
	err := r.db.GetContext(ctx, &t, `
		SELECT ft.id, ft.category_id, ft.user_id,
		       u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		       COALESCE(ft.thread_type, 'discussion') AS thread_type,
		       ft.title, ft.pinned, ft.locked, ft.reply_count,
		       ft.last_post_at, ft.created_at,
		       fc.name AS category_name
		FROM forum_threads ft
		JOIN user_display u ON u.id = ft.user_id
		JOIN forum_categories fc ON fc.id = ft.category_id
		WHERE ft.id = $1 AND ft.hidden_at IS NULL`, threadID)
	return &t, err
}

func (r *PGStore) CreateForumThread(ctx context.Context, categoryID, userID int, title, body, threadType string) (*ForumThread, error) {
	// Empty falls back to the DB column default ("discussion") so
	// legacy callers that don't yet pass a type continue working.
	if threadType == "" {
		threadType = ForumThreadTypeDiscussion
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var threadID int
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO forum_threads (category_id, user_id, title, thread_type) VALUES ($1, $2, $3, $4) RETURNING id`,
		categoryID, userID, title, threadType).Scan(&threadID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO forum_posts (thread_id, user_id, body) VALUES ($1, $2, $3)`,
		threadID, userID, body); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetForumThread(ctx, threadID)
}

func (r *PGStore) DeleteForumThread(ctx context.Context, threadID, userID int, isAdmin bool) error {
	if isAdmin {
		_, err := r.db.ExecContext(ctx, `DELETE FROM forum_threads WHERE id = $1`, threadID)
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM forum_threads WHERE id = $1 AND user_id = $2`, threadID, userID)
	return err
}

func (r *PGStore) SetThreadLocked(ctx context.Context, threadID int, locked bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE forum_threads SET locked = $1 WHERE id = $2`, locked, threadID)
	return err
}

func (r *PGStore) SetThreadPinned(ctx context.Context, threadID int, pinned bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE forum_threads SET pinned = $1 WHERE id = $2`, pinned, threadID)
	return err
}

// ── Posts ──────────────────────────────────────────────────────────

func (r *PGStore) GetForumPosts(ctx context.Context, threadID, limit, offset, viewerID int, canSeeAllReplies bool) ([]*ForumPost, int, error) {
	// Recruitment-thread visibility (mig 251): when the viewer
	// isn't admin/thread-author, restrict to the OP plus replies
	// authored by viewerID. Filter happens in SQL so other
	// applicants' rows never cross the wire. The OP is identified
	// as the lowest-id post in the thread — created_at is also
	// ascending but id is the safer monotonic key.
	visibilityClause := ""
	args := []interface{}{threadID, limit, offset}
	if !canSeeAllReplies {
		visibilityClause = ` AND (fp.id = (SELECT MIN(id) FROM forum_posts WHERE thread_id = $1) OR fp.user_id = $4)`
		args = append(args, viewerID)
	}

	var total int
	totalArgs := []interface{}{threadID}
	totalQuery := `SELECT COUNT(*) FROM forum_posts WHERE thread_id = $1 AND hidden_at IS NULL`
	if !canSeeAllReplies {
		totalQuery += ` AND (id = (SELECT MIN(id) FROM forum_posts WHERE thread_id = $1) OR user_id = $2)`
		totalArgs = append(totalArgs, viewerID)
	}
	if err := r.db.GetContext(ctx, &total, totalQuery, totalArgs...); err != nil {
		return nil, 0, err
	}
	var posts []*ForumPost
	// sqllint:allow visibilityClause is a fixed literal — either "" or the exact AND-clause above with $1/$4 placeholders, never user-controlled
	err := r.db.SelectContext(ctx, &posts, `
		SELECT fp.id, fp.thread_id, fp.user_id, u.username, COALESCE(u.role,'user') AS user_role,
		       COALESCE(u.reputation_tier,0) AS reputation_tier,
		       COALESCE(u.avatar_path,'') AS avatar_path,
		       fp.body, fp.edited_at, fp.created_at,
		       fp.quoted_post_id,
		       qu.username AS quoted_username,
		       SUBSTRING(qp.body FROM 1 FOR 280) AS quoted_body_excerpt
		FROM forum_posts fp
		JOIN user_display u ON u.id = fp.user_id
		LEFT JOIN forum_posts qp ON qp.id = fp.quoted_post_id
		LEFT JOIN user_display qu ON qu.id = qp.user_id
		WHERE fp.thread_id = $1 AND fp.hidden_at IS NULL`+visibilityClause+`
		ORDER BY fp.created_at ASC, fp.id ASC
		LIMIT $2 OFFSET $3`, args...)
	if err != nil {
		return nil, 0, err
	}
	if len(posts) == 0 {
		return posts, total, nil
	}

	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}

	type reactRow struct {
		PostID int64  `db:"post_id"`
		Emoji  string `db:"emoji"`
		Count  int    `db:"count"`
	}
	var counts []reactRow
	if err := r.db.SelectContext(ctx, &counts, `
		SELECT post_id, emoji, COUNT(*)::int AS count
		FROM forum_post_reactions
		WHERE post_id = ANY($1)
		GROUP BY post_id, emoji
		ORDER BY post_id, emoji`, pq.Array(ids)); err != nil {
		return nil, 0, err
	}
	byPost := make(map[int64][]ForumReactionCount, len(posts))
	for _, rr := range counts {
		byPost[rr.PostID] = append(byPost[rr.PostID], ForumReactionCount{Emoji: rr.Emoji, Count: rr.Count})
	}

	mineByPost := make(map[int64]map[string]bool, len(posts))
	if viewerID > 0 {
		type myRow struct {
			PostID int64  `db:"post_id"`
			Emoji  string `db:"emoji"`
		}
		var mine []myRow
		if err := r.db.SelectContext(ctx, &mine, `
			SELECT post_id, emoji
			FROM forum_post_reactions
			WHERE user_id = $1 AND post_id = ANY($2)`, viewerID, pq.Array(ids)); err != nil {
			return nil, 0, err
		}
		for _, mr := range mine {
			if mineByPost[mr.PostID] == nil {
				mineByPost[mr.PostID] = make(map[string]bool, 4)
			}
			mineByPost[mr.PostID][mr.Emoji] = true
		}
	}

	for _, p := range posts {
		p.Reactions = byPost[p.ID]
		if m := mineByPost[p.ID]; m != nil {
			p.MyReactions = make([]string, 0, len(m))
			for emoji := range m {
				p.MyReactions = append(p.MyReactions, emoji)
			}
		}
	}
	return posts, total, nil
}

func (r *PGStore) CreateForumPost(ctx context.Context, threadID, userID int, body string, quotedPostID *int) (*ForumPost, error) {
	var post ForumPost
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO forum_posts (thread_id, user_id, body, quoted_post_id) VALUES ($1, $2, $3, $4)
		 RETURNING id, thread_id, user_id, body, edited_at, created_at`,
		threadID, userID, body, quotedPostID).Scan(
		&post.ID, &post.ThreadID, &post.UserID, &post.Body, &post.EditedAt, &post.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, _ = r.db.ExecContext(ctx,
		`UPDATE forum_threads SET reply_count = reply_count + 1, last_post_at = NOW() WHERE id = $1`,
		threadID)
	return &post, nil
}

func (r *PGStore) UpdateForumPost(ctx context.Context, postID int64, userID int, body string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE forum_posts SET body = $1, edited_at = NOW() WHERE id = $2 AND user_id = $3`,
		body, postID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("post not found or not owned by user")
	}
	return nil
}

func (r *PGStore) DeleteForumPost(ctx context.Context, postID int64, userID int, isAdmin bool) error {
	if isAdmin {
		_, err := r.db.ExecContext(ctx, `DELETE FROM forum_posts WHERE id = $1`, postID)
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM forum_posts WHERE id = $1 AND user_id = $2`, postID, userID)
	return err
}

// ── Reactions ──────────────────────────────────────────────────────

func (r *PGStore) ToggleForumPostReaction(ctx context.Context, postID int64, userID int, emoji string) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM forum_post_reactions WHERE post_id = $1 AND user_id = $2 AND emoji = $3`,
		postID, userID, emoji)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO forum_post_reactions (post_id, user_id, emoji) VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		postID, userID, emoji); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PGStore) GetForumPostReactionCounts(ctx context.Context, postID int64) ([]ForumReactionCount, error) {
	var rows []ForumReactionCount
	err := r.db.SelectContext(ctx, &rows, `
		SELECT emoji, COUNT(*)::int AS count
		FROM forum_post_reactions
		WHERE post_id = $1
		GROUP BY emoji
		ORDER BY emoji`, postID)
	return rows, err
}

// ── Sidebars ──────────────────────────────────────────────────────

func (r *PGStore) GetRecentForumActivity(ctx context.Context, limit int) ([]*ForumActivityItem, error) {
	var items []*ForumActivityItem
	err := r.db.SelectContext(ctx, &items, `
		SELECT fp.id AS post_id, ft.id AS thread_id, ft.title AS thread_title,
		       fp.user_id, u.username, u.role,
		       COALESCE(u.avatar_path, '') AS avatar_path,
		       fc.id    AS category_id,
		       COALESCE(fc.icon, '')  AS category_icon,
		       COALESCE(fc.color, '') AS category_color,
		       fp.created_at
		FROM forum_posts fp
		JOIN forum_threads ft     ON ft.id = fp.thread_id
		JOIN user_display u              ON u.id  = fp.user_id
		JOIN forum_categories fc  ON fc.id = ft.category_id
		WHERE fp.hidden_at IS NULL AND ft.hidden_at IS NULL
		ORDER BY fp.created_at DESC
		LIMIT $1`, limit)
	return items, err
}

func (r *PGStore) GetTopForumContributors(ctx context.Context, limit int) ([]*ForumContributor, error) {
	var rows []*ForumContributor
	err := r.db.SelectContext(ctx, &rows, `
		SELECT fp.user_id, u.username, u.role,
		       COALESCE(u.avatar_path, '') AS avatar_path,
		       COUNT(*) AS post_count
		FROM forum_posts fp
		JOIN user_display u ON u.id = fp.user_id
		WHERE fp.hidden_at IS NULL
		GROUP BY fp.user_id, u.username, u.role, u.avatar_path
		ORDER BY post_count DESC
		LIMIT $1`, limit)
	return rows, err
}

// PostContext returns the author + thread for a single post.
// Ported from the notification repository's GetForumPostContext
// when the forum became a plugin — it is forum SQL. Returns a
// wrapped sql.ErrNoRows if the post doesn't exist; callers in the
// notification path swallow the error and skip the notification.
func (r *PGStore) PostContext(ctx context.Context, postID int64) (*PostContext, error) {
	var p PostContext
	err := r.db.GetContext(ctx, &p, `
		SELECT p.id, p.user_id, p.thread_id, COALESCE(t.title, '') AS thread_title
		FROM forum_posts p
		LEFT JOIN forum_threads t ON t.id = p.thread_id
		WHERE p.id = $1`, postID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
