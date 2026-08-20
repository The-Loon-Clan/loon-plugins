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
	// RenderPage wraps a finished fragment in the site chrome. All five
	// pages are this plugin's markup now, so it needs chrome rather than a
	// data map. status crosses too: the new-thread page re-renders on a
	// validation failure, and a 200 saying "rejected" would lie to a client.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)
	// Markdown renders untrusted forum markdown to sanitized HTML. Whatever
	// the host uses for its other user-authored surfaces, so forum posts
	// render identically to comments/DMs.
	//
	// This is the one helper that crosses for SAFETY rather than for
	// consistency: a second allow-list inside the plugin is a stored-XSS bug
	// waiting on whichever copy is laxer.
	Markdown func(src string) template.HTML
	// CSRFToken for the reply, edit, moderation and new-thread forms —
	// minted by host middleware, so only the host can answer it.
	CSRFToken func(c *gin.Context) string
	// RenderEditor is the site's shared markdown editor as ready HTML, for
	// the options given. Seven pages across the site use it, so it stays
	// host-rendered and only the per-call options cross.
	RenderEditor func(opts map[string]any) template.HTML
	// RenderPagination is the site's pager as finished HTML.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML
	// RenderReportModal is the site-wide "report this content" dialog. The
	// forum's report buttons target it by id, so a host that omits it gets
	// buttons that open nothing — hence required rather than optional.
	RenderReportModal func(c *gin.Context) template.HTML
	// RelativeTime formats a timestamp as "2 hours ago". Passed rather than
	// copied for consistency of wording across the site, not for safety —
	// the weaker of the two reasons, and worth saying so.
	RelativeTime func(any) string

	// BaseData and Paginate are the PREVIOUS contract, where the HOST owned
	// these five templates and the plugin rendered them by name.
	//
	// Kept working because loon-demo-site still wires it and compiles against
	// this working tree — breaking it would surface as a build error in
	// someone else's session about code they did not write. Remove both, and
	// the branches in render()/paginate() that read them, once the demo has
	// moved to RenderPage. See TestLegacyContractIsStillAccepted, which also
	// refuses a MIXTURE of the two.
	BaseData func(c *gin.Context, extra gin.H) gin.H
	Paginate func(page, totalPages int, baseURL string) any

	// RepBadge names the earned reputation tier shown beside a poster's name.
	//
	// OPTIONAL, and the only Deps entry here that is site VOCABULARY rather
	// than a rendering service: what the tiers are called is the operator's
	// decision, and a plugin that hardcoded one site's ladder would ship
	// those words to every adopter. A host that leaves it nil simply shows
	// no badge.
	RepBadge func(tier int) RepBadgeInfo
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
		Flavours:    []string{core.FlavourAny},
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
	if !deps.ready() {
		return fmt.Errorf("forum: SetDeps not called, or a render seam is missing — " +
			"Markdown plus either the current contract (RenderPage, CSRFToken, " +
			"RenderEditor, RenderPagination, RenderReportModal, RelativeTime) or the " +
			"previous one (BaseData, Paginate); wire it in main() before core.Boot")
	}
	// Parsed here, not at package init: RelativeTime is a Deps function.
	// Forgetting this leaves pageTmpl nil and panics on the first page view
	// rather than failing at boot. Skipped on the legacy contract, where the
	// host renders its own copies of these templates by name.
	parseTemplates()
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("forum: Core.Storage.DB() is nil")
	}
	store := NewPGStore(db)
	p.handlers = NewHandlers(store, c.Auth, c.Users, c.Notifications).WithCore(c)

	// Announce what members do here, so plugins that care — achievements,
	// stats — can listen without the forum knowing they exist. Declared
	// (rather than only emitted) so they appear in the directory before
	// anything fires; a subscriber cannot discover an event by waiting for it.
	//
	// A duplicate declaration fails Provision on purpose: two plugins both
	// believing they own "forum.post.created" is a wiring bug, not a warning.
	if err := declareEvents(c); err != nil {
		return err
	}

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

// ready reports whether the host wired one COMPLETE contract.
//
// Deliberately not "enough seams to limp": a half-wired host would render
// some pages and blank others, which reads as a broken site rather than a
// missing call. Markdown is required either way — it is the sanitiser.
func (d Deps) ready() bool {
	if d.Markdown == nil {
		return false
	}
	modern := d.RenderPage != nil && d.CSRFToken != nil && d.RenderEditor != nil &&
		d.RenderPagination != nil && d.RenderReportModal != nil && d.RelativeTime != nil
	legacy := d.BaseData != nil && d.Paginate != nil
	return modern || legacy
}
