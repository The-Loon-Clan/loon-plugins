package forum

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// seedCategory creates a category via the public API so server-
// stamping (id + created_at + insertion order) stays in one
// place.
func seedCategory(repo *MemStore, t *testing.T, name string, ordinal int) *ForumCategory {
	t.Helper()
	if err := repo.CreateForumCategory(context.Background(), CategoryParams{Name: name, Description: "desc-" + name, Ordinal: ordinal, Icon: "chat-square-text", Color: "blue", SeeRole: "all", ReadRole: "all", WriteRole: "user"}); err != nil {
		t.Fatalf("seedCategory %q: %v", name, err)
	}
	// Production CreateForumCategory doesn't return the new id;
	// fetch the row we just inserted by name.
	cats, _ := repo.GetForumCategories(context.Background())
	for _, c := range cats {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("seedCategory %q: not found after insert", name)
	return nil
}

// seedThreadForCategory drops a forum_thread row directly into
// the mock's threads map. The threads/posts sub-slice will own
// the public CreateForumThread surface; this slice's tests just
// need a row that points at a category so the rollup queries
// have something to count.
func seedThreadForCategory(repo *MemStore, t *testing.T, catID int, title string, lastPostAt time.Time) *ForumThread {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	id := len(repo.threads) + 1
	thread := &ForumThread{
		ID:         id,
		CategoryID: catID,
		UserID:     1,
		Username:   "u",
		Title:      title,
		LastPostAt: lastPostAt,
		CreatedAt:  lastPostAt,
	}
	repo.threads[id] = &forumThreadRow{thread: thread}
	return thread
}

// seedPostForThread drops a forum_post into the mock's posts
// map — same justification as seedThreadForCategory.
func seedPostForThread(repo *MemStore, t *testing.T, threadID int, username string, createdAt time.Time) *ForumPost {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	id := int64(len(repo.posts) + 1)
	post := &ForumPost{
		ID:        id,
		ThreadID:  threadID,
		UserID:    1,
		Username:  username,
		Body:      "body",
		CreatedAt: createdAt,
	}
	repo.posts[id] = &forumPostRow{
		post:      post,
		reactions: map[forumReactionKey]struct{}{},
	}
	return post
}

func TestMockForum_CreateForumCategoryRejectsDuplicateName(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	if err := repo.CreateForumCategory(ctx, CategoryParams{Name: "general", Description: "d", Ordinal: 1}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := repo.CreateForumCategory(ctx, CategoryParams{Name: "general", Description: "different desc", Ordinal: 2}); err == nil {
		t.Error("duplicate name should error")
	}
}

func TestMockForum_GetForumCategoriesOrderedAndCounted(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	a := seedCategory(repo, t, "alpha", 2)
	b := seedCategory(repo, t, "beta", 1)
	seedCategory(repo, t, "gamma", 3)

	// Two threads in alpha, one in beta. Beta's thread has the
	// latest last_post_at across the surface.
	t1 := seedThreadForCategory(repo, t, a.ID, "a thread 1", now)
	_ = seedThreadForCategory(repo, t, a.ID, "a thread 2", now.Add(-time.Hour))
	tb := seedThreadForCategory(repo, t, b.ID, "b thread", now.Add(2*time.Hour))
	seedPostForThread(repo, t, t1.ID, "alice", now.Add(-30*time.Minute))
	seedPostForThread(repo, t, tb.ID, "bob", now.Add(2*time.Hour))

	got, err := repo.GetForumCategories(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("list: %d %v", len(got), err)
	}
	// Ordered by ordinal ASC: beta(1), alpha(2), gamma(3).
	if got[0].Name != "beta" || got[1].Name != "alpha" || got[2].Name != "gamma" {
		t.Errorf("ordering: %s %s %s", got[0].Name, got[1].Name, got[2].Name)
	}
	// Counts.
	if got[0].ThreadCount != 1 || got[1].ThreadCount != 2 || got[2].ThreadCount != 0 {
		t.Errorf("thread_count: %d %d %d", got[0].ThreadCount, got[1].ThreadCount, got[2].ThreadCount)
	}
	if got[0].PostCount != 1 || got[1].PostCount != 1 || got[2].PostCount != 0 {
		t.Errorf("post_count: %d %d %d", got[0].PostCount, got[1].PostCount, got[2].PostCount)
	}
	// last_post_at — beta has the freshest.
	if got[0].LastPostAt == nil || !got[0].LastPostAt.Equal(now.Add(2*time.Hour)) {
		t.Errorf("beta last_post_at: %+v", got[0].LastPostAt)
	}
	// Preview cols on beta: latest thread + latest poster.
	if got[0].LastThreadID == nil || *got[0].LastThreadID != tb.ID {
		t.Errorf("beta last_thread_id: %+v", got[0].LastThreadID)
	}
	if got[0].LastPostUser == nil || *got[0].LastPostUser != "bob" {
		t.Errorf("beta last_post_user: %+v", got[0].LastPostUser)
	}
	// Gamma has no activity — every nullable column stays nil.
	if got[2].LastPostAt != nil || got[2].LastThreadID != nil || got[2].LastPostUser != nil {
		t.Errorf("empty gamma not nil: %+v", got[2])
	}
}

func TestMockForum_GetForumCategoryDetailOmitsPreview(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	c := seedCategory(repo, t, "general", 1)
	seedThreadForCategory(repo, t, c.ID, "t1", time.Now())

	got, err := repo.GetForumCategory(ctx, c.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.ThreadCount != 1 {
		t.Errorf("count: %d", got.ThreadCount)
	}
	// Detail shape — no preview columns.
	if got.LastThreadID != nil || got.LastThreadTitle != nil || got.LastPostUser != nil {
		t.Errorf("preview leaked into detail: %+v", got)
	}

	if _, err := repo.GetForumCategory(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("miss: %v", err)
	}
}

func TestMockForum_UpdateForumCategory(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	c := seedCategory(repo, t, "old", 5)

	if err := repo.UpdateForumCategory(ctx, c.ID, CategoryParams{Name: "new", Description: "newer desc", Ordinal: 99, Icon: "megaphone", Color: "green", SeeRole: "mod", SeeTier: 2}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := repo.GetForumCategory(ctx, c.ID)
	if after.Name != "new" || after.Description != "newer desc" || after.Ordinal != 99 {
		t.Errorf("update not applied: %+v", after)
	}
	if after.Icon != "megaphone" || after.Color != "green" {
		t.Errorf("icon/color not applied: %+v", after)
	}

	// Missing id is a no-op.
	if err := repo.UpdateForumCategory(ctx, 999, CategoryParams{Name: "x"}); err != nil {
		t.Errorf("missing id: %v", err)
	}
}

func TestMockForum_DeleteForumCategoryRefusesNonEmpty(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	c := seedCategory(repo, t, "general", 1)
	seedThreadForCategory(repo, t, c.ID, "blocker", time.Now())

	if err := repo.DeleteForumCategory(ctx, c.ID); !errors.Is(err, errCategoryHasThreads) {
		t.Fatalf("non-empty should be rejected: %v", err)
	}
	// Drop the thread and delete should succeed.
	repo.mu.Lock()
	for tid, tr := range repo.threads {
		if tr.thread.CategoryID == c.ID {
			delete(repo.threads, tid)
		}
	}
	repo.mu.Unlock()
	if err := repo.DeleteForumCategory(ctx, c.ID); err != nil {
		t.Errorf("empty delete: %v", err)
	}
	if _, err := repo.GetForumCategory(ctx, c.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("post-delete still present: %v", err)
	}
}

func TestMockForum_MergeForumCategoryRepointsAndDrops(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	src := seedCategory(repo, t, "dup", 1)
	dst := seedCategory(repo, t, "keep", 2)
	thread := seedThreadForCategory(repo, t, src.ID, "to-be-moved", time.Now())

	if err := repo.MergeForumCategory(ctx, src.ID, dst.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	// src category gone.
	if _, err := repo.GetForumCategory(ctx, src.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("src not dropped: %v", err)
	}
	// thread re-pointed.
	repo.mu.RLock()
	tr := repo.threads[thread.ID]
	repo.mu.RUnlock()
	if tr.thread.CategoryID != dst.ID {
		t.Errorf("thread not repointed: cat=%d", tr.thread.CategoryID)
	}
	// dst now reflects the thread in its count.
	after, _ := repo.GetForumCategory(ctx, dst.ID)
	if after.ThreadCount != 1 {
		t.Errorf("dst count: %d", after.ThreadCount)
	}

	// src==dst is rejected without touching anything.
	if err := repo.MergeForumCategory(ctx, dst.ID, dst.ID); !errors.Is(err, errMergeSameCategory) {
		t.Errorf("same-id merge: %v", err)
	}
}

func TestMockForum_CategoryDefensiveClone(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	c := seedCategory(repo, t, "general", 1)
	seedThreadForCategory(repo, t, c.ID, "t1", time.Now())

	got, _ := repo.GetForumCategory(ctx, c.ID)
	got.Name = "tampered"
	again, _ := repo.GetForumCategory(ctx, c.ID)
	if again.Name == "tampered" {
		t.Error("category was not cloned defensively")
	}
}

// ── Threads + posts + reactions + sidebars ─────────────────────────

func TestMockForum_CreateForumThreadInsertsThreadAndFirstPost(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	repo.SetClock(func() time.Time { return now })
	cat := seedCategory(repo, t, "general", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "alice.png")

	thread, err := repo.CreateForumThread(ctx, cat.ID, 7, "Hello", "first post body", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if thread.ID == 0 || thread.CategoryID != cat.ID || thread.UserID != 7 {
		t.Errorf("thread shape: %+v", thread)
	}
	if !thread.CreatedAt.Equal(now) || !thread.LastPostAt.Equal(now) {
		t.Errorf("stamps: %+v", thread)
	}
	if thread.Username != "alice" || thread.Role != "user" || thread.AvatarPath != "alice.png" {
		t.Errorf("user join: %+v", thread)
	}
	if thread.CategoryName != "general" {
		t.Errorf("category join: %q", thread.CategoryName)
	}
	// First post must have landed in the posts side-car.
	posts, total, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if total != 1 || len(posts) != 1 || posts[0].Body != "first post body" {
		t.Errorf("first post: total=%d %+v", total, posts)
	}
}

func TestMockForum_GetForumThreadsOrdersPinnedFirst(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	cat := seedCategory(repo, t, "general", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")

	// 3 threads in the same category, freshest first by time.
	repo.SetClock(func() time.Time { return now })
	old, _ := repo.CreateForumThread(ctx, cat.ID, 7, "oldest", "x", "")
	repo.SetClock(func() time.Time { return now.Add(time.Hour) })
	mid, _ := repo.CreateForumThread(ctx, cat.ID, 7, "middle", "x", "")
	repo.SetClock(func() time.Time { return now.Add(2 * time.Hour) })
	fresh, _ := repo.CreateForumThread(ctx, cat.ID, 7, "freshest", "x", "")
	// Pin the oldest — it must surface first.
	_ = repo.SetThreadPinned(ctx, old.ID, true)
	// Hide the middle thread.
	repo.SeedForumThreadHidden(mid.ID, now)

	got, total, _ := repo.GetForumThreads(ctx, cat.ID, 10, 0)
	if total != 2 || len(got) != 2 {
		t.Fatalf("total=%d len=%d (hidden should be excluded)", total, len(got))
	}
	if got[0].ID != old.ID {
		t.Errorf("pinned first: %d", got[0].ID)
	}
	if got[1].ID != fresh.ID {
		t.Errorf("fresh second: %d", got[1].ID)
	}
}

func TestMockForum_GetForumThreadHiddenReturnsErrNoRows(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "general", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "y", "")

	if _, err := repo.GetForumThread(ctx, thread.ID); err != nil {
		t.Fatalf("visible: %v", err)
	}
	repo.SeedForumThreadHidden(thread.ID, time.Now())
	if _, err := repo.GetForumThread(ctx, thread.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("hidden should 404: %v", err)
	}
	if _, err := repo.GetForumThread(ctx, 9999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("miss: %v", err)
	}
}

func TestMockForum_DeleteForumThreadGate(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "y", "")

	// Non-owner non-admin → silent no-op.
	if err := repo.DeleteForumThread(ctx, thread.ID, 8, false); err != nil {
		t.Fatalf("non-owner err: %v", err)
	}
	if _, err := repo.GetForumThread(ctx, thread.ID); err != nil {
		t.Errorf("non-owner should have left thread: %v", err)
	}
	// Owner → succeeds + FK cascades to posts.
	if err := repo.DeleteForumThread(ctx, thread.ID, 7, false); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	if _, err := repo.GetForumThread(ctx, thread.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("owner delete didn't drop: %v", err)
	}
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if len(posts) != 0 {
		t.Errorf("FK cascade failed: %d", len(posts))
	}
	// Admin can delete other-owner threads.
	t2, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "y", "")
	if err := repo.DeleteForumThread(ctx, t2.ID, 8, true); err != nil {
		t.Fatalf("admin delete: %v", err)
	}
}

func TestMockForum_SetThreadLockedAndPinned(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "y", "")

	_ = repo.SetThreadLocked(ctx, thread.ID, true)
	_ = repo.SetThreadPinned(ctx, thread.ID, true)
	got, _ := repo.GetForumThread(ctx, thread.ID)
	if !got.Locked || !got.Pinned {
		t.Errorf("flags: %+v", got)
	}
	_ = repo.SetThreadLocked(ctx, thread.ID, false)
	got, _ = repo.GetForumThread(ctx, thread.ID)
	if got.Locked {
		t.Error("unlock didn't apply")
	}
	// Missing id no-ops.
	if err := repo.SetThreadLocked(ctx, 9999, true); err != nil {
		t.Errorf("missing thread lock: %v", err)
	}
}

func TestMockForum_CreateForumPostBumpsParent(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	repo.SetClock(func() time.Time { return now })
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "first", "")

	repo.SetClock(func() time.Time { return now.Add(time.Hour) })
	reply, err := repo.CreateForumPost(ctx, thread.ID, 8, "reply", nil)
	if err != nil || reply.ID == 0 {
		t.Fatalf("create: %v %+v", err, reply)
	}
	got, _ := repo.GetForumThread(ctx, thread.ID)
	if got.ReplyCount != 1 {
		t.Errorf("reply_count: %d", got.ReplyCount)
	}
	if !got.LastPostAt.Equal(now.Add(time.Hour)) {
		t.Errorf("last_post_at not bumped: %v", got.LastPostAt)
	}
	// LATERAL last-reply on the list view.
	list, _, _ := repo.GetForumThreads(ctx, cat.ID, 10, 0)
	if list[0].LastPostUserID == nil || *list[0].LastPostUserID != 8 {
		t.Errorf("last_post_user_id: %+v", list[0].LastPostUserID)
	}
	if list[0].LastPostUsername == nil || *list[0].LastPostUsername != "bob" {
		t.Errorf("last_post_username: %+v", list[0].LastPostUsername)
	}
}

func TestMockForum_GetForumPostsQuotedExcerpt(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "the original body that is being quoted by bob", "")
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	first := posts[0]
	qid := int(first.ID)
	reply, _ := repo.CreateForumPost(ctx, thread.ID, 8, "quoted reply", &qid)

	got, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[1].ID != reply.ID {
		t.Errorf("ordering: %d %d", got[0].ID, got[1].ID)
	}
	if got[1].QuotedPostID == nil || *got[1].QuotedPostID != first.ID {
		t.Errorf("quoted_post_id: %+v", got[1].QuotedPostID)
	}
	if got[1].QuotedUsername == nil || *got[1].QuotedUsername != "alice" {
		t.Errorf("quoted_username: %+v", got[1].QuotedUsername)
	}
	if got[1].QuotedBodyExcerpt == nil || *got[1].QuotedBodyExcerpt == "" {
		t.Errorf("quoted_body_excerpt empty")
	}
}

func TestMockForum_GetForumPostsExcludesHidden(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "first", "")
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	repo.SeedForumPostHidden(posts[0].ID, time.Now())
	visible, total, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if total != 0 || len(visible) != 0 {
		t.Errorf("hidden should be excluded: total=%d len=%d", total, len(visible))
	}
}

