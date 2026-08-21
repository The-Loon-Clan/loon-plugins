package forum

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

const forumPageSize = 30

// pageOffset is the SQL offset for a (page, pageSize) pair, page clamped to
// >= 1 — the up-front half of the pagination math (deps.RenderPagination builds the
// view-model once the total is known).
func pageOffset(page, pageSize int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	if page < 1 {
		page = 1
	}
	return (page - 1) * pageSize
}

// jsonError writes the host convention's JSON error envelope. msg is shown to
// the client — human-readable, no internals.
func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"ok": false, "error": msg})
}

// Notification kinds — MUST match the models.NotifForumQuote /
// NotifForumLike constants the preferences UI enumerates, so the
// per-user channel toggles keep gating these events.
const (
	notifKindQuote = "forum_quote"
	notifKindLike  = "forum_like"
)

// forumPostView wraps a ForumPost with the rendered HTML body and the
// rendered quoted-excerpt so the template can drop them in directly
// without calling template functions per row. Embedded *ForumPost
// keeps all the existing template field references working.
type forumPostView struct {
	*ForumPost
	BodyHTML   template.HTML
	QuotedHTML template.HTML
	// EditorHTML is this post's inline edit form, prefilled with its own
	// body. Per-post rather than per-page because it sits inside the post
	// range: a single page-level editor would have to be re-rendered per row
	// anyway, and `.EditorHTML` inside a {{range}} resolves against the ROW —
	// a page-level key there is a silent empty, which is an edit form that
	// renders as a bare Save button.
	EditorHTML template.HTML
}

// Handlers serves the public forum pages and the thread/post write
// paths. Ported from the host's CommunityHandler forum block during
// the plugin extraction; host seams are the Deps funcs (BaseData,
// Markdown, Paginate — see plugin.go) and the Core services (Auth
// for the session user, Users for actor display names,
// Notifications for quote/like pings).
type Handlers struct {
	store  Store
	auth   core.AuthService
	users  core.UsersService
	notify core.NotificationsService
	// core is held only to Emit. Nil in tests that do not care, so every
	// emit goes through h.emit rather than touching it directly.
	core *core.Core
}

func NewHandlers(store Store, auth core.AuthService, users core.UsersService, notify core.NotificationsService) *Handlers {
	return &Handlers{store: store, auth: auth, users: users, notify: notify}
}

// WithCore attaches the mediator so this plugin can announce what members do.
// Separate from the constructor so every existing caller — and every test —
// keeps compiling and simply emits nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces an event, if there is anywhere to announce it to.
//
// ALWAYS called after the write has committed, never before and never inside
// a transaction that can still roll back: the event says a thing happened, and
// a subscriber that acted on a post which then vanished has no way to find out.
func (h *Handlers) emit(c *gin.Context, name string, userID int, subject string, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(c.Request.Context(), core.Event{
		Name: name, UserID: int64(userID), Subject: subject, Data: data,
	})
}

// currentUser returns the viewer's id and mod-or-above flag —
// (0, false) for anonymous viewers. The session middleware in the
// route chain has already loaded the user into the context.
func (h *Handlers) currentUser(c *gin.Context) (int, bool) {
	u, ok := h.auth.CurrentUser(c)
	if !ok {
		return 0, false
	}
	return int(u.ID), u.AtLeast(core.RoleMod)
}

// Forums — index listing all categories.
func (h *Handlers) Forums(c *gin.Context) {
	ctx := c.Request.Context()
	cats, err := h.store.GetForumCategories(ctx)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "loadfailed")
		return
	}
	// See gates: unseeable categories vanish from the listing, the totals,
	// and the activity sidebar (a title in Recent Activity would leak what
	// the gate hides). Contributors is an all-forum aggregate of names +
	// counts — no content, so it stays.
	v := h.viewer(c)
	cats = visibleCategories(cats, v)
	seeable := make(map[int]bool, len(cats))
	for _, cat := range cats {
		seeable[cat.ID] = true
	}
	activity, _ := h.store.GetRecentForumActivity(ctx, 5)
	filtered := activity[:0]
	for _, a := range activity {
		if seeable[a.CategoryID] {
			filtered = append(filtered, a)
		}
	}
	activity = filtered
	contributors, _ := h.store.GetTopForumContributors(ctx, 5)

	var totalThreads, totalPosts int
	for _, cat := range cats {
		totalThreads += cat.ThreadCount
		totalPosts += cat.PostCount
	}

	h.render(c, http.StatusOK, "Forums", "community_forums.html", gin.H{
		"Categories":   cats,
		"Activity":     activity,
		"Contributors": contributors,
		"TotalThreads": totalThreads,
		"TotalPosts":   totalPosts,
	})
}

