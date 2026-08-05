package wiki

import (
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
}

func NewHandlers(store Store, auth core.AuthService) *Handlers {
	return &Handlers{store: store, auth: auth}
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

// postsByTopicMap loads every wiki post and folds them into a map
// keyed by topic_id, so the sidebar can pre-render the full
// collapsed tree for client-side toggling. Returns an empty map on
// error — the sidebar gracefully shows just folder rows. The store
// method blanks the content column so this stays a slim payload.
func (h *Handlers) postsByTopicMap(c *gin.Context) map[int][]*Post {
	all, err := h.store.AllPosts(c.Request.Context())
	if err != nil {
		return map[int][]*Post{}
	}
	out := make(map[int][]*Post, 16)
	for _, p := range all {
		out[p.TopicID] = append(out[p.TopicID], p)
	}
	return out
}

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
	c.HTML(http.StatusOK, "wiki.html", deps.BaseData(c, gin.H{
		"Topics":       topics,
		"RecentPosts":  recentPosts,
		"PopularPosts": popularPosts,
		"PostsByTopic": h.postsByTopicMap(c),
	}))
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
	c.HTML(http.StatusOK, "wiki.html", deps.BaseData(c, gin.H{
		"Topics":         topics,
		"RecentPosts":    recentPosts,
		"PostsByTopic":   h.postsByTopicMap(c),
		"RecentOnlyView": true,
		"ActiveNav":      "recent",
	}))
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
	// AllTopics drives the left sidebar so the topic page mirrors the
	// landing layout — clicking a category in the sidebar from a topic
	// page stays in the same shell (hero + sidebar + main grid)
	// instead of jumping to a stripped-down topic view. Failure here is
	// non-fatal; the topic page still renders without the sidebar list.
	allTopics, _ := h.store.Topics(ctx)
	c.HTML(http.StatusOK, "wiki_topic.html", deps.BaseData(c, gin.H{
		"Topic":        topic,
		"Posts":        posts,
		"AllTopics":    allTopics,
		"PostsByTopic": h.postsByTopicMap(c),
	}))
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
	// AllTopics + Posts power the same hero+sidebar shell as the landing
	// and topic pages — the article-view page renders inside the same
	// surface with the current topic expanded and the current article
	// highlighted. Both lookups are best-effort; failure means the
	// sidebar comes up empty but the article body still renders.
	allTopics, _ := h.store.Topics(ctx)
	siblingPosts, _ := h.store.PostsByTopic(ctx, topic.ID)
	c.HTML(http.StatusOK, "wiki_post.html", deps.BaseData(c, gin.H{
		"Topic":           topic,
		"Post":            post,
		"RenderedContent": deps.Markdown(post.Content),
		"AllTopics":       allTopics,
		"Posts":           siblingPosts,
		"PostsByTopic":    h.postsByTopicMap(c),
	}))
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
	c.HTML(http.StatusOK, "admin_wiki.html", deps.BaseData(c, gin.H{
		"Topics":       topics,
		"PostsByTopic": postsByTopic,
	}))
}

func (h *Handlers) NewTopic(c *gin.Context) {
	c.HTML(http.StatusOK, "admin_wiki_topic_form.html", deps.BaseData(c, gin.H{
		"Action": "Create",
		"Icons":  TopicIcons,
	}))
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
	_, _ = h.store.CreateTopic(c.Request.Context(), topicInputFrom(c))
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
			c.HTML(http.StatusOK, "admin_wiki_topic_form.html", deps.BaseData(c, gin.H{
				"Action": "Edit",
				"Topic":  t,
				"Icons":  TopicIcons,
			}))
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
	c.HTML(http.StatusOK, "admin_wiki_post_form.html", deps.BaseData(c, gin.H{
		"Action":  "Create",
		"TopicID": topicID,
	}))
}

func (h *Handlers) CreatePost(c *gin.Context) {
	topicIDStr := c.PostForm("topic_id")
	topicID, _ := strconv.Atoi(topicIDStr)
	title := c.PostForm("title")
	content := c.PostForm("content")
	slug := makeSlug(title)
	createdBy := 0
	if u, ok := h.auth.CurrentUser(c); ok {
		createdBy = int(u.ID)
	}
	_, _ = h.store.CreatePost(c.Request.Context(), topicID, title, slug, content, createdBy)
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
	c.HTML(http.StatusOK, "admin_wiki_post_form.html", deps.BaseData(c, gin.H{
		"Action": "Edit",
		"Post":   post,
	}))
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
	_ = h.store.UpdatePost(c.Request.Context(), id, title, slug, content)
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
