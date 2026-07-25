package forum

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"
)

var _ Store = (*MemStore)(nil)

// errCategoryHasThreads / errMergeSameCategory mirror the
// production fmt.Errorf shapes for the two delete/merge guard
// rails. Exposed as sentinels so tests can assert via errors.Is
// instead of brittle string-matching.
var (
	errCategoryHasThreads = errors.New("category has threads — move or delete them first")
	errMergeSameCategory  = errors.New("source and destination are the same category")
	errPostNotOwned       = errors.New("post not found or not owned by user")
)

// ForumRepository is the in-memory implementation for the
// categories sub-slice. Threads + posts + reactions fields
// extend this struct in the follow-up slice — the seam maps
// they share with categories (cross-table count rollups) all
// live here so both slices can read/write to the same
// in-memory store.
type MemStore struct {
	mu sync.RWMutex

	categories map[int]*forumCategoryRow
	nextCat    int

	// threads + posts populate the threads/posts sub-slice.
	// Categories needs read access for the aggregate rollups
	// (thread_count, post_count, last_post_at, last-thread
	// preview) — that's why both seams live on this
	// repository.
	threads    map[int]*forumThreadRow
	posts      map[int64]*forumPostRow
	nextThread int
	nextPost   int64

	// tiers backs ViewerTier (the user_display reputation_tier
	// lookup in production); seeded via SetViewerTier.
	tiers map[int64]int

	// users stages the per-user info every Forum JOIN reads
	// (username / role / avatar_path). Production fetches via
	// JOIN users; the mock reads from this map at SELECT
	// time. Missing user falls back to zero-value strings.
	users map[int]*forumUserInfo

	clock func() time.Time
}

// forumUserInfo is the slim per-user side-car the Forum reads
// join to. Production has a real users table; the mock takes
// these via SeedUserInfo.
type forumUserInfo struct {
	Username   string
	Role       string
	AvatarPath string
}

// forumCategoryRow wraps the public model + private color/icon
// + a created_at the public constructor stamps.
type forumCategoryRow struct {
	cat *ForumCategory
}

// forumThreadRow is the in-memory wrapper for a forum_thread.
// The hidden_at seam mirrors the production column the
// threads/posts queries gate on; the post side-car here keeps
// us from having to compute reply_count + last_post_at
// repeatedly on every read.
type forumThreadRow struct {
	thread     *ForumThread
	hiddenAt   *time.Time
	authorOnly bool // OP-only thread — no replies yet (suppresses last-reply panel)
}

// forumPostRow is the in-memory wrapper for a forum_post.
type forumPostRow struct {
	post      *ForumPost
	hiddenAt  *time.Time
	reactions map[forumReactionKey]struct{}
}

// forumReactionKey is the forum_post_reactions composite
// primary key.
type forumReactionKey struct {
	UserID int
	Emoji  string
}