// ForumCategory — lists threads in a category.
func (h *Handlers) ForumCategory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := pageOffset(page, forumPageSize)

	cat, err := h.store.GetForumCategory(c.Request.Context(), id)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	v := h.viewer(c)
	if !v.canSee(cat) {
		// Unseeable = indistinguishable from nonexistent.
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	if !v.canRead(cat) {
		// Seeable but locked: show the category shell with an access note
		// instead of the thread list — the viewer is allowed to know it
		// exists, not what's inside.
		h.render(c, http.StatusOK, cat.Name, "community_category.html", gin.H{
			"Category": cat, "Threads": nil, "Total": 0,
			"Page": 1, "TotalPages": 1, "AccessDenied": true,
			// No pager on the locked shell: there is no list to page.
			"PaginationHTML": template.HTML(""),
		})
		return
	}
	threads, total, err := h.store.GetForumThreads(c.Request.Context(), id, forumPageSize, offset)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "loadfailed")
		return
	}
	totalPages := (total + forumPageSize - 1) / forumPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	h.render(c, http.StatusOK, cat.Name, "community_category.html", gin.H{
		"Category":   cat,
		"Threads":    threads,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"PaginationHTML": paginate(page, total,
			fmt.Sprintf("/community/forums/category/%d?", id)),
		"Pagination": legacyPaginate(page, totalPages,
			fmt.Sprintf("/community/forums/category/%d?", id)),
	})
}

// ForumThread — shows posts in a thread.
func (h *Handlers) ForumThread(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	thread, err := h.store.GetForumThread(c.Request.Context(), id)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}

	// Read gate on the thread's category: unreadable threads bounce to the
	// forums index (same as nonexistent — a gated thread URL proves nothing).
	if cat, ok := h.categoryFor(c.Request.Context(), thread.CategoryID); ok {
		if !h.viewer(c).canRead(cat) {
			c.Redirect(http.StatusFound, "/community/forums")
			return
		}
	}

	currentUserID, isAdmin := h.currentUser(c)

	// Recruitment-thread visibility: the thread author (the
	// recruiter receiving applications) and admins see every reply;
	// other viewers see only the OP plus their own reply (if any).
	// canSeeAllReplies stays true for discussion threads — the SQL
	// path collapses to the original behaviour. Anonymous viewers
	// (currentUserID == 0) on a recruitment thread see only the OP.
	canSeeAllReplies := thread.ThreadType != ForumThreadTypeRecruitment ||
		isAdmin || (currentUserID != 0 && currentUserID == thread.UserID)

	offset := pageOffset(page, forumPageSize)
	posts, total, err := h.store.GetForumPosts(c.Request.Context(), id, forumPageSize, offset, currentUserID, canSeeAllReplies)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "loadfailed")
		return
	}
	totalPages := (total + forumPageSize - 1) / forumPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	// Clamp out-of-range page numbers (ReplyThread used to redirect
	// to ?page=9999 to "jump to last page", which landed on an empty
	// offset). One re-fetch is unavoidable for a request that asks
	// for a non-existent page; normal navigation never trips this.
	if page > totalPages {
		page = totalPages
		offset = (page - 1) * forumPageSize
		posts, total, err = h.store.GetForumPosts(c.Request.Context(), id, forumPageSize, offset, currentUserID, canSeeAllReplies)
		if err != nil {
			h.fail(c, http.StatusInternalServerError, "loadfailed")
			return
		}
	}

	// Render each post body to safe HTML once here, in Go, instead of
	// calling a template function per row. Quoted excerpts go through
	// the same renderer so a quoted blockquote stays formatted (the
	// excerpt is capped server-side at 280 chars in GetForumPosts).
	views := make([]forumPostView, 0, len(posts))
	for _, p := range posts {
		v := forumPostView{ForumPost: p, BodyHTML: deps.Markdown(p.Body)}
		if p.QuotedBodyExcerpt != nil && *p.QuotedBodyExcerpt != "" {
			v.QuotedHTML = deps.Markdown(*p.QuotedBodyExcerpt)
		}
		v.EditorHTML = editor(map[string]any{
			"Name": "body", "Rows": 4, "Value": p.Body, "Required": true,
		})
		views = append(views, v)
	}

	h.render(c, http.StatusOK, thread.Title, "community_thread.html", gin.H{
		"Thread":        thread,
		"Posts":         views,
		"Total":         total,
		"Page":          page,
		"TotalPages":    totalPages,
		"CurrentUserID": currentUserID,
		"PaginationHTML": paginate(page, total,
			fmt.Sprintf("/community/forums/thread/%d?", id)),
		"Pagination": legacyPaginate(page, totalPages,
			fmt.Sprintf("/community/forums/thread/%d?", id)),
		"ReplyEditorHTML": editor(map[string]any{
			"Name": "body", "Rows": 4, "Placeholder": "Write your reply…", "Required": true,
		}),
		"ReportModalHTML": reportModal(c),
		// Surfaced to the template so the recruitment-thread chrome
		// can show "Replies are private" hints + suppress the public
		// reply-count display for non-privileged viewers.
		"IsRecruitment":    thread.ThreadType == ForumThreadTypeRecruitment,
		"CanSeeAllReplies": canSeeAllReplies,
	})
}