func TestMockForum_UpdateForumPostOwnerGated(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "v0", "")
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	first := posts[0]

	// Non-owner returns the sentinel.
	if err := repo.UpdateForumPost(ctx, first.ID, 8, "v1"); !errors.Is(err, errPostNotOwned) {
		t.Errorf("non-owner: %v", err)
	}
	// Owner succeeds + stamps edited_at.
	if err := repo.UpdateForumPost(ctx, first.ID, 7, "v1"); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	got, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if got[0].Body != "v1" || got[0].EditedAt == nil {
		t.Errorf("update: %+v", got[0])
	}
	// Missing id returns sentinel too.
	if err := repo.UpdateForumPost(ctx, 9999, 7, "x"); !errors.Is(err, errPostNotOwned) {
		t.Errorf("missing id: %v", err)
	}
}

func TestMockForum_DeleteForumPostGate(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "first", "")
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	first := posts[0]

	// Non-owner non-admin → silent no-op.
	_ = repo.DeleteForumPost(ctx, first.ID, 8, false)
	check, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if len(check) != 1 {
		t.Errorf("non-owner removed: %d", len(check))
	}
	// Owner succeeds.
	_ = repo.DeleteForumPost(ctx, first.ID, 7, false)
	check, _, _ = repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if len(check) != 0 {
		t.Errorf("owner delete: %d", len(check))
	}
}

