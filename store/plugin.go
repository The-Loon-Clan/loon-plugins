// Package store is the points store — users spend points on catalog
// items. The first reward type is a rank, granted through the
// pluginapi.RankGranter capability the ranks plugin publishes, so the
// store never imports ranks. Self-contained data layer in its own
// `store` schema (store.items / store.purchases, migrations 001-002); no SetDeps.
package store

import (
	"context"
	"embed"
	"fmt"
	"github.com/gin-gonic/gin"
	"html/template"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// storeMigrations holds the plugin's own schema. loon's
// RunPluginMigrations creates the "store" Postgres schema at boot and
// applies these files into it (search_path scoped), tracked in
// core.plugin_migrations. Store is the first plugin to own a schema
// this way; the older plugins still ship host-numbered public-schema
// migrations pending the same conversion.
//
//go:embed migrations/*.sql
var storeMigrations embed.FS

// FlavourExtension is the registry key a host publishes its flavour answer
// under: func() (indexer, tracker bool). See Item.Flavour.
const FlavourExtension = "store.flavour"

func init() {
	core.RegisterPlugin("store", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper. Web/all only (session
// UI) — empty Metadata.Processes defaults to "web", so the bare api
// engine and the worker never see its routes.
// Deps are the host seams this plugin cannot get from the Core.
//
// It renders the HOST's templates (store.html, store_history.html,
// admin_store.html — see README for the template contract), so the host
// supplies its page-chrome injector and its pagination view-model builder.
// Same shape as the forum plugin's, deliberately: this is the third plugin to
// need exactly these, and a second dialect would be worse than the coupling.
//
// The session user is NOT here. It comes off core.Auth, which every plugin
// already has — taking it as a dep would mean handing over a host type
// (*models.User) when all three call sites want an id.
type Deps struct {
	// BaseData merges the host's page chrome (user, nav, CSRF, notification
	// counts, ...) into a template data map — every page render goes through
	// it, exactly as host-side handlers do.
	// RenderPage wraps a finished fragment in the site chrome. The pages are
	// this plugin's markup now, so it needs chrome rather than a data map.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// CSRFToken for the store and admin forms. Host-owned: the token is
	// minted and validated by host middleware.
	CSRFToken func(c *gin.Context) string
	// Paginate builds the view-model the host's pagination partial consumes.
	// Returned as `any` so the plugin never learns the host's type — the
	// template reads it by field name.
	// RenderPagination returns the site's pager as ready HTML. A fragment is
	// rendered by this plugin's own template set and cannot reach the host's
	// partials, so the host renders its own rather than a second copy living
	// here.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML
	// PageOffset is the SQL offset for a page. A separate seam from Paginate
	// because the offset is needed BEFORE the query and the view-model after
	// it — the host's PaginationFromTotal returns both at once, which only
	// works when you already know the total. Taking it as a dep rather than
	// writing (page-1)*size inline keeps the plugin on the host's one
	// implementation, which is the rule on the host side too.
	PageOffset func(page, pageSize int) int
	// ExtraTabs lets the HOST add entries to the store's tab strip, for pages
	// that belong to the points area without belonging to this plugin — a
	// rewards claim page, say. Optional: nil means the strip is Store and
	// History, exactly as before.
	//
	// The host supplies tabs rather than this plugin linking to them, because
	// the alternative is a hardcoded URL to a page that may not exist. A site
	// running store WITHOUT whatever serves that page would render a tab that
	// 404s, and store cannot know which those are.
	ExtraTabs func(c *gin.Context) []Tab
	// SuppressTabs stops this plugin drawing its own Store | History strip,
	// for a host whose chrome already navigates the points area.
	//
	// The strip exists because a plugin cannot assume the host has navigation:
	// dropped into a site with none, these pages would be a dead end. That is
	// the right default, and it stays the default — but a host that carries an
	// account bar covering /store and /store/history ends up rendering two rows
	// of tabs, one above the other, offering the same places.
	//
	// The host is the only side that can know, so it is the side that says.
	// Independent of ExtraTabs on purpose: a host that turns the strip back on
	// must not also have to remember to re-wire its extra tabs.
	SuppressTabs bool
}

// Tab is one host-supplied entry on the store's tab strip.
//
// Active is the host's call: it owns the routes these point at, so it is the
// only side that can say which one the reader is on. The store's own two tabs
// mark themselves.
type Tab struct {
	Label  string
	Href   string
	Active bool
}

// extraTabs resolves the host's tabs for a request, tolerating an unset seam.
// Returns nil rather than an empty slice so `{{if}}` in a template reads the
// way it looks.
func extraTabs(c *gin.Context) []Tab {
	if deps.ExtraTabs == nil {
		return nil
	}
	return deps.ExtraTabs(c)
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

type Plugin struct {
	core     *core.Core
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "store",
		Version:     "1.0.0",
		Description: "Points store — spend points on catalog items; rank items granted via the ranks capability.",
		Flavours:    []string{core.FlavourAny},
		// Provisioned after ranks so the RankGranter capability is on
		// the extension registry before this plugin's Lookup runs.
		Requires: []string{"ranks"},
		// Owns its tables in the dedicated "store" Postgres schema.
		Migrations: storeMigrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("store: Core.Storage.DB() is nil")
	}
	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("store: Core.Router.Engine() is nil")
	}

	// Consume the rank-granting capability published by ranks. It is
	// declared in Metadata.Requires, so a missing or wrong-typed
	// registration is a wiring bug — fail boot loudly rather than sell
	// rank items that can never be granted.
	svc, ok := c.Lookup(pluginapi.RankGranterName)
	if !ok {
		return fmt.Errorf("store: %q not registered — is the ranks plugin enabled?", pluginapi.RankGranterName)
	}
	granter, ok := svc.(pluginapi.RankGranter)
	if !ok {
		return fmt.Errorf("store: %q is %T, not pluginapi.RankGranter", pluginapi.RankGranterName, svc)
	}

	// The invite capability comes from the HOST, not a sibling plugin, so it
	// cannot go in Metadata.Requires (which orders plugins) and its absence
	// must not fail boot — a host with no invite system is a legitimate host.
	// Look it up softly; grantReward fails the individual purchase, and only
	// for invite items, if it is missing.
	// The perk granter, on the same terms as invites below: published by a
	// sibling plugin that a host may simply not install, so its absence is a
	// per-purchase failure rather than a boot one. A wrong TYPE under the right
	// name is still a wiring bug and still fails loudly.
	var perks pluginapi.PerkGranter
	if svc, ok := c.Lookup(pluginapi.PerkGranterName); ok {
		g, ok := svc.(pluginapi.PerkGranter)
		if !ok {
			return fmt.Errorf("store: %q is %T, not pluginapi.PerkGranter",
				pluginapi.PerkGranterName, svc)
		}
		perks = g
	}

	// The flair granter, on the same terms as perks: a sibling plugin a host
	// may not install, so absence fails the individual flair purchase rather
	// than boot, and a wrong type under the right name is a loud wiring bug.
	var flair pluginapi.FlairGranter
	if svc, ok := c.Lookup(pluginapi.FlairGranterName); ok {
		g, ok := svc.(pluginapi.FlairGranter)
		if !ok {
			return fmt.Errorf("store: %q is %T, not pluginapi.FlairGranter",
				pluginapi.FlairGranterName, svc)
		}
		flair = g
	}

	var invites pluginapi.InviteGranter
	if svc, ok := c.Lookup(pluginapi.InviteGranterName); ok {
		if g, ok := svc.(pluginapi.InviteGranter); ok {
			invites = g
		} else {
			// Registered under the right name with the wrong type is a wiring
			// bug, not a missing feature. Say so rather than silently
			// behaving like a host that has no invites at all.
			return fmt.Errorf("store: %q is %T, not pluginapi.InviteGranter", pluginapi.InviteGranterName, svc)
		}
	}

	// Which site flavours are on, for hiding items whose half is off. A host
	// registers func() (indexer, tracker bool) under store.flavour; absent
	// means every item shows.
	var halves func() (bool, bool)
	if svc, ok := c.Lookup(FlavourExtension); ok {
		fn, ok := svc.(func() (bool, bool))
		if !ok {
			return fmt.Errorf("store: %q is %T, not func() (bool, bool)", FlavourExtension, svc)
		}
		halves = fn
	}

	if deps.RenderPage == nil || deps.CSRFToken == nil ||
		deps.RenderPagination == nil || deps.PageOffset == nil {
		return fmt.Errorf("store: SetDeps not called (BaseData/Paginate/PageOffset required) — wire it in cmd/main.go before core.Boot")
	}
	p.handlers = &Handlers{
		auth:    c.Auth,
		store:   NewPGStore(db),
		points:  c.Points,
		granter: granter,
		halves:  halves,
		invites: invites,
		perks:   perks,
		flair:   flair,
		errs:    c.Errors,
		core:    c,
	}

	// Announce purchases so achievements and stats can react without the store
	// knowing they exist.
	if err := declareEvents(c); err != nil {
		return err
	}

	shop := engine.Group("/store")
	shop.Use(c.Auth.Authenticate()...)
	shop.GET("", p.handlers.StorePage)
	shop.GET("/history", p.handlers.HistoryPage)
	shop.POST("/buy/:id", p.handlers.BuyItem)

	adm := engine.Group("/admin/store")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminStorePage)
	adm.POST("/create", p.handlers.CreateItem)
	adm.POST("/:id/update", p.handlers.UpdateItem)
	adm.POST("/:id/delete", p.handlers.DeleteItem)

	return nil
}

// Start looks up the tracker's transfer credit — in Start, not Provision,
// because the tracker is a sibling whose registration order is nobody's
// promise (the games plugin learned this the same afternoon). Absent is a
// legitimate host (no tracker, or an idle one) and fails only credit-item
// purchases; wrong-typed is a wiring bug and fails loudly.
func (p *Plugin) Start(ctx context.Context) error {
	if svc, ok := p.core.Lookup(pluginapi.TrackerCreditName); ok {
		g, ok := svc.(pluginapi.TrackerCredit)
		if !ok {
			return fmt.Errorf("store: %q is %T, not pluginapi.TrackerCredit",
				pluginapi.TrackerCreditName, svc)
		}
		p.handlers.credit = g
	}
	if svc, ok := p.core.Lookup(pluginapi.MedalGranterName); ok {
		g, ok := svc.(pluginapi.MedalGranter)
		if !ok {
			return fmt.Errorf("store: %q is %T, not pluginapi.MedalGranter",
				pluginapi.MedalGranterName, svc)
		}
		p.handlers.medals = g
	}
	return nil
}
func (p *Plugin) Stop(ctx context.Context) error { return nil }