// NewThread — GET form. The category picker offers only categories the
// viewer may write in.
func (h *Handlers) NewThread(c *gin.Context) {
	catID, _ := strconv.Atoi(c.Query("category"))
	cats, _ := h.store.GetForumCategories(c.Request.Context())
	v := h.viewer(c)
	writable := cats[:0]
	for _, cat := range cats {
		if v.canSee(cat) && v.canWrite(cat) {
			writable = append(writable, cat)
		}
	}
	h.render(c, http.StatusOK, "New thread", "community_new_thread.html", gin.H{
		"Categories":       writable,
		"SelectedCategory": catID,
		"EditorHTML": editor(map[string]any{
			"Name": "body", "Rows": 8, "Placeholder": "Write your post…", "Required": true,
		}),
	})
}

// CreateThread — POST.
func (h *Handlers) CreateThread(c *gin.Context) {
	userID, _ := h.currentUser(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}

	catID, _ := strconv.Atoi(c.PostForm("category_id"))
	title := c.PostForm("title")
	body := c.PostForm("body")
	if title == "" || body == "" || catID == 0 {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	// Write gate on the target category — the form only offers writable
	// categories, so a mismatch here is a crafted POST.
	if cat, ok := h.categoryFor(c.Request.Context(), catID); !ok || !h.viewer(c).canWrite(cat) {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}

	// thread_type is a closed allowlist — anything outside the two
	// known values falls back to the default. Prevents a crafted
	// form post from injecting a value the storage layer doesn't
	// understand even though Postgres's CHECK constraint would
	// catch it anyway.
	threadType := c.PostForm("thread_type")
	if threadType != ForumThreadTypeRecruitment {
		threadType = ForumThreadTypeDiscussion
	}

	thread, err := h.store.CreateForumThread(c.Request.Context(), catID, userID, title, body, threadType)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "createfailed")
		return
	}
	// After the write, not before: the row exists now.
	h.emit(c, EventThreadCreated, userID, strconv.Itoa(thread.ID),
		ThreadCreated{CategoryID: catID, ThreadID: thread.ID, Title: title, Type: threadType})
	c.Redirect(http.StatusFound, "/community/forums/thread/"+strconv.Itoa(thread.ID))
}

