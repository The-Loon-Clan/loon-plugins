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
	// Store backs both halves of the inbox. Required.
	Store Store

	// BaseData merges the host's page chrome (signed-in user, nav counts,
	// CSRF token, theme) into a template context. Required, because these
	// pages render inside the host's layout: without it the templates render
	// logged-out chrome to a signed-in reader.
	BaseData func(c *gin.Context, extra gin.H) gin.H

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
	if deps.Store == nil {
		return fmt.Errorf("messages: Deps.Store is required")
	}
	if deps.BaseData == nil {
		// Not optional: without the host's chrome these pages render as though
		// nobody is signed in, which looks like a broken session rather than a
		// missing seam.
		return fmt.Errorf("messages: Deps.BaseData is required — it supplies the host's page chrome")
	}
	p.handlers = &Handlers{
		store: deps.Store, auth: c.Auth, users: c.Users,
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
