package wiki

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// PGStore is the production Store over the shared connection pool
// (core.Storage.DB()). The SQL is byte-identical to the pre-
// extraction pkg/storage/postgres/wiki.go knowledge-base methods —
// the tables still live in the public schema until the PG17
// baseline consolidation moves them into a `wiki` schema.
type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// ── Topics ─────────────────────────────────────────────────────────

func (s *PGStore) Topics(ctx context.Context) ([]*Topic, error) {
	var topics []*Topic
	err := s.db.SelectContext(ctx, &topics,
		`SELECT t.id, t.name, t.slug, t.description, t.sort_order, t.icon, t.color, t.created_at, COUNT(p.id) AS post_count
		 FROM wiki_topics t
		 LEFT JOIN wiki_posts p ON p.topic_id = t.id
		 GROUP BY t.id
		 ORDER BY t.sort_order, t.name`)
	return topics, err
}

func (s *PGStore) TopicBySlug(ctx context.Context, slug string) (*Topic, error) {
	topic := &Topic{}
	err := s.db.GetContext(ctx, topic,
		`SELECT id, name, slug, description, sort_order, icon, color, created_at FROM wiki_topics WHERE slug = $1`,
		slug)
	if err != nil {
		return nil, err
	}
	return topic, nil
}

func (s *PGStore) CreateTopic(ctx context.Context, in TopicInput) (*Topic, error) {
	topic := &Topic{
		Name: in.Name, Slug: in.Slug, Description: in.Description,
		SortOrder: in.SortOrder, Icon: in.Icon, Color: in.Color,
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO wiki_topics (name, slug, description, sort_order, icon, color)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		in.Name, in.Slug, in.Description, in.SortOrder, in.Icon, in.Color,
	).Scan(&topic.ID, &topic.CreatedAt)
	if err != nil {
		return nil, err
	}
	return topic, nil
}

func (s *PGStore) UpdateTopic(ctx context.Context, id int, in TopicInput) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wiki_topics SET name=$2, slug=$3, description=$4, sort_order=$5, icon=$6, color=$7
		 WHERE id=$1`,
		id, in.Name, in.Slug, in.Description, in.SortOrder, in.Icon, in.Color)
	return err
}

func (s *PGStore) DeleteTopic(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM wiki_topics WHERE id = $1`, id)
	return err
}

// ── Posts ──────────────────────────────────────────────────────────

func (s *PGStore) PostsByTopic(ctx context.Context, topicID int) ([]*Post, error) {
	var posts []*Post
	err := s.db.SelectContext(ctx, &posts,
		`SELECT id, topic_id, title, slug, content, created_by, created_at, updated_at FROM wiki_posts WHERE topic_id = $1 ORDER BY created_at ASC`,
		topicID)
	return posts, err
}

// AllPosts excludes content from the SELECT so the sidebar nav
// doesn't pull 100KB markdown blobs for every row.
func (s *PGStore) AllPosts(ctx context.Context) ([]*Post, error) {
	var posts []*Post
	err := s.db.SelectContext(ctx, &posts,
		`SELECT id, topic_id, title, slug, '' AS content,
		        created_by, created_at, updated_at
		   FROM wiki_posts
		  ORDER BY topic_id ASC, created_at ASC`)
	return posts, err
}

// RecentPosts orders by updated_at — that catches both fresh posts
// and meaningful edits, which is the more useful "what's new"
// signal for readers than created_at alone.
func (s *PGStore) RecentPosts(ctx context.Context, limit int) ([]*RecentPost, error) {
	if limit <= 0 {
		limit = 10
	}
	var posts []*RecentPost
	err := s.db.SelectContext(ctx, &posts, `
		SELECT p.id, p.topic_id, p.title, p.slug, p.created_at, p.updated_at,
		       t.name AS topic_name, t.slug AS topic_slug,
		       COALESCE(u.username, '') AS created_by_username,
		       COALESCE(p.view_count, 0) AS view_count
		  FROM wiki_posts p
		  JOIN wiki_topics t ON t.id = p.topic_id
		  LEFT JOIN users u  ON u.id = p.created_by
		 ORDER BY p.updated_at DESC
		 LIMIT $1`, limit)
	return posts, err
}