func TestMockForum_ToggleForumPostReactionRoundTrip(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	thread, _ := repo.CreateForumThread(ctx, cat.ID, 7, "x", "first", "")
	posts, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	pid := posts[0].ID

	added, _ := repo.ToggleForumPostReaction(ctx, pid, 7, "👍")
	if !added {
		t.Error("first toggle should return added=true")
	}
	added, _ = repo.ToggleForumPostReaction(ctx, pid, 7, "👍")
	if added {
		t.Error("second toggle should return added=false")
	}

	// Multi-user multi-emoji.
	repo.SeedForumUserInfo(8, "bob", "user", "")
	_, _ = repo.ToggleForumPostReaction(ctx, pid, 7, "👍")
	_, _ = repo.ToggleForumPostReaction(ctx, pid, 7, "🎉")
	_, _ = repo.ToggleForumPostReaction(ctx, pid, 8, "👍")

	counts, _ := repo.GetForumPostReactionCounts(ctx, pid)
	gotMap := map[string]int{}
	for _, c := range counts {
		gotMap[c.Emoji] = c.Count
	}
	if gotMap["👍"] != 2 || gotMap["🎉"] != 1 {
		t.Errorf("counts: %+v", counts)
	}

	// Viewer overlay on GetForumPosts.
	view, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 7, true)
	mine := map[string]bool{}
	for _, e := range view[0].MyReactions {
		mine[e] = true
	}
	if !mine["👍"] || !mine["🎉"] {
		t.Errorf("alice's overlay: %+v", view[0].MyReactions)
	}
	// Anonymous viewer has no overlay.
	anon, _, _ := repo.GetForumPosts(ctx, thread.ID, 10, 0, 0, true)
	if len(anon[0].MyReactions) != 0 {
		t.Errorf("anon overlay: %+v", anon[0].MyReactions)
	}
}

