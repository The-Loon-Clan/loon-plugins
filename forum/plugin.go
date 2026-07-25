package forum

import (
	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("forum", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams the forum needs beyond what core provides. The
// plugin renders the HOST's templates (community_forums.html and friends —
// see README for the template contract), so the host supplies its page-chrome
// injector, its markdown renderer, and its pagination view-model builder.
// SetDeps must be called before core.Boot; Provision fails loud otherwise.
type Deps struct {
	// BaseData merges the host's page chrome (user, nav, CSRF, notification
	// counts, ...) into a template data map — every page render goes through
	// it, exactly as host-side handlers do.
	BaseData func(c *gin.Context, extra gin.H) gin.H
	// Markdown renders untrusted forum markdown to sanitized HTML. Whatever
	// the host uses for its other user-authored surfaces, so forum posts
	// render identically to comments/DMs.
	Markdown func(src string) template.HTML
	// Paginate builds the view-model the host's pagination partial consumes.
	// Returned as `any` so the plugin never learns the host's type — the
	// template reads it by field name.
	Paginate func(page, totalPages int, baseURL string) any
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

// SpotlightName is the extension key the plugin publishes its home-page
// spotlight reader under (type SpotlightFunc). A host that renders a
// "Community Spotlight" card looks it up after Boot and feeds the card;
// hosts without one simply don't.
const SpotlightName = "forum.spotlight"

// SpotlightFunc returns the most-recently-active threads for a home-page
// card, or nil when there is nothing to show. The result is duck-typed by
// the host template.
type SpotlightFunc func(ctx context.Context, limit int) any

// Plugin is the core.Plugin lifecycle wrapper. Provision builds
// the store + handlers over the Core mediator, registers the
// routes, and publishes the home-page spotlight extension; there is
// no background work, so Start/Stop are no-ops.
type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "forum",
		Version:     "1.0.0",
		Description: "Site-wide discussion board — categories, threads, quote-replies, reactions.",
		// Tables ship via the host's numbered migrations in the
		// public schema until the PG17 baseline consolidation —
		// see README (Data) for the required tables.
	}
}

// Provision wires the forum into the host. Routes keep their
// historical top-level paths (/community/forums/*,
// /admin/forum-categories/*) via Router.Engine() — moving them
// would break every bookmark and template link for zero gain.
func (p *Plugin) Provision(c *core.Core) error {
	if deps.BaseData == nil || deps.Markdown == nil || deps.Paginate == nil {
		return fmt.Errorf("forum: SetDeps not called (BaseData/Markdown/Paginate required) — wire it in main() before core.Boot")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("forum: Core.Storage.DB() is nil")
	}
	store := NewPGStore(db)
	p.handlers = NewHandlers(store, c.Auth, c.Users, c.Notifications)

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("forum: Core.Router.Engine() is nil")
	}

	// Public pages follow the site's default access policy (the
	// same gate the routes had in main.go's `authorized` group);
	// write handlers gate on the loaded user themselves, matching
	// the pre-extraction behaviour in public mode.
	pub := engine.Group("/community/forums")
	pub.Use(c.Auth.Authenticate()...)
	pub.GET("", p.handlers.Forums)
	pub.GET("/category/:id", p.handlers.ForumCategory)
	pub.GET("/thread/:id", p.handlers.ForumThread)
	pub.GET("/new", p.handlers.NewThread)
	pub.POST("/threads", p.handlers.CreateThread)
	pub.POST("/thread/:id/reply", p.handlers.ReplyThread)
	pub.POST("/post/:id/edit", p.handlers.EditPost)
	pub.POST("/post/:id/delete", p.handlers.DeletePost)
	pub.POST("/post/:id/react", p.handlers.ReactPost)
	pub.POST("/thread/:id/delete", p.handlers.DeleteThread)

	// Thread moderation — mod-or-above, matching the old
	// communityAdmin group's AuthRequired + AdminRequired stack.
	mod := engine.Group("/community/forums/thread")
	mod.Use(c.Auth.RequireUser(core.RoleMod)...)
	mod.POST("/:id/pin", p.handlers.AdminPinThread)
	mod.POST("/:id/lock", p.handlers.AdminLockThread)

	// Category management — mod-or-above, same gate as the host
	// /admin group it moved out of.
	adm := engine.Group("/admin/forum-categories")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminCategories)
	adm.POST("", p.handlers.AdminCreateCategory)
	adm.POST("/:id", p.handlers.AdminUpdateCategory)
	adm.POST("/:id/delete", p.handlers.AdminDeleteCategory)
	adm.POST("/:id/merge", p.handlers.AdminMergeCategory)

	// Home-page Community Spotlight — published as an extension so
	// the host (a separate module) can look it up after Boot and
	// feed its card without importing this package's internals.
	return c.Register(SpotlightName, SpotlightFunc(func(ctx context.Context, limit int) any {
		threads, err := store.GetRecentForumThreads(ctx, limit)
		if err != nil || len(threads) == 0 {
			return nil
		}
		return threads
	}))
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
