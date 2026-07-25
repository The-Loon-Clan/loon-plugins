package wiki

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"
)

// MemStore is the in-memory Store for tests — ported from the old
// pkg/storage/mock wiki knowledge-base half. Deliberately thin:
// maps + mutex, no transaction simulation. Storage-layer
// correctness (SQL casts, JOINs) stays with integration tests
// against a real Postgres.
type MemStore struct {
	mu sync.RWMutex

	topics    map[int]*Topic
	posts     map[int]*Post
	nextTopic int
	nextPost  int

	clock func() time.Time
}

func NewMemStore() *MemStore {
	return &MemStore{
		topics: map[int]*Topic{},
		posts:  map[int]*Post{},
		clock:  func() time.Time { return time.Now().UTC() },
	}
}

var _ Store = (*MemStore)(nil)

// SetClock pins created_at / updated_at for deterministic tests.
func (s *MemStore) SetClock(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = fn
}

func cloneTopic(t *Topic) *Topic {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func clonePost(p *Post) *Post {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// ── Topics ─────────────────────────────────────────────────────────

func (s *MemStore) Topics(ctx context.Context) ([]*Topic, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[int]int{}
	for _, p := range s.posts {
		counts[p.TopicID]++
	}
	out := make([]*Topic, 0, len(s.topics))
	for _, t := range s.topics {
		c := cloneTopic(t)
		c.PostCount = counts[c.ID]
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *MemStore) TopicBySlug(ctx context.Context, slug string) (*Topic, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.topics {
		if t.Slug == slug {
			return cloneTopic(t), nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *MemStore) CreateTopic(ctx context.Context, name, slug, description string, sortOrder int) (*Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextTopic++
	t := &Topic{
		ID:          s.nextTopic,
		Name:        name,
		Slug:        slug,
		Description: description,
		SortOrder:   sortOrder,
		CreatedAt:   s.clock(),
	}
	s.topics[t.ID] = t
	return cloneTopic(t), nil
}

func (s *MemStore) UpdateTopic(ctx context.Context, id int, name, slug, description string, sortOrder int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.topics[id]
	if !ok {
		// Production runs an UPDATE that affects 0 rows on miss — same here.
		return nil
	}
	t.Name = name
	t.Slug = slug
	t.Description = description
	t.SortOrder = sortOrder
	return nil
}

func (s *MemStore) DeleteTopic(ctx context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.topics, id)
	// Cascade — drop posts in the deleted topic, matching the
	// production FK ON DELETE CASCADE.
	for pid, p := range s.posts {
		if p.TopicID == id {
			delete(s.posts, pid)
		}
	}
	return nil
}

// ── Posts ──────────────────────────────────────────────────────────

func (s *MemStore) PostsByTopic(ctx context.Context, topicID int) ([]*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*Post{}
	for _, p := range s.posts {
		if p.TopicID == topicID {
			out = append(out, clonePost(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// AllPosts mirrors the production projection — content is blanked
// out so callers see the same lightweight rows.
func (s *MemStore) AllPosts(ctx context.Context) ([]*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Post, 0, len(s.posts))
	for _, p := range s.posts {
		c := clonePost(p)
		c.Content = ""
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TopicID != out[j].TopicID {
			return out[i].TopicID < out[j].TopicID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemStore) joinRecent(p *Post, t *Topic) *RecentPost {
	return &RecentPost{
		ID:        p.ID,
		TopicID:   p.TopicID,
		Title:     p.Title,
		Slug:      p.Slug,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		TopicName: t.Name,
		TopicSlug: t.Slug,
		ViewCount: p.ViewCount,
	}
}

func (s *MemStore) RecentPosts(ctx context.Context, limit int) ([]*RecentPost, error) {
	if limit <= 0 {
		limit = 10
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type joined struct {
		post  *Post
		topic *Topic
	}
	rows := []joined{}
	for _, p := range s.posts {
		t, ok := s.topics[p.TopicID]
		if !ok {
			continue
		}
		rows = append(rows, joined{post: p, topic: t})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].post.UpdatedAt.After(rows[j].post.UpdatedAt) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]*RecentPost, 0, len(rows))
	for _, j := range rows {
		out = append(out, s.joinRecent(j.post, j.topic))
	}
	return out, nil
}

func (s *MemStore) PopularPosts(ctx context.Context, limit int) ([]*RecentPost, error) {
	if limit <= 0 {
		limit = 5
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type joined struct {
		post  *Post
		topic *Topic
	}
	rows := []joined{}
	for _, p := range s.posts {
		t, ok := s.topics[p.TopicID]
		if !ok {
			continue
		}
		rows = append(rows, joined{post: p, topic: t})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].post.ViewCount != rows[j].post.ViewCount {
			return rows[i].post.ViewCount > rows[j].post.ViewCount
		}
		return rows[i].post.UpdatedAt.After(rows[j].post.UpdatedAt)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]*RecentPost, 0, len(rows))
	for _, j := range rows {
		out = append(out, s.joinRecent(j.post, j.topic))
	}
	return out, nil
}

// IncrementPostView returns nil even when the id is missing
// (matches the postgres UPDATE semantics — no error, just zero
// rows affected).
func (s *MemStore) IncrementPostView(ctx context.Context, postID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.posts[postID]; ok {
		p.ViewCount++
	}
	return nil
}

// RandomPost returns one post chosen via map-iteration order (Go
// randomises map iteration, so this is effectively random without
// math/rand seed plumbing). sql.ErrNoRows when empty.
func (s *MemStore) RandomPost(ctx context.Context) (*RecentPost, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.posts {
		t, ok := s.topics[p.TopicID]
		if !ok {
			continue
		}
		return s.joinRecent(p, t), nil
	}
	return nil, sql.ErrNoRows
}

func (s *MemStore) PostBySlug(ctx context.Context, topicID int, slug string) (*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.posts {
		if p.TopicID == topicID && p.Slug == slug {
			return clonePost(p), nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *MemStore) PostByID(ctx context.Context, id int) (*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.posts[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return clonePost(p), nil
}

func (s *MemStore) CreatePost(ctx context.Context, topicID int, title, slug, content string, createdBy int) (*Post, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPost++
	now := s.clock()
	p := &Post{
		ID:        s.nextPost,
		TopicID:   topicID,
		Title:     title,
		Slug:      slug,
		Content:   content,
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.posts[p.ID] = p
	return clonePost(p), nil
}

func (s *MemStore) UpdatePost(ctx context.Context, id int, title, slug, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.posts[id]
	if !ok {
		return nil
	}
	p.Title = title
	p.Slug = slug
	p.Content = content
	p.UpdatedAt = s.clock()
	return nil
}

func (s *MemStore) DeletePost(ctx context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.posts, id)
	return nil
}
