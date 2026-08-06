// Package news is the site-news system — the public /news feed +
// per-post pages and the admin editor. Fully self-contained: it owns
// its models + Store and publishes the home-page "Latest news" rows
// as the news.home extension for the host to consume.
package news

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("news", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams the news plugin renders through. SetDeps
// must be called before core.Boot; Provision fails loud otherwise.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The four pages
	// are this plugin's markup now, so what it needs from the host is chrome
	// rather than a data map.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// Sanitize cleans admin-authored news body HTML before it is
	// rendered unescaped (the host's news sanitization policy).
	Sanitize func(html string) string
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

// HomeFeedName is the extension key the plugin publishes its
// home-page card rows under. The host Looks it up after core.Boot and
// wires it into its home handler; a host that never looks it up
// simply has no news card.
const HomeFeedName = "news.home"

// HomeFeedFunc is the extension's type: up to 4 sanitized,
// template-ready published posts (a []safePost-shaped value).
type HomeFeedFunc func(ctx context.Context) any

// Handlers serves /news* + /admin/news*.
type Handlers struct {
	store Store
	errs  core.ErrorReporter
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "news",
		Version:     "1.0.0",
		Description: "Site news — public feed, per-post pages, admin editor.",
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps.RenderPage == nil || deps.Sanitize == nil {
		return fmt.Errorf("news: SetDeps not called (RenderPage/Sanitize required) — wire it in main() before core.Boot")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("news: Core.Storage.DB() is nil")
	}
	p.handlers = &Handlers{store: NewPGStore(db), errs: c.Errors}

	// Home page "Latest news" card — sanitized, template-ready rows.
	// Published as an extension rather than set directly on a host
	// hook: the plugin has no host import, so the host pulls the feed
	// out of the registry post-Boot (wireNewsHome in cmd/main.go).
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt interface{}
	}
	c.Register(HomeFeedName, HomeFeedFunc(func(ctx context.Context) any {
		raw, _ := p.handlers.store.GetPublishedNewsPosts(ctx, 4)
		out := make([]safePost, 0, len(raw))
		for _, post := range raw {
			out = append(out, safePost{post.ID, post.Title, post.Slug,
				template.HTML(deps.Sanitize(post.Body)), post.CreatedAt})
		}
		return out
	}))

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("news: Core.Router.Engine() is nil")
	}
	g := engine.Group("/news")
	g.Use(c.Auth.Authenticate()...)
	g.GET("", p.handlers.NewsAll)
	g.GET("/:slug", p.handlers.NewsDetail)

	adm := engine.Group("/admin/news")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.NewsList)
	adm.POST("", p.handlers.CreateNews)
	adm.GET("/:id/edit", p.handlers.EditNewsPage)
	adm.POST("/:id/update", p.handlers.UpdateNews)
	adm.POST("/:id/delete", p.handlers.DeleteNews)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

func (h *Handlers) NewsAll(c *gin.Context) {
	news, _ := h.store.GetPublishedNewsPosts(c.Request.Context(), 50)
	// Mark bodies as safe HTML so the template won't escape them — but ONLY
	// after Sanitize, exactly as NewsDetail does. This list previously passed
	// p.Body through raw: any markup an admin (or anything that ever reached
	// the admin form) stored was rendered unescaped on the public feed, while
	// the detail page for the same post was filtered. A host's Sanitize policy
	// cannot defend a path that never calls it.
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt interface{}
	}
	safe := make([]safePost, 0, len(news))
	for _, p := range news {
		safe = append(safe, safePost{p.ID, p.Title, p.Slug, template.HTML(deps.Sanitize(p.Body)), p.CreatedAt})
	}
	render(c, "News", "news.html", gin.H{"News": safe})
}

func (h *Handlers) NewsDetail(c *gin.Context) {
	slug := c.Param("slug")
	post, err := h.store.GetNewsPostBySlug(c.Request.Context(), slug)
	if err != nil {
		c.Redirect(http.StatusFound, "/news")
		return
	}
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt interface{}
	}
	safe := safePost{post.ID, post.Title, post.Slug, template.HTML(deps.Sanitize(post.Body)), post.CreatedAt}
	render(c, "News", "news_detail.html", gin.H{"Post": safe})
}

func (h *Handlers) NewsList(c *gin.Context) {
	posts, err := h.store.GetAllNewsPosts(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to get news posts")
		return
	}
	render(c, "News — admin", "admin_news.html", gin.H{"Posts": posts})
}

func (h *Handlers) CreateNews(c *gin.Context) {
	title := c.PostForm("title")
	slug := c.PostForm("slug")
	body := c.PostForm("body")
	published := c.PostForm("published") == "1"
	if slug == "" {
		// auto-slug from title
		slug = slugify(title)
	}
	_, err := h.store.CreateNewsPost(c.Request.Context(), title, slug, body, published)
	if err != nil {
		log.Printf("news/create-post: %v", err)
		c.String(http.StatusInternalServerError, "failed to create the news post")
		return
	}
	c.Redirect(http.StatusFound, "/admin/news")
}

func (h *Handlers) EditNewsPage(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	post, err := h.store.GetNewsPostByID(c.Request.Context(), id)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/news")
		return
	}
	render(c, "News post", "admin_news_form.html", gin.H{"Post": post})
}

func (h *Handlers) UpdateNews(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	title := c.PostForm("title")
	slug := c.PostForm("slug")
	body := c.PostForm("body")
	published := c.PostForm("published") == "1"
	if slug == "" {
		slug = slugify(title)
	}
	if err := h.store.UpdateNewsPost(c.Request.Context(), id, title, slug, body, published); err != nil {
		c.String(http.StatusInternalServerError, "failed to update news post")
		return
	}
	c.Redirect(http.StatusFound, "/admin/news")
}

func (h *Handlers) DeleteNews(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	_ = h.store.DeleteNewsPost(c.Request.Context(), id)
	c.Redirect(http.StatusFound, "/admin/news")
}

// slugify mirrors the host admin_handler helper byte-for-byte so
// new posts keep the same slug shape: every non-alphanumeric run
// becomes a single dash.
var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = reNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