func NewMemStore() *MemStore {
	return &MemStore{
		categories: map[int]*forumCategoryRow{},
		threads:    map[int]*forumThreadRow{},
		posts:      map[int64]*forumPostRow{},
		users:      map[int]*forumUserInfo{},
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

// SeedForumUserInfo stages the (username, role, avatar_path) the
// Forum JOIN-to-users reads. Required for any thread/post test
// that asserts on the joined view columns. Production has a
// real users table; the mock takes the data via this seam.
func (r *MemStore) SeedForumUserInfo(userID int, username, role, avatarPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[userID] = &forumUserInfo{Username: username, Role: role, AvatarPath: avatarPath}
}

// SeedForumCategoryAppearance stages the icon + color the
// Recent Activity sidebar JOIN reads. Production stores these
// on forum_categories.{icon,color}; the categories sub-slice
// doesn't surface a writer for them.
func (r *MemStore) SeedForumCategoryAppearance(catID int, icon, color string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.categories[catID]; ok {
		c.cat.Icon = icon
		c.cat.Color = color
	}
}

// SeedForumThreadHidden stages the hidden_at column on one
// thread. Drives the cross-surface exclusion in
// GetForumThreads / GetForumThread / GetRecentForumActivity.
func (r *MemStore) SeedForumThreadHidden(threadID int, hiddenAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.threads[threadID]; ok {
		v := hiddenAt
		t.hiddenAt = &v
	}
}

// SeedForumPostHidden stages the hidden_at column on one
// post. Drives the cross-surface exclusion in GetForumPosts /
// GetRecentForumActivity / GetTopForumContributors.
func (r *MemStore) SeedForumPostHidden(postID int64, hiddenAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.posts[postID]; ok {
		v := hiddenAt
		p.hiddenAt = &v
	}
}

// SetClock pins created_at / last_post_at for deterministic tests.
func (r *MemStore) SetClock(fn func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = fn
}

func cloneForumCategory(c *ForumCategory) *ForumCategory {
	if c == nil {
		return nil
	}
	cp := *c
	if c.LastPostAt != nil {
		v := *c.LastPostAt
		cp.LastPostAt = &v
	}
	if c.LastThreadID != nil {
		v := *c.LastThreadID
		cp.LastThreadID = &v
	}
	if c.LastThreadTitle != nil {
		v := *c.LastThreadTitle
		cp.LastThreadTitle = &v
	}
	if c.LastPostUser != nil {
		v := *c.LastPostUser
		cp.LastPostUser = &v
	}
	return &cp
}

// ── Categories ─────────────────────────────────────────────────────

// rollupCategoryLocked builds the per-category aggregate counts +
// latest-thread preview. Caller already holds at least the read
// lock. withPreview=true fills LastThreadID/Title/PostUser (the
// list shape); false leaves them nil (the detail shape).
func (r *MemStore) rollupCategoryLocked(catID int, withPreview bool) *ForumCategory {
	row, ok := r.categories[catID]
	if !ok {
		return nil
	}
	out := cloneForumCategory(row.cat)
	out.ThreadCount = 0
	out.PostCount = 0
	out.LastPostAt = nil
	out.LastThreadID = nil
	out.LastThreadTitle = nil
	out.LastPostUser = nil
	var latestThread *forumThreadRow
	for _, t := range r.threads {
		if t.thread.CategoryID != catID {
			continue
		}
		out.ThreadCount++
		// MAX(last_post_at) across the category — mirrors the
		// production GROUP BY rollup.
		if out.LastPostAt == nil || t.thread.LastPostAt.After(*out.LastPostAt) {
			v := t.thread.LastPostAt
			out.LastPostAt = &v
		}
		if latestThread == nil || t.thread.LastPostAt.After(latestThread.thread.LastPostAt) {
			latestThread = t
		}
	}
	for _, p := range r.posts {
		if t, ok := r.threads[p.post.ThreadID]; ok && t.thread.CategoryID == catID {
			out.PostCount++
		}
	}
	if withPreview && latestThread != nil {
		tid := latestThread.thread.ID
		title := latestThread.thread.Title
		out.LastThreadID = &tid
		out.LastThreadTitle = &title
		// Latest post user inside the latest thread.
		var latestPost *forumPostRow
		for _, p := range r.posts {
			if p.post.ThreadID != tid {
				continue
			}
			if latestPost == nil || p.post.CreatedAt.After(latestPost.post.CreatedAt) {
				latestPost = p
			}
		}
		if latestPost != nil {
			u := latestPost.post.Username
			out.LastPostUser = &u
		}
	}
	return out
}

func (r *MemStore) GetForumCategories(ctx context.Context) ([]*ForumCategory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ForumCategory, 0, len(r.categories))
	for id := range r.categories {
		out = append(out, r.rollupCategoryLocked(id, true))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal != out[j].Ordinal {
			return out[i].Ordinal < out[j].Ordinal
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r *MemStore) GetForumCategory(ctx context.Context, id int) (*ForumCategory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.categories[id]; !ok {
		return nil, sql.ErrNoRows
	}
	return r.rollupCategoryLocked(id, false), nil
}

func (r *MemStore) CreateForumCategory(ctx context.Context, p CategoryParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Production has a UNIQUE index on name — mirror that here
	// so duplicate inserts return the same shape the handler
	// expects.
	for _, c := range r.categories {
		if c.cat.Name == p.Name {
			return errors.New("duplicate category name")
		}
	}
	r.nextCat++
	r.categories[r.nextCat] = &forumCategoryRow{cat: &ForumCategory{
		ID:          r.nextCat,
		Name:        p.Name,
		Description: p.Description,
		Ordinal:     p.Ordinal,
		Icon:        p.Icon,
		Color:       p.Color,
		SeeRole:     p.SeeRole,
		ReadRole:    p.ReadRole,
		WriteRole:   p.WriteRole,
		SeeTier:     p.SeeTier,
		ReadTier:    p.ReadTier,
		WriteTier:   p.WriteTier,
		CreatedAt:   r.clock(),
	}}
	return nil
}

func (r *MemStore) UpdateForumCategory(ctx context.Context, id int, p CategoryParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.categories[id]
	if !ok {
		// Production runs an UPDATE that affects 0 rows on miss
		// — same here.
		return nil
	}
	c.cat.Name = p.Name
	c.cat.Description = p.Description
	c.cat.Ordinal = p.Ordinal
	c.cat.Icon = p.Icon
	c.cat.Color = p.Color
	c.cat.SeeRole = p.SeeRole
	c.cat.ReadRole = p.ReadRole
	c.cat.WriteRole = p.WriteRole
	c.cat.SeeTier = p.SeeTier
	c.cat.ReadTier = p.ReadTier
	c.cat.WriteTier = p.WriteTier
	return nil
}

// ViewerTier mirrors the PG lookup off user_display; seed tiers with
// SetViewerTier in tests. Unknown users are tier 0, like a missing row.
func (r *MemStore) ViewerTier(ctx context.Context, userID int64) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tiers[userID], nil
}

// SetViewerTier seeds a user's rank tier for gate tests.
func (r *MemStore) SetViewerTier(userID int64, tier int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tiers == nil {
		r.tiers = map[int64]int{}
	}
	r.tiers[userID] = tier
}

func (r *MemStore) DeleteForumCategory(ctx context.Context, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.threads {
		if t.thread.CategoryID == id {
			return errCategoryHasThreads
		}
	}
	delete(r.categories, id)
	return nil
}

func (r *MemStore) MergeForumCategory(ctx context.Context, srcID, dstID int) error {
	if srcID == dstID {
		return errMergeSameCategory
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.threads {
		if t.thread.CategoryID == srcID {
			t.thread.CategoryID = dstID
		}
	}
	delete(r.categories, srcID)
	return nil
}

// ── Threads ───────────────────────────────────────────────────────

func cloneForumThread(t *ForumThread) *ForumThread {
	if t == nil {
		return nil
	}
	cp := *t
	if t.LastPostUserID != nil {
		v := *t.LastPostUserID
		cp.LastPostUserID = &v
	}
	if t.LastPostUsername != nil {
		v := *t.LastPostUsername
		cp.LastPostUsername = &v
	}
	if t.LastPostRole != nil {
		v := *t.LastPostRole
		cp.LastPostRole = &v
	}
	if t.LastPostAvatarPath != nil {
		v := *t.LastPostAvatarPath
		cp.LastPostAvatarPath = &v
	}
	return &cp
}

func cloneForumPost(p *ForumPost) *ForumPost {
	if p == nil {
		return nil
	}
	cp := *p
	if p.EditedAt != nil {
		v := *p.EditedAt
		cp.EditedAt = &v
	}
	if p.QuotedPostID != nil {
		v := *p.QuotedPostID
		cp.QuotedPostID = &v
	}
	if p.QuotedUsername != nil {
		v := *p.QuotedUsername
		cp.QuotedUsername = &v
	}
	if p.QuotedBodyExcerpt != nil {
		v := *p.QuotedBodyExcerpt
		cp.QuotedBodyExcerpt = &v
	}
	if len(p.Reactions) > 0 {
		cp.Reactions = make([]ForumReactionCount, len(p.Reactions))
		copy(cp.Reactions, p.Reactions)
	}
	if len(p.MyReactions) > 0 {
		cp.MyReactions = make([]string, len(p.MyReactions))
		copy(cp.MyReactions, p.MyReactions)
	}
	return &cp
}

// fillThreadJoinCols overlays the user + category view columns
// from the staged seams. Caller already holds the read lock.
func (r *MemStore) fillThreadJoinCols(t *ForumThread) {
	if u := r.users[t.UserID]; u != nil {
		t.Username = u.Username
		t.Role = u.Role
		t.AvatarPath = u.AvatarPath
	}
	if c, ok := r.categories[t.CategoryID]; ok {
		t.CategoryName = c.cat.Name
	}
	// LATERAL last-reply user: most recent visible post in
	// this thread whose user_id differs from the OP.
	var latest *forumPostRow
	for _, p := range r.posts {
		if p.post.ThreadID != t.ID {
			continue
		}
		if p.hiddenAt != nil {
			continue
		}
		if p.post.UserID == t.UserID {
			continue
		}
		if latest == nil || p.post.CreatedAt.After(latest.post.CreatedAt) {
			latest = p
		}
	}
	if latest != nil {
		uid := latest.post.UserID
		t.LastPostUserID = &uid
		if u := r.users[uid]; u != nil {
			username := u.Username
			role := u.Role
			avatar := u.AvatarPath
			t.LastPostUsername = &username
			t.LastPostRole = &role
			t.LastPostAvatarPath = &avatar
		} else {
			empty := ""
			t.LastPostUsername = &empty
			t.LastPostRole = &empty
			t.LastPostAvatarPath = &empty
		}
	}
}

// fillPostJoinCols overlays the user + quoted-post view columns.
// Caller already holds the read lock.
func (r *MemStore) fillPostJoinCols(p *ForumPost) {
	if u := r.users[p.UserID]; u != nil {
		p.Username = u.Username
		p.UserRole = u.Role
		p.AvatarPath = u.AvatarPath
	}
	if p.UserRole == "" {
		p.UserRole = "user"
	}
	if p.QuotedPostID != nil {
		if qp, ok := r.posts[*p.QuotedPostID]; ok {
			if qu := r.users[qp.post.UserID]; qu != nil {
				name := qu.Username
				p.QuotedUsername = &name
			}
			excerpt := qp.post.Body
			if len(excerpt) > 280 {
				excerpt = excerpt[:280]
			}
			p.QuotedBodyExcerpt = &excerpt
		}
	}
}

func (r *MemStore) GetForumThreads(ctx context.Context, categoryID, limit, offset int) ([]*ForumThread, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	matches := []*forumThreadRow{}
	for _, t := range r.threads {
		if t.thread.CategoryID != categoryID || t.hiddenAt != nil {
			continue
		}
		matches = append(matches, t)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].thread.Pinned != matches[j].thread.Pinned {
			return matches[i].thread.Pinned // true sorts first
		}
		return matches[i].thread.LastPostAt.After(matches[j].thread.LastPostAt)
	})
	total := len(matches)
	if offset >= total {
		return []*ForumThread{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	out := make([]*ForumThread, 0, end-offset)
	for _, t := range matches[offset:end] {
		c := cloneForumThread(t.thread)
		r.fillThreadJoinCols(c)
		out = append(out, c)
	}
	return out, total, nil
}

func (r *MemStore) GetRecentForumThreads(ctx context.Context, limit int) ([]*ForumThread, error) {
	if limit <= 0 {
		limit = 5
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	matches := []*forumThreadRow{}
	for _, t := range r.threads {
		if t.hiddenAt != nil {
			continue
		}
		// Mirror production: the spotlight renders without a viewer
		// context, so only categories whose See gate admits everyone
		// may contribute rows.
		if c, ok := r.categories[t.thread.CategoryID]; ok {
			if c.cat.SeeRole != "" && c.cat.SeeRole != "all" {
				continue
			}
		}
		matches = append(matches, t)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].thread.LastPostAt.After(matches[j].thread.LastPostAt)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]*ForumThread, 0, len(matches))
	for _, t := range matches {
		c := cloneForumThread(t.thread)
		r.fillThreadJoinCols(c)
		out = append(out, c)
	}
	return out, nil
}

func (r *MemStore) GetForumThread(ctx context.Context, threadID int) (*ForumThread, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.threads[threadID]
	if !ok || t.hiddenAt != nil {
		return nil, sql.ErrNoRows
	}
	out := cloneForumThread(t.thread)
	// Detail page already shows the reply list; no LATERAL
	// last-reply preview needed. Drop those fields so the
	// returned shape matches GetForumThread production
	// (which omits them from SELECT).
	out.LastPostUserID = nil
	out.LastPostUsername = nil
	out.LastPostRole = nil
	out.LastPostAvatarPath = nil
	if u := r.users[out.UserID]; u != nil {
		out.Username = u.Username
		out.Role = u.Role
		out.AvatarPath = u.AvatarPath
	}
	if c, ok := r.categories[out.CategoryID]; ok {
		out.CategoryName = c.cat.Name
	}
	return out, nil
}

func (r *MemStore) CreateForumThread(ctx context.Context, categoryID, userID int, title, body, threadType string) (*ForumThread, error) {
	if threadType == "" {
		threadType = ForumThreadTypeDiscussion
	}
	r.mu.Lock()
	now := r.clock()
	r.nextThread++
	threadID := r.nextThread
	thread := &ForumThread{
		ID:         threadID,
		CategoryID: categoryID,
		UserID:     userID,
		Title:      title,
		ThreadType: threadType,
		LastPostAt: now,
		CreatedAt:  now,
	}
	r.threads[threadID] = &forumThreadRow{thread: thread}
	r.nextPost++
	postID := r.nextPost
	r.posts[postID] = &forumPostRow{
		post: &ForumPost{
			ID:        postID,
			ThreadID:  threadID,
			UserID:    userID,
			Body:      body,
			CreatedAt: now,
		},
		reactions: map[forumReactionKey]struct{}{},
	}
	r.mu.Unlock()
	return r.GetForumThread(ctx, threadID)
}

func (r *MemStore) DeleteForumThread(ctx context.Context, threadID, userID int, isAdmin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.threads[threadID]
	if !ok {
		return nil
	}
	if !isAdmin && t.thread.UserID != userID {
		// Non-owner non-admin = silent zero-rows-affected
		// (production WHERE user_id = $2 returns 0 rows).
		return nil
	}
	delete(r.threads, threadID)
	// FK ON DELETE CASCADE: drop child posts.
	for pid, p := range r.posts {
		if p.post.ThreadID == threadID {
			delete(r.posts, pid)
		}
	}
	return nil
}

func (r *MemStore) SetThreadLocked(ctx context.Context, threadID int, locked bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.threads[threadID]; ok {
		t.thread.Locked = locked
	}
	return nil
}

func (r *MemStore) SetThreadPinned(ctx context.Context, threadID int, pinned bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.threads[threadID]; ok {
		t.thread.Pinned = pinned
	}
	return nil
}

// ── Posts ─────────────────────────────────────────────────────────

func (r *MemStore) GetForumPosts(ctx context.Context, threadID, limit, offset, viewerID int, canSeeAllReplies bool) ([]*ForumPost, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	matches := []*forumPostRow{}
	for _, p := range r.posts {
		if p.post.ThreadID != threadID || p.hiddenAt != nil {
			continue
		}
		matches = append(matches, p)
	}
	sort.Slice(matches, func(i, j int) bool {
		// Tie-break on ID: two posts created in the same clock tick
		// (thread OP + fast reply, or same-transaction inserts in
		// production) must still order deterministically.
		if matches[i].post.CreatedAt.Equal(matches[j].post.CreatedAt) {
			return matches[i].post.ID < matches[j].post.ID
		}
		return matches[i].post.CreatedAt.Before(matches[j].post.CreatedAt)
	})
	// Recruitment-thread visibility (mig 251) — when the viewer
	// can't see all replies, keep the OP (first by created_at)
	// and any post they authored themselves; drop everything else.
	if !canSeeAllReplies && len(matches) > 0 {
		opID := matches[0].post.ID
		filtered := matches[:0]
		for _, p := range matches {
			if p.post.ID == opID || p.post.UserID == viewerID {
				filtered = append(filtered, p)
			}
		}
		matches = filtered
	}
	total := len(matches)
	if offset >= total {
		return []*ForumPost{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	out := make([]*ForumPost, 0, end-offset)
	for _, p := range matches[offset:end] {
		c := cloneForumPost(p.post)
		r.fillPostJoinCols(c)
		// Per-emoji counts + viewer's own reactions.
		counts := map[string]int{}
		var mine []string
		for k := range p.reactions {
			counts[k.Emoji]++
			if viewerID > 0 && k.UserID == viewerID {
				mine = append(mine, k.Emoji)
			}
		}
		if len(counts) > 0 {
			emojis := make([]string, 0, len(counts))
			for e := range counts {
				emojis = append(emojis, e)
			}
			sort.Strings(emojis)
			c.Reactions = make([]ForumReactionCount, 0, len(emojis))
			for _, e := range emojis {
				c.Reactions = append(c.Reactions, ForumReactionCount{Emoji: e, Count: counts[e]})
			}
		}
		if len(mine) > 0 {
			sort.Strings(mine)
			c.MyReactions = mine
		}
		out = append(out, c)
	}
	return out, total, nil
}

func (r *MemStore) CreateForumPost(ctx context.Context, threadID, userID int, body string, quotedPostID *int) (*ForumPost, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPost++
	now := r.clock()
	post := &ForumPost{
		ID:        r.nextPost,
		ThreadID:  threadID,
		UserID:    userID,
		Body:      body,
		CreatedAt: now,
	}
	if quotedPostID != nil {
		v := int64(*quotedPostID)
		post.QuotedPostID = &v
	}
	r.posts[post.ID] = &forumPostRow{
		post:      post,
		reactions: map[forumReactionKey]struct{}{},
	}
	// Bump parent thread's reply_count + last_post_at so the
	// category list re-sorts. No-op if the thread is missing —
	// matches production silently-zero-rows behaviour.
	if t, ok := r.threads[threadID]; ok {
		t.thread.ReplyCount++
		t.thread.LastPostAt = now
	}
	return cloneForumPost(post), nil
}

func (r *MemStore) UpdateForumPost(ctx context.Context, postID int64, userID int, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok || p.post.UserID != userID {
		// Either missing OR owned by another user → same
		// shape as production "post not found or not owned".
		return errPostNotOwned
	}
	p.post.Body = body
	edited := r.clock()
	p.post.EditedAt = &edited
	return nil
}

func (r *MemStore) DeleteForumPost(ctx context.Context, postID int64, userID int, isAdmin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return nil
	}
	if !isAdmin && p.post.UserID != userID {
		return nil
	}
	delete(r.posts, postID)
	return nil
}

// ── Reactions ─────────────────────────────────────────────────────

func (r *MemStore) ToggleForumPostReaction(ctx context.Context, postID int64, userID int, emoji string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		// Production INSERT errors via FK; mock returns the
		// same shape so handler treats it as a soft fail.
		return false, nil
	}
	k := forumReactionKey{UserID: userID, Emoji: emoji}
	if _, exists := p.reactions[k]; exists {
		delete(p.reactions, k)
		return false, nil
	}
	p.reactions[k] = struct{}{}
	return true, nil
}

func (r *MemStore) GetForumPostReactionCounts(ctx context.Context, postID int64) ([]ForumReactionCount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.posts[postID]
	if !ok {
		return []ForumReactionCount{}, nil
	}
	counts := map[string]int{}
	for k := range p.reactions {
		counts[k.Emoji]++
	}
	emojis := make([]string, 0, len(counts))
	for e := range counts {
		emojis = append(emojis, e)
	}
	sort.Strings(emojis)
	out := make([]ForumReactionCount, 0, len(emojis))
	for _, e := range emojis {
		out = append(out, ForumReactionCount{Emoji: e, Count: counts[e]})
	}
	return out, nil
}

// ── Sidebars ──────────────────────────────────────────────────────

func (r *MemStore) GetRecentForumActivity(ctx context.Context, limit int) ([]*ForumActivityItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	candidates := []*forumPostRow{}
	for _, p := range r.posts {
		if p.hiddenAt != nil {
			continue
		}
		t, ok := r.threads[p.post.ThreadID]
		if !ok || t.hiddenAt != nil {
			continue
		}
		candidates = append(candidates, p)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].post.CreatedAt.After(candidates[j].post.CreatedAt)
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]*ForumActivityItem, 0, len(candidates))
	for _, p := range candidates {
		t := r.threads[p.post.ThreadID]
		c := r.categories[t.thread.CategoryID]
		item := &ForumActivityItem{
			PostID:      p.post.ID,
			ThreadID:    t.thread.ID,
			ThreadTitle: t.thread.Title,
			UserID:      p.post.UserID,
			CategoryID:  c.cat.ID,
			CreatedAt:   p.post.CreatedAt,
		}
		if u := r.users[p.post.UserID]; u != nil {
			item.Username = u.Username
			item.Role = u.Role
			item.AvatarPath = u.AvatarPath
		}
		item.CategoryIcon = c.cat.Icon
		item.CategoryColor = c.cat.Color
		out = append(out, item)
	}
	return out, nil
}

