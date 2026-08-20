package wiki

import (
	"context"
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("wiki", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams the wiki needs beyond what core provides. The
// plugin renders the HOST's templates (wiki.html and friends — see README
// for the template contract), so the host supplies its page-chrome injector
// and markdown renderer; the upload pair localises the one filesystem
// assumption the in-tree plugin hardcoded. SetDeps must be called before
// core.Boot; Provision fails loud otherwise.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The six pages
	// are this plugin's markup now, so it needs chrome rather than a data map.
	//
	// status is a parameter because the admin forms re-render themselves on a
	// validation failure, and a seam fixed at 200 would report success while
	// showing an error.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)

	// BaseData is the PREVIOUS contract, kept working while loon-demo-site
	// migrates. A host that sets this instead of RenderPage gets the old
	// behaviour: rendered by template NAME out of the host's own directory.
	//
	// It exists because loon-demo-site is maintained separately and builds
	// against this working tree — shipping only the new seam would break a
	// build in someone else's session for code they did not write. Delete it,
	// and the legacy branch in views.go, once demo sets RenderPage.
	BaseData func(c *gin.Context, extra gin.H) gin.H
	// Markdown renders trusted-editor wiki markdown to HTML. Wiki authors
	// are mods+, so hosts may allow richer markup here than in user-
	// authored surfaces — wire whatever renderer the host's wiki pages
	// already use.
	Markdown func(src string) template.HTML
	// Files is where admin image uploads are stored. The plugin saves
	// under the "wiki-uploads/" namespace within the store; the host
	// decides where that lives on disk (or, later, remotely) and what
	// public URL it serves under.
	Files blob.Store
	// CSRFToken supplies the double-submit token for the admin forms — the
	// host's session concern. REQUIRED: this plugin shipped without it, all
	// seven admin forms posted tokenless, and the host's CSRF middleware
	// refused every one with 403 — the wiki admin could not create or delete
	// anything. The access audit probes WITH a valid token by design, so only
	// counting tokens in the rendered forms catches this class.
	CSRFToken func(c *gin.Context) string

	// SiteName is what this deployment calls itself, for the index heading.
	//
	// It exists because the heading said "ameNZB Wiki" — the name of the site
	// this plugin was lifted out of — on every host that ran it. A plugin
	// cannot know the name and must not guess it, and there is exactly one
	// right answer per deployment, so the host says.
	//
	// Optional: absent, the heading is just "Wiki", which is true everywhere.
	SiteName func() string
}

// siteName resolves the deployment's name, empty when the host set no seam.
func siteName() string {
	if deps.SiteName == nil {
		return ""
	}
	return strings.TrimSpace(deps.SiteName())
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

// Plugin is the core.Plugin lifecycle wrapper. Provision builds
// the store + handlers over the Core mediator and registers the
// routes; there is no background work, so Start/Stop are no-ops.
type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "wiki",
		Version:     "1.0.0",
		Description: "Knowledge base — topics containing long-form markdown articles (Help, FAQ, guides).",
		Flavours:    []string{core.FlavourAny},
		// Tables (wiki_topics, wiki_posts) still ship via the core
		// numbered migrations in the public schema; Migrations stays
		// empty until the PG17 baseline consolidation moves them
		// into a dedicated `wiki` schema.
	}
}

// Provision wires the wiki into the host. Routes keep their
// historical top-level paths (/wiki/*, /admin/wiki/*) rather than
// the /plugin/wiki/* default — RouterService explicitly allows
// domain-specific paths via Engine(), and moving them would break
// bookmarks, templates, and sitemap URLs for zero gain.
func (p *Plugin) Provision(c *core.Core) error {
	if (deps.RenderPage == nil && deps.BaseData == nil) || deps.Markdown == nil || deps.Files == nil || deps.CSRFToken == nil {
		return fmt.Errorf("wiki: SetDeps not called (RenderPage or BaseData, plus Markdown/Files/CSRFToken — wire it in main() before core.Boot)")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("wiki: Core.Storage.DB() is nil")
	}
	p.handlers = NewHandlers(NewPGStore(db), c.Auth).WithCore(c)
	if err := declareEvents(c); err != nil {
		return err
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("wiki: Core.Router.Engine() is nil")
	}

	// Public pages follow the site's default access policy
	// (closed mode: login required; public mode: anonymous
	// browsing allowed) — the same gate the routes had when they
	// lived in main.go's `authorized` group.
	pub := engine.Group("/wiki")
	pub.Use(c.Auth.Authenticate()...)
	pub.GET("", p.handlers.Index)
	// Static-path siblings of /wiki/:topic — Gin's radix tree
	// matches these before the wildcard, so /wiki/recent and
	// /wiki/random don't collide with /wiki/<topic-slug>.
	pub.GET("/recent", p.handlers.RecentChanges)
	pub.GET("/random", p.handlers.Random)
	pub.GET("/:topic", p.handlers.Topic)
	pub.GET("/:topic/:post", p.handlers.Post)

	// Admin CRUD — mod-or-above, matching the host /admin group's
	// AuthRequired + AdminRequired stack.
	adm := engine.Group("/admin/wiki")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminIndex)
	adm.GET("/topics/new", p.handlers.NewTopic)
	adm.POST("/topics", p.handlers.CreateTopic)
	adm.GET("/topics/:id/edit", p.handlers.EditTopic)
	adm.POST("/topics/:id/update", p.handlers.UpdateTopic)
	adm.POST("/topics/:id/delete", p.handlers.DeleteTopic)
	adm.GET("/posts/new", p.handlers.NewPost)
	adm.POST("/posts", p.handlers.CreatePost)
	adm.GET("/posts/:id/edit", p.handlers.EditPost)
	adm.POST("/posts/:id/update", p.handlers.UpdatePost)
	adm.POST("/posts/:id/delete", p.handlers.DeletePost)
	adm.POST("/upload", p.handlers.UploadImage)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
