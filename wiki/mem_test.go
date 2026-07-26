package wiki

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// seedTopic + seedPost are minimal seeders for the test surface
// — they wrap the public Create* methods so server-stamping +
// the topic/post sequence stay in one place.
func seedTopic(s *MemStore, t *testing.T, slug string, sortOrder int) *Topic {
	t.Helper()
	out, err := s.CreateTopic(context.Background(), TopicInput{Name: slug + " topic", Slug: slug, Description: "desc", SortOrder: sortOrder})
	if err != nil {
		t.Fatalf("seedTopic %q: %v", slug, err)
	}
	return out
}

func seedPost(s *MemStore, t *testing.T, topicID int, slug string) *Post {
	t.Helper()
	out, err := s.CreatePost(context.Background(), topicID, slug+" title", slug, "body", 1)
	if err != nil {
		t.Fatalf("seedPost %q: %v", slug, err)
	}
	return out
}

func TestMemStore_CreateTopicStampsServerFields(t *testing.T) {
	s := NewMemStore()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	out, err := s.CreateTopic(context.Background(), TopicInput{Name: "Help", Slug: "help", Description: "Help section", SortOrder: 5})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ID == 0 || !out.CreatedAt.Equal(now) {
		t.Errorf("server fields not stamped: %+v", out)
	}
	if out.Name != "Help" || out.Slug != "help" || out.SortOrder != 5 {
		t.Errorf("payload not stored: %+v", out)
	}
}

func TestMemStore_TopicsCountsAndOrders(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	// sort_order ASC, then name ASC. Two topics share sort_order 1
	// to exercise the name tiebreaker.
	a := seedTopic(s, t, "a", 2)
	b := seedTopic(s, t, "b", 1)
	c := seedTopic(s, t, "c", 1)
	seedPost(s, t, a.ID, "a-p1")
	seedPost(s, t, a.ID, "a-p2")
	seedPost(s, t, b.ID, "b-p1")

	got, err := s.Topics(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Expected ordering: b (1,"b topic"), c (1,"c topic"), a (2,"a topic").
	if len(got) != 3 || got[0].ID != b.ID || got[1].ID != c.ID || got[2].ID != a.ID {
		t.Errorf("ordering: %v %v %v",
			got[0].Slug, got[1].Slug, got[2].Slug)
	}
	if got[0].PostCount != 1 || got[1].PostCount != 0 || got[2].PostCount != 2 {
		t.Errorf("post_count: %d %d %d", got[0].PostCount, got[1].PostCount, got[2].PostCount)
	}
}

func TestMemStore_TopicBySlug(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	seedTopic(s, t, "help", 0)

	got, err := s.TopicBySlug(ctx, "help")
	if err != nil || got == nil {
		t.Fatalf("hit: got=%v err=%v", got, err)
	}

	_, err = s.TopicBySlug(ctx, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("miss should be ErrNoRows: %v", err)
	}
}

func TestMemStore_UpdateAndDeleteTopicCascades(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "old", 0)
	seedPost(s, t, tp.ID, "child")

	if err := s.UpdateTopic(ctx, tp.ID, TopicInput{Name: "New", Slug: "new", Description: "newdesc", SortOrder: 9}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := s.TopicBySlug(ctx, "new")
	if after == nil || after.Name != "New" || after.SortOrder != 9 {
		t.Errorf("update not applied: %+v", after)
	}

	// Missing id is a no-op, not an error.
	if err := s.UpdateTopic(ctx, 999, TopicInput{Name: "x", Slug: "x", Description: "x"}); err != nil {
		t.Errorf("update unknown id: %v", err)
	}

	if err := s.DeleteTopic(ctx, tp.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// FK cascade — the child post must be gone too.
	posts, _ := s.PostsByTopic(ctx, tp.ID)
	if len(posts) != 0 {
		t.Errorf("cascade failed: %d posts left", len(posts))
	}
}

func TestMemStore_CreatePostStampsAndUpdateBumps(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	t0 := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return t0 })
	tp := seedTopic(s, t, "help", 0)

	post, err := s.CreatePost(ctx, tp.ID, "FAQ", "faq", "## body", 7)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !post.CreatedAt.Equal(t0) || !post.UpdatedAt.Equal(t0) {
		t.Errorf("stamps: %+v", post)
	}
	if post.CreatedBy != 7 {
		t.Errorf("createdBy: %d", post.CreatedBy)
	}

	// Advance the clock and update — updated_at must bump but
	// created_at must not.
	s.SetClock(func() time.Time { return t0.Add(time.Hour) })
	if err := s.UpdatePost(ctx, post.ID, "FAQ v2", "faq-v2", "## new body"); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := s.PostByID(ctx, post.ID)
	if !after.CreatedAt.Equal(t0) {
		t.Errorf("created_at moved: %v", after.CreatedAt)
	}
	if !after.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("updated_at not bumped: %v", after.UpdatedAt)
	}
	if after.Title != "FAQ v2" || after.Slug != "faq-v2" || after.Content != "## new body" {
		t.Errorf("update payload: %+v", after)
	}
}

func TestMemStore_PostBySlugAndID(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "help", 0)
	p := seedPost(s, t, tp.ID, "intro")

	got, err := s.PostBySlug(ctx, tp.ID, "intro")
	if err != nil || got == nil || got.ID != p.ID {
		t.Errorf("slug hit: got=%v err=%v", got, err)
	}
	_, err = s.PostBySlug(ctx, tp.ID, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("slug miss: %v", err)
	}
	// Wrong topic id returns miss even if the slug exists elsewhere.
	other := seedTopic(s, t, "other", 1)
	_, err = s.PostBySlug(ctx, other.ID, "intro")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cross-topic should miss: %v", err)
	}

	got, err = s.PostByID(ctx, p.ID)
	if err != nil || got == nil {
		t.Errorf("id hit: got=%v err=%v", got, err)
	}
	_, err = s.PostByID(ctx, 999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("id miss: %v", err)
	}
}