func (r *MemStore) GetTopForumContributors(ctx context.Context, limit int) ([]*ForumContributor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := map[int]int{}
	for _, p := range r.posts {
		if p.hiddenAt != nil {
			continue
		}
		counts[p.post.UserID]++
	}
	out := make([]*ForumContributor, 0, len(counts))
	for uid, n := range counts {
		c := &ForumContributor{UserID: uid, PostCount: n}
		if u := r.users[uid]; u != nil {
			c.Username = u.Username
			c.Role = u.Role
			c.AvatarPath = u.AvatarPath
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PostCount != out[j].PostCount {
			return out[i].PostCount > out[j].PostCount
		}
		return out[i].UserID < out[j].UserID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PostContext derives the (author, thread) projection from the
// live posts/threads maps — unlike the old mock/notification stub
// it needs no separate seeding, so tests that create real posts
// get real contexts.
func (r *MemStore) PostContext(ctx context.Context, postID int64) (*PostContext, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	row, ok := r.posts[postID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	out := &PostContext{
		PostID:   postID,
		AuthorID: row.post.UserID,
		ThreadID: int64(row.post.ThreadID),
	}
	if t, ok := r.threads[row.post.ThreadID]; ok {
		out.ThreadName = t.thread.Title
	}
	return out, nil
}
