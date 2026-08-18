package wiki

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Handlers serves the public wiki pages and the admin topic/post
// CRUD. Ported from the host's WikiHandler during the plugin
// extraction; the host couplings that remain are the Deps seams
// (BaseData for the site template chrome, Markdown for the wiki
// markdown pipeline — see plugin.go). Templates live in the host's
// template set (wiki*.html, admin_wiki*.html — the template
// contract in the README).

// jsonError writes the host convention's JSON error envelope.
func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"ok": false, "error": msg})
}

type Handlers struct {
	store Store
	auth  core.AuthService
	// core is the mediator, for announcing edits. Nil in tests.
	core *core.Core
}

func NewHandlers(store Store, auth core.AuthService) *Handlers {
	return &Handlers{store: store, auth: auth}
}

// report sends a failed write to the host's error sink. Reached through the
// mediator rather than a constructor argument so the existing callers and
// tests keep compiling; a Handlers without one logs nowhere, which is what a
// test wants and what production never has.
func (h *Handlers) report(ctx context.Context, op string, err error) {
	if h.core == nil || h.core.Errors == nil {
		return
	}
	h.core.Errors.Report(ctx, op, err)
}

// editorID is the signed-in editor, or 0. Every route that calls it is behind
// the mod gate, so 0 means the host wired no auth rather than "anonymous".
func (h *Handlers) editorID(c *gin.Context) int {
	if u, ok := h.auth.CurrentUser(c); ok && u != nil {
		return int(u.ID)
	}
	return 0
}

var (
	slugStrip    = regexp.MustCompile(`[^a-z0-9\s-]`)
	slugCollapse = regexp.MustCompile(`[\s-]+`)
)

// makeSlug converts a string to a URL-friendly slug.
func makeSlug(s string) string {
	s = strings.ToLower(s)
	s = slugStrip.ReplaceAllString(s, "")
	s = slugCollapse.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// Public handlers

// (postsByTopicMap and the landing-stats builder lived here until the
// 2026-08-17 declutter: they fed the explorer sidebar tree and the
// right-rail stats card, both retired with their layouts.)

func (h *Handlers) Index(c *gin.Context) {
	ctx := c.Request.Context()
	topics, err := h.store.Topics(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to get wiki topics")
		return
	}
	// Both panels are best-effort: failure hides the affected card
	// rather than 500ing the whole landing page over non-essential
	// extras. Empty slices render an empty panel naturally.
	recentPosts, _ := h.store.RecentPosts(ctx, 10)
	popularPosts, _ := h.store.PopularPosts(ctx, 5)
	render(c, http.StatusOK, "Wiki", "wiki.html", gin.H{
		"Topics":       topics,
		"RecentPosts":  recentPosts,
		"PopularPosts": popularPosts,
		// What this deployment calls itself, so the heading is not the name
		// of the site this plugin was lifted out of. Empty is fine — the
		// template says just "Wiki" then.
		"SiteName": siteName(),
	})
}

// RecentChanges renders the full chronological list of the most-
// recently-updated posts. Surfaced from the wiki landing sidebar's
// "Recent Changes" link.
func (h *Handlers) RecentChanges(c *gin.Context) {
	ctx := c.Request.Context()
	topics, _ := h.store.Topics(ctx)
	// 50 caps the page at one screenful and matches the in-flight
	// edit feed conventions elsewhere on the site.
	recentPosts, _ := h.store.RecentPosts(ctx, 50)
	render(c, http.StatusOK, "Wiki", "wiki.html", gin.H{
		"Topics":         topics,
		"RecentPosts":    recentPosts,
		"RecentOnlyView": true,
		"SiteName":       siteName(),
	})
}

// Random picks one wiki post at random and 302s to its canonical
// URL. Wired to the sidebar's "Random Page" shortcut. Falls back to
// the landing page when there are no posts at all.
func (h *Handlers) Random(c *gin.Context) {
	p, err := h.store.RandomPost(c.Request.Context())
	if err != nil || p == nil {
		c.Redirect(http.StatusFound, "/wiki")
		return
	}
	c.Redirect(http.StatusFound, "/wiki/"+p.TopicSlug+"/"+p.Slug)
}

func (h *Handlers) Topic(c *gin.Context) {
	ctx := c.Request.Context()
	topicSlug := c.Param("topic")
	topic, err := h.store.TopicBySlug(ctx, topicSlug)
	if err != nil {
		c.String(http.StatusNotFound, "topic not found")
		return
	}
	posts, err := h.store.PostsByTopic(ctx, topic.ID)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to get wiki posts")
		return
	}
	render(c, http.StatusOK, "Wiki", "wiki_topic.html", gin.H{
		"Topic": topic,
		"Posts": posts,
	})
}

func (h *Handlers) Post(c *gin.Context) {
	ctx := c.Request.Context()
	topicSlug := c.Param("topic")
	postSlug := c.Param("post")
	topic, err := h.store.TopicBySlug(ctx, topicSlug)
	if err != nil {
		c.String(http.StatusNotFound, "topic not found")
		return
	}
	post, err := h.store.PostBySlug(ctx, topic.ID, postSlug)
	if err != nil {
		c.String(http.StatusNotFound, "post not found")
		return
	}
	// Fire-and-forget view bump. Drives the Popular Articles card on
	// the landing page. We deliberately don't gate on auth or apply
	// rate-limiting per IP — even noisy bots end up surfacing the
	// real "what readers actually click" signal once the catalog
	// grows, and the column lives in wiki_posts so any drift is
	// trivially reset via UPDATE.
	_ = h.store.IncrementPostView(ctx, post.ID)
	render(c, http.StatusOK, "Wiki", "wiki_post.html", gin.H{
		"Topic":           topic,
		"Post":            post,
		"RenderedContent": h.renderContent(ctx, post.Content),
	})
}