func TestMemStore_AllPostsExcludesContent(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "help", 0)
	p, _ := s.CreatePost(ctx, tp.ID, "T", "t", "## huge content", 1)

	all, err := s.AllPosts(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: got=%d err=%v", len(all), err)
	}
	if all[0].Content != "" {
		t.Errorf("content not blanked: %q", all[0].Content)
	}
	// Sanity: PostByID still returns full content.
	full, _ := s.PostByID(ctx, p.ID)
	if full.Content != "## huge content" {
		t.Errorf("byID dropped content: %q", full.Content)
	}
}

func TestMemStore_RecentPostsOrdersByUpdatedAt(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	tp := seedTopic(s, t, "help", 0)

	// Three posts created at increasing times. Then bump post 1's
	// updated_at via an Update so it surfaces above the fresher
	// posts in the "Recent" panel.
	s.SetClock(func() time.Time { return now })
	p1 := seedPost(s, t, tp.ID, "a")
	s.SetClock(func() time.Time { return now.Add(time.Hour) })
	p2 := seedPost(s, t, tp.ID, "b")
	s.SetClock(func() time.Time { return now.Add(2 * time.Hour) })
	p3 := seedPost(s, t, tp.ID, "c")

	s.SetClock(func() time.Time { return now.Add(10 * time.Hour) })
	_ = s.UpdatePost(ctx, p1.ID, p1.Title, p1.Slug, "edited")

	got, err := s.RecentPosts(ctx, 0) // limit<=0 → default 10
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 || got[0].ID != p1.ID || got[1].ID != p3.ID || got[2].ID != p2.ID {
		t.Errorf("ordering: %d %d %d", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[0].TopicSlug != "help" || got[0].TopicName == "" {
		t.Errorf("topic join: %+v", got[0])
	}

	// Limit truncates.
	top1, _ := s.RecentPosts(ctx, 1)
	if len(top1) != 1 || top1[0].ID != p1.ID {
		t.Errorf("limit=1: %+v", top1)
	}
}

func TestMemStore_PopularPostsOrdersByViews(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "help", 0)
	p1 := seedPost(s, t, tp.ID, "a")
	p2 := seedPost(s, t, tp.ID, "b")
	seedPost(s, t, tp.ID, "c")

	for i := 0; i < 3; i++ {
		_ = s.IncrementPostView(ctx, p2.ID)
	}
	_ = s.IncrementPostView(ctx, p1.ID)

	got, err := s.PopularPosts(ctx, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != p2.ID || got[1].ID != p1.ID {
		t.Errorf("ordering: %+v", got)
	}
	if got[0].ViewCount != 3 {
		t.Errorf("view count: %d", got[0].ViewCount)
	}
}

func TestMemStore_PostsByTopicChronological(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	tp := seedTopic(s, t, "help", 0)
	other := seedTopic(s, t, "other", 1)

	s.SetClock(func() time.Time { return now })
	p1 := seedPost(s, t, tp.ID, "intro")
	s.SetClock(func() time.Time { return now.Add(time.Hour) })
	p2 := seedPost(s, t, tp.ID, "details")
	seedPost(s, t, other.ID, "noise") // must not bleed in

	got, _ := s.PostsByTopic(ctx, tp.ID)
	if len(got) != 2 || got[0].ID != p1.ID || got[1].ID != p2.ID {
		t.Errorf("by topic: %+v", got)
	}
}

func TestMemStore_DeletePostByID(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "help", 0)
	p := seedPost(s, t, tp.ID, "drop")
	if err := s.DeletePost(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.PostByID(ctx, p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("post still present: %v", err)
	}
	// Deleting a missing id is a no-op.
	if err := s.DeletePost(ctx, 999); err != nil {
		t.Errorf("missing delete: %v", err)
	}
}

func TestMemStore_RandomPostEmptyAndHit(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	if _, err := s.RandomPost(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("empty store should be ErrNoRows: %v", err)
	}
	tp := seedTopic(s, t, "help", 0)
	p := seedPost(s, t, tp.ID, "only")
	got, err := s.RandomPost(ctx)
	if err != nil || got == nil || got.ID != p.ID || got.TopicSlug != "help" {
		t.Errorf("hit: got=%+v err=%v", got, err)
	}
}

func TestMemStore_DefensiveClone(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tp := seedTopic(s, t, "help", 0)
	p := seedPost(s, t, tp.ID, "intro")

	got, _ := s.TopicBySlug(ctx, "help")
	got.Name = "tampered"
	again, _ := s.TopicBySlug(ctx, "help")
	if again.Name == "tampered" {
		t.Error("topic was not cloned defensively")
	}

	post, _ := s.PostByID(ctx, p.ID)
	post.Content = "tampered"
	again2, _ := s.PostByID(ctx, p.ID)
	if again2.Content == "tampered" {
		t.Error("post was not cloned defensively")
	}
}

func TestMakeSlug(t *testing.T) {
	cases := map[string]string{
		"Hello World":        "hello-world",
		"  FAQ & Help!  ":    "faq-help",
		"multi   space":      "multi-space",
		"already-slugged":    "already-slugged",
		"--trim--edges--":    "trim-edges",
		"Ünïcode strîpped 9": "ncode-strpped-9",
	}
	for in, want := range cases {
		if got := makeSlug(in); got != want {
			t.Errorf("makeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