// PopularPosts is the view-count-ranked twin of RecentPosts. Ties
// on view_count break to updated_at DESC so fresh posts beat stale
// ones at the same readership tier.
func (s *PGStore) PopularPosts(ctx context.Context, limit int) ([]*RecentPost, error) {
	if limit <= 0 {
		limit = 5
	}
	var posts []*RecentPost
	err := s.db.SelectContext(ctx, &posts, `
		SELECT p.id, p.topic_id, p.title, p.slug, p.created_at, p.updated_at,
		       t.name AS topic_name, t.slug AS topic_slug,
		       COALESCE(u.username, '') AS created_by_username,
		       COALESCE(p.view_count, 0) AS view_count
		  FROM wiki_posts p
		  JOIN wiki_topics t ON t.id = p.topic_id
		  LEFT JOIN users u  ON u.id = p.created_by
		 ORDER BY p.view_count DESC, p.updated_at DESC
		 LIMIT $1`, limit)
	return posts, err
}

func (s *PGStore) IncrementPostView(ctx context.Context, postID int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wiki_posts SET view_count = view_count + 1 WHERE id = $1`,
		postID)
	return err
}

// RandomPost uses ORDER BY RANDOM() — acceptable at wiki-table
// sizes (tens to low thousands of rows); revisit if the catalog
// ever crosses ~100k posts.
func (s *PGStore) RandomPost(ctx context.Context) (*RecentPost, error) {
	var p RecentPost
	err := s.db.GetContext(ctx, &p, `
		SELECT p.id, p.topic_id, p.title, p.slug, p.created_at, p.updated_at,
		       t.name AS topic_name, t.slug AS topic_slug,
		       COALESCE(u.username, '') AS created_by_username,
		       COALESCE(p.view_count, 0) AS view_count
		  FROM wiki_posts p
		  JOIN wiki_topics t ON t.id = p.topic_id
		  LEFT JOIN users u  ON u.id = p.created_by
		 ORDER BY RANDOM()
		 LIMIT 1`)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PGStore) PostBySlug(ctx context.Context, topicID int, slug string) (*Post, error) {
	post := &Post{}
	err := s.db.GetContext(ctx, post,
		`SELECT id, topic_id, title, slug, content, created_by, created_at, updated_at FROM wiki_posts WHERE topic_id = $1 AND slug = $2`,
		topicID, slug)
	if err != nil {
		return nil, err
	}
	return post, nil
}

func (s *PGStore) PostByID(ctx context.Context, id int) (*Post, error) {
	post := &Post{}
	err := s.db.GetContext(ctx, post,
		`SELECT id, topic_id, title, slug, content, created_by, created_at, updated_at FROM wiki_posts WHERE id = $1`,
		id)
	if err != nil {
		return nil, err
	}
	return post, nil
}

func (s *PGStore) CreatePost(ctx context.Context, topicID int, title, slug, content string, createdBy int) (*Post, error) {
	post := &Post{TopicID: topicID, Title: title, Slug: slug, Content: content, CreatedBy: createdBy}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO wiki_posts (topic_id, title, slug, content, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at, updated_at`,
		topicID, title, slug, content, createdBy,
	).Scan(&post.ID, &post.CreatedAt, &post.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return post, nil
}

func (s *PGStore) UpdatePost(ctx context.Context, id int, title, slug, content string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wiki_posts SET title=$2, slug=$3, content=$4, updated_at=NOW() WHERE id=$1`,
		id, title, slug, content)
	return err
}

func (s *PGStore) DeletePost(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM wiki_posts WHERE id = $1`, id)
	return err
}