// ReplyThread — POST adds a post to an existing thread.
func (h *Handlers) ReplyThread(c *gin.Context) {
	actor, ok := h.auth.CurrentUser(c)
	if !ok {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	userID := int(actor.ID)

	threadID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}

	thread, err := h.store.GetForumThread(c.Request.Context(), threadID)
	if err != nil || thread.Locked {
		c.Redirect(http.StatusFound, "/community/forums/thread/"+strconv.Itoa(threadID))
		return
	}

	// Write gate on the thread's category (see access.go).
	if cat, ok := h.categoryFor(c.Request.Context(), thread.CategoryID); !ok || !h.viewer(c).canWrite(cat) {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}

	body := c.PostForm("body")
	if body == "" {
		c.Redirect(http.StatusFound, "/community/forums/thread/"+strconv.Itoa(threadID))
		return
	}

	// Optional quoted_post_id from the Quote button. Empty/zero/invalid
	// is treated as "plain reply" — never an error, so a stale quote
	// link from a deleted post still posts the reply.
	var quotedID *int
	if q := c.PostForm("quoted_post_id"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			quotedID = &n
		}
	}

	post, err := h.store.CreateForumPost(c.Request.Context(), threadID, userID, body, quotedID)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "replyfailed")
		return
	}
	h.emit(c, EventPostCreated, userID, strconv.FormatInt(post.ID, 10),
		PostCreated{ThreadID: threadID, PostID: post.ID})
	// Forum-quote notification: only fires when this reply quoted
	// another post AND the quoted post still exists. Look up the
	// quoted post's author so we know who to ping; the self-quote
	// skip lives in the host's Notify (recipient == actor).
	if quotedID != nil {
		if ctxPost, err := h.store.PostContext(c.Request.Context(), int64(*quotedID)); err == nil && ctxPost != nil {
			_ = h.notify.Notify(c.Request.Context(), int64(ctxPost.AuthorID), core.Notification{
				Kind:      notifKindQuote,
				Title:     fmt.Sprintf("%s quoted you", actor.Username),
				Body:      ctxPost.ThreadName,
				Link:      fmt.Sprintf("/community/forums/thread/%d#post-%d", threadID, post.ID),
				ActorID:   actor.ID,
				ActorName: actor.Username,
			})
		}
	}
	// Compute which page the new post lands on (always the last
	// one) and tack a #post-N fragment on so the browser scrolls
	// straight to it. CreateForumThread inserts the first post
	// without bumping reply_count, so total posts = reply_count + 1,
	// and after the reply we just inserted it's reply_count + 2
	// (thread.ReplyCount is the value from BEFORE the insert).
	totalPosts := thread.ReplyCount + 2
	lastPage := (totalPosts + forumPageSize - 1) / forumPageSize
	target := fmt.Sprintf("/community/forums/thread/%d?page=%d#post-%d", threadID, lastPage, post.ID)
	c.Redirect(http.StatusFound, target)
}

// ReactPost — POST, toggles one emoji reaction on a post for the
// current user. JSON response so the front-end can update the bar
// in-place without a full page reload.
func (h *Handlers) ReactPost(c *gin.Context) {
	userID, _ := h.currentUser(c)
	if userID == 0 {
		jsonError(c, http.StatusUnauthorized, "login required")
		return
	}
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid post id")
		return
	}
	emoji := strings.TrimSpace(c.PostForm("emoji"))
	if !isAllowedReactionEmoji(emoji) {
		jsonError(c, http.StatusBadRequest, "unsupported emoji")
		return
	}
	added, err := h.store.ToggleForumPostReaction(c.Request.Context(), postID, userID, emoji)
	if err != nil {
		log.Printf("ToggleForumPostReaction(%d, user=%d, %q): %v", postID, userID, emoji, err)
		jsonError(c, http.StatusInternalServerError, "reaction failed")
		return
	}
	// Notify the post author on the "added" leg of the toggle. Removing
	// a reaction is silent — there's no useful "X un-liked your post"
	// message worth firing. The self-like skip lives in the host's
	// Notify (recipient == actor).
	if added {
		if ctxPost, err := h.store.PostContext(c.Request.Context(), postID); err == nil && ctxPost != nil {
			actorName, _ := h.users.DisplayName(c.Request.Context(), int64(userID))
			_ = h.notify.Notify(c.Request.Context(), int64(ctxPost.AuthorID), core.Notification{
				Kind:      notifKindLike,
				Title:     fmt.Sprintf("%s reacted %s to your post", actorName, emoji),
				Link:      fmt.Sprintf("/community/forums/thread/%d#post-%d", ctxPost.ThreadID, postID),
				ActorID:   int64(userID),
				ActorName: actorName,
			})
			// UserID is the REACTOR, not the author. There are two members in
			// this event and only one can be the subject; the reactor did the
			// thing, so a "reacted 100 times" achievement credits them. Put
			// the author in Data and get it backwards and you build a badge
			// that rewards the wrong person — and it looks right until
			// somebody checks whose count went up.
			h.emit(c, EventPostReacted, userID, strconv.FormatInt(postID, 10),
				PostReacted{PostID: postID, ThreadID: ctxPost.ThreadID,
					AuthorID: ctxPost.AuthorID, Emoji: emoji})
		}
	}
	counts, err := h.store.GetForumPostReactionCounts(c.Request.Context(), postID)
	if err != nil {
		// The toggle already succeeded; just return what we know.
		log.Printf("GetForumPostReactionCounts(%d): %v", postID, err)
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":     true,
		"added":  added,
		"counts": counts,
	})
}