// Admin handlers

func (h *Handlers) AdminIndex(c *gin.Context) {
	topics, err := h.store.Topics(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to get wiki topics")
		return
	}
	postsByTopic := make(map[int]interface{})
	for _, t := range topics {
		posts, _ := h.store.PostsByTopic(c.Request.Context(), t.ID)
		postsByTopic[t.ID] = posts
	}
	render(c, http.StatusOK, "Wiki — admin", "admin_wiki.html", gin.H{
		"CSRFToken":    deps.CSRFToken(c),
		"Topics":       topics,
		"PostsByTopic": postsByTopic,
	})
}

func (h *Handlers) NewTopic(c *gin.Context) {
	render(c, http.StatusOK, "Wiki topic", "admin_wiki_topic_form.html", gin.H{
		"CSRFToken": deps.CSRFToken(c),
		"Action":    "Create",
		"Icons":     TopicIcons,
	})
}

// topicInputFrom reads the shared create/edit form. Icon and colour are
// validated here rather than trusted: an unknown icon key or a malformed
// colour becomes empty, which the templates read as "use the default". A
// mistyped colour should cost you the accent, not the save.
func topicInputFrom(c *gin.Context) TopicInput {
	name := c.PostForm("name")
	sortOrder, _ := strconv.Atoi(c.DefaultPostForm("sort_order", "0"))
	icon := c.PostForm("icon")
	if !ValidIcon(icon) {
		icon = ""
	}
	return TopicInput{
		Name:        name,
		Slug:        makeSlug(name),
		Description: c.PostForm("description"),
		SortOrder:   sortOrder,
		Icon:        icon,
		Color:       NormalizeColor(c.PostForm("color")),
	}
}

func (h *Handlers) CreateTopic(c *gin.Context) {
	// All three write handlers here discarded their error until the events
	// went in. That is worse than untidy: a failed save redirected to the
	// index looking exactly like a successful one, so the editor's only clue
	// that their page had not been written was noticing it missing. Emitting
	// after a discarded error compounds it — the announcement claims a page
	// that does not exist, and a subscriber counts it.
	ctx := c.Request.Context()
	t, err := h.store.CreateTopic(ctx, topicInputFrom(c))
	if err != nil {
		h.report(ctx, "wiki/create-topic", err)
		c.Redirect(http.StatusFound, "/admin/wiki?err=1")
		return
	}
	h.emit(ctx, EventTopicCreated, h.editorID(c), TopicCreated{TopicID: t.ID, Title: t.Name, Slug: t.Slug})
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) EditTopic(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	topics, err := h.store.Topics(c.Request.Context())
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	for _, t := range topics {
		if t.ID == id {
			render(c, http.StatusOK, "Wiki topic", "admin_wiki_topic_form.html", gin.H{
				"CSRFToken": deps.CSRFToken(c),
				"Action":    "Edit",
				"Topic":     t,
				"Icons":     TopicIcons,
			})
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) UpdateTopic(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	_ = h.store.UpdateTopic(c.Request.Context(), id, topicInputFrom(c))
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) DeleteTopic(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	_ = h.store.DeleteTopic(c.Request.Context(), id)
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) NewPost(c *gin.Context) {
	topicIDStr := c.Query("topic_id")
	topicID, _ := strconv.Atoi(topicIDStr)
	render(c, http.StatusOK, "Wiki page", "admin_wiki_post_form.html", gin.H{
		"CSRFToken": deps.CSRFToken(c),
		"Action":    "Create",
		"TopicID":   topicID,
	})
}

func (h *Handlers) CreatePost(c *gin.Context) {
	topicIDStr := c.PostForm("topic_id")
	topicID, _ := strconv.Atoi(topicIDStr)
	title := c.PostForm("title")
	content := c.PostForm("content")
	slug := makeSlug(title)
	createdBy := h.editorID(c)
	ctx := c.Request.Context()
	post, err := h.store.CreatePost(ctx, topicID, title, slug, content, createdBy)
	if err != nil {
		h.report(ctx, "wiki/create-post", err)
		c.Redirect(http.StatusFound, "/admin/wiki?err=1")
		return
	}
	h.emit(ctx, EventPostCreated, createdBy,
		PostCreated{PostID: post.ID, TopicID: topicID, Title: title, Slug: slug})
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) EditPost(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	post, err := h.store.PostByID(c.Request.Context(), id)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	render(c, http.StatusOK, "Wiki page", "admin_wiki_post_form.html", gin.H{
		"CSRFToken": deps.CSRFToken(c),
		"Action":    "Edit",
		"Post":      post,
	})
}

func (h *Handlers) UpdatePost(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	title := c.PostForm("title")
	content := c.PostForm("content")
	slug := makeSlug(title)
	ctx := c.Request.Context()
	if err := h.store.UpdatePost(ctx, id, title, slug, content); err != nil {
		h.report(ctx, "wiki/update-post", err)
		c.Redirect(http.StatusFound, "/admin/wiki?err=1")
		return
	}
	h.emit(ctx, EventPostUpdated, h.editorID(c), PostUpdated{PostID: id, Title: title, Slug: slug})
	c.Redirect(http.StatusFound, "/admin/wiki")
}

func (h *Handlers) DeletePost(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/wiki")
		return
	}
	_ = h.store.DeletePost(c.Request.Context(), id)
	c.Redirect(http.StatusFound, "/admin/wiki")
}