func TestMockForum_GetRecentForumActivityExcludesHidden(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumCategoryAppearance(cat.ID, "icon.svg", "#fa0")
	repo.SeedForumUserInfo(7, "alice", "mod", "alice.png")
	repo.SeedForumUserInfo(8, "bob", "user", "")

	repo.SetClock(func() time.Time { return now })
	tA, _ := repo.CreateForumThread(ctx, cat.ID, 7, "ta", "p0", "")
	repo.SetClock(func() time.Time { return now.Add(time.Hour) })
	tB, _ := repo.CreateForumThread(ctx, cat.ID, 8, "tb", "p0", "")
	// Add one reply on each so we have multiple posts.
	repo.SetClock(func() time.Time { return now.Add(2 * time.Hour) })
	_, _ = repo.CreateForumPost(ctx, tA.ID, 8, "reply-a", nil)
	repo.SetClock(func() time.Time { return now.Add(3 * time.Hour) })
	hiddenPost, _ := repo.CreateForumPost(ctx, tB.ID, 7, "hidden-reply", nil)
	repo.SeedForumPostHidden(hiddenPost.ID, now.Add(3*time.Hour))

	got, err := repo.GetRecentForumActivity(ctx, 10)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 4 posts created, 1 hidden → 3 visible.
	if len(got) != 3 {
		t.Errorf("len: %d", len(got))
	}
	// Newest first.
	for i := 1; i < len(got); i++ {
		if !got[i-1].CreatedAt.After(got[i].CreatedAt) {
			t.Errorf("not newest-first at %d: %v vs %v", i, got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}
	// First row should join category icon/color + user info.
	if got[0].CategoryIcon != "icon.svg" || got[0].CategoryColor != "#fa0" {
		t.Errorf("category appearance: %+v", got[0])
	}

	// Hide a whole thread — every post in it should drop too.
	repo.SeedForumThreadHidden(tA.ID, now)
	got, _ = repo.GetRecentForumActivity(ctx, 10)
	for _, row := range got {
		if row.ThreadID == tA.ID {
			t.Errorf("hidden thread row leaked: %+v", row)
		}
	}
}

func TestMockForum_GetTopForumContributorsExcludesHidden(t *testing.T) {
	repo := NewMemStore()
	ctx := context.Background()
	cat := seedCategory(repo, t, "g", 1)
	repo.SeedForumUserInfo(7, "alice", "user", "")
	repo.SeedForumUserInfo(8, "bob", "user", "")
	// Alice: 3 posts (1 hidden). Bob: 2 posts.
	tA, _ := repo.CreateForumThread(ctx, cat.ID, 7, "ta", "a0", "")
	_, _ = repo.CreateForumPost(ctx, tA.ID, 7, "a1", nil)
	hp, _ := repo.CreateForumPost(ctx, tA.ID, 7, "a2-hidden", nil)
	repo.SeedForumPostHidden(hp.ID, time.Now())
	_, _ = repo.CreateForumPost(ctx, tA.ID, 8, "b0", nil)
	_, _ = repo.CreateForumPost(ctx, tA.ID, 8, "b1", nil)

	top, _ := repo.GetTopForumContributors(ctx, 5)
	if len(top) != 2 {
		t.Fatalf("len: %d", len(top))
	}
	// Alice 2 visible, Bob 2 visible → tied; userID ASC tiebreaker → alice first.
	if top[0].UserID != 7 || top[0].PostCount != 2 {
		t.Errorf("first: %+v", top[0])
	}
	if top[1].UserID != 8 || top[1].PostCount != 2 {
		t.Errorf("second: %+v", top[1])
	}
}
