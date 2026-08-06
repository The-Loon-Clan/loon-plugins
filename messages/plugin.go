// Package messages is the messaging surface: the unified /inbox (direct-message
// threads and system announcements in one list), the DM send/read/block flows,
// and the /admin/messages broadcast composer.
//
// Ported from the origin site's in-tree plugin. The handlers are a verbatim
// lift — what changed is everything underneath them:
//
//   - four host repositories became one Store this package defines, with a
//     Postgres and an in-memory implementation, so a host owes it an interface
//     rather than its storage layer;
//   - user lookup and notification delivery moved to core.Users and
//     core.Notifications, which every loon host already provides;
//   - the page chrome arrives through the BaseData seam, the same way the
//     forum plugin takes it.
//
// It owns no tables. On the origin site the IRC plugin also writes DMs for
// whisper delivery, so the schema stays host-owned and shared; schema.sql in
// this directory is what a host without those tables runs to provide them.
package messages

import (
	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("messages", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams this plugin needs beyond what core provides.
//
// Only two, and both for reasons core cannot cover: the store is the host's
// schema, and BaseData is the host's page chrome. Everything else — auth,
// users, notifications, entitlements, error reporting — comes from core.
type Deps struct {
	// Store is OPTIONAL: leave it nil and Provision builds a PGStore over
	// core.Storage.DB(), which is what a host with the schema wants. Supply
	// one to point the plugin at a different backing — a MemStore for a demo,
	// or an adapter over a host's existing repositories.
	Store Store

	// RenderPage wraps a finished fragment in the site chrome. Both pages are
	// this plugin's markup now, so it needs chrome rather than a data map.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// CSRFToken for the compose and reply forms — minted by host middleware.
	CSRFToken func(c *gin.Context) string
	// RenderEditor is the site's shared markdown editor as ready HTML, for
	// the options given. Seven pages across the site use it, so it stays
	// host-rendered; only the per-call options cross.
	RenderEditor func(opts map[string]any) template.HTML
	// Markdown renders a message body. It SANITISES, so it crosses rather
	// than being reimplemented: a second allow-list in here is a stored-XSS
	// bug waiting on whichever copy is laxer.
	Markdown func(string) template.HTML
	// RelativeTime formats a timestamp as "2 hours ago". Passed rather than
	// copied for consistency of wording across the site, not for safety —
	// this is the weaker reason of the two, and worth saying so.
	RelativeTime func(any) string
	// RenderPagination is the site's pager as finished HTML.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML

	// ListUsers is OPTIONAL: it backs the admin composer's recipient picker.
	//
	// Core has no "list every user" method, and rightly so — on a site of any
	// size that is a page-breaking query. So a host that wants a dropdown
	// supplies one, and a host that does not gets the username field the
	// template falls back to. The send path resolves by username either way,
	// so nothing is lost but the convenience.
	ListUsers func(ctx context.Context) ([]UserOption, error)
}

// UserOption is one entry in the composer's recipient picker.
type UserOption struct {
	ID       int
	Username string
}

var deps *Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = &d }

// Handlers serves /inbox* and /admin/messages*.
type Handlers struct {
	store  Store
	auth   core.AuthService
	users  core.UsersService
	notify core.NotificationsService
	errs   core.ErrorReporter
	// ents answers "may this user START a conversation?". An access decision,
	// so it belongs to core rather than to whichever plugin happens to own
	// ranks — the messaging surface should not have to know that paid ranks
	// exist, only whether this person is allowed.
	ents core.EntitlementsService
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "messages",
		Version:     "1.0.0",
		Description: "Messaging — unified inbox (DMs + announcements), DM flows, admin broadcast composer.",
		// web only: no jobs, and the routes are page-shaped.
		Processes: []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps == nil {
		return fmt.Errorf("messages: SetDeps was not called before core.Boot — wire it in main()")
	}
	if deps.RenderPage == nil || deps.CSRFToken == nil || deps.RenderEditor == nil ||
		deps.Markdown == nil || deps.RelativeTime == nil || deps.RenderPagination == nil {
		// Not optional: without the host's chrome these pages render as though
		// nobody is signed in, which looks like a broken session rather than a
		// missing seam.
		return fmt.Errorf("messages: Deps is missing a render seam — RenderPage, CSRFToken, " +
			"RenderEditor, Markdown, RelativeTime and RenderPagination are all required")
	}
	// Parsed here, not at package init: markdown and relativeTime are Deps
	// functions. Forgetting this call leaves pageTmpl nil and panics on the
	// first page view rather than failing at boot.
	parseTemplates()
	// Default the store to Postgres over the host's handle, the same way the
	// forum plugin does. A host that has the schema (see schema.sql) wires
	// nothing; one that wants a different backing supplies Deps.Store.
	store := deps.Store
	if store == nil {
		db := c.Storage.DB()
		if db == nil {
			return fmt.Errorf("messages: Core.Storage.DB() is nil and no Deps.Store was supplied")
		}
		store = NewPGStore(db)
	}
	p.handlers = &Handlers{
		store: store, auth: c.Auth, users: c.Users,
		notify: c.Notifications, errs: c.Errors, ents: c.Entitlements,
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("messages: Core.Router.Engine() is nil")
	}

	// Authed surface. Every /inbox route registers on ONE group so Gin's tree
	// matches them exactly as the origin site's pre-plugin wiring did — split
	// across groups, the parameterised routes start shadowing the literal ones.
	authed := engine.Group("/")
	authed.Use(c.Auth.Authenticate()...)
	authed.GET("/inbox", p.handlers.Inbox)
	authed.POST("/inbox/:id/read", p.handlers.MarkRead)
	authed.POST("/inbox/:id/dismiss", p.handlers.DismissMessage)
	authed.GET("/inbox/dm", p.handlers.InboxDM)
	authed.POST("/inbox/dm/send", p.handlers.SendDM)
	authed.POST("/inbox/dm/:id/read", p.handlers.MarkDMRead)
	authed.POST("/inbox/dm/:id/delete", p.handlers.DeleteDMThread)
	authed.POST("/inbox/dm/block", p.handlers.BlockDMUser)
	authed.POST("/inbox/dm/unblock", p.handlers.UnblockDMUser)

	// Admin broadcast composer — mod or above.
	adm := engine.Group("/admin/messages")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminMessages)
	adm.POST("", p.handlers.AdminSend)
	adm.POST("/:id/delete", p.handlers.AdminDelete)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