// allowedReactionEmojis is the fixed picker. Server-side allowlist so
// users can't sneak arbitrary unicode (or HTML) into the column via a
// hand-crafted POST. Keep this in sync with the picker buttons in
// community_thread.html.
var allowedReactionEmojis = map[string]bool{
	"👍":  true,
	"❤️": true,
	"😂":  true,
	"😮":  true,
	"😢":  true,
	"🎉":  true,
}

func isAllowedReactionEmoji(s string) bool { return allowedReactionEmojis[s] }

// DeletePost — POST, owner or admin.
func (h *Handlers) DeletePost(c *gin.Context) {
	userID, isAdmin := h.currentUser(c)
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID == 0 {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	if err := h.store.DeleteForumPost(c.Request.Context(), postID, userID, isAdmin); err != nil {
		log.Printf("DeleteForumPost(%d, user=%d, admin=%v): %v", postID, userID, isAdmin, err)
	}

	threadID := c.PostForm("thread_id")
	c.Redirect(http.StatusFound, "/community/forums/thread/"+threadID)
}

// EditPost — POST, owner only.
func (h *Handlers) EditPost(c *gin.Context) {
	userID, _ := h.currentUser(c)
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID == 0 {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	body := strings.TrimSpace(c.PostForm("body"))
	if body == "" {
		threadID := c.PostForm("thread_id")
		c.Redirect(http.StatusFound, "/community/forums/thread/"+threadID)
		return
	}
	if err := h.store.UpdateForumPost(c.Request.Context(), postID, userID, body); err != nil {
		log.Printf("UpdateForumPost(%d, user=%d): %v", postID, userID, err)
	}
	threadID := c.PostForm("thread_id")
	c.Redirect(http.StatusFound, "/community/forums/thread/"+threadID)
}

// DeleteThread — POST, owner or admin.
func (h *Handlers) DeleteThread(c *gin.Context) {
	userID, isAdmin := h.currentUser(c)
	threadID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID == 0 {
		c.Redirect(http.StatusFound, "/community/forums")
		return
	}
	thread, _ := h.store.GetForumThread(c.Request.Context(), threadID)
	if err := h.store.DeleteForumThread(c.Request.Context(), threadID, userID, isAdmin); err != nil {
		log.Printf("DeleteForumThread(%d, user=%d, admin=%v): %v", threadID, userID, isAdmin, err)
	}
	if thread != nil {
		c.Redirect(http.StatusFound, "/community/forums/category/"+strconv.Itoa(thread.CategoryID))
		return
	}
	c.Redirect(http.StatusFound, "/community/forums")
}

// AdminLockThread — POST, mod+ (route-gated).
func (h *Handlers) AdminLockThread(c *gin.Context) {
	threadID, _ := strconv.Atoi(c.Param("id"))
	thread, _ := h.store.GetForumThread(c.Request.Context(), threadID)
	if thread != nil {
		_ = h.store.SetThreadLocked(c.Request.Context(), threadID, !thread.Locked)
	}
	c.Redirect(http.StatusFound, "/community/forums/thread/"+strconv.Itoa(threadID))
}

// AdminPinThread — POST, mod+ (route-gated).
func (h *Handlers) AdminPinThread(c *gin.Context) {
	threadID, _ := strconv.Atoi(c.Param("id"))
	thread, _ := h.store.GetForumThread(c.Request.Context(), threadID)
	if thread != nil {
		_ = h.store.SetThreadPinned(c.Request.Context(), threadID, !thread.Pinned)
	}
	c.Redirect(http.StatusFound, "/community/forums/thread/"+strconv.Itoa(threadID))
}
