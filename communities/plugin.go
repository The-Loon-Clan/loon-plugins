// Package communities is the Reddit-style user-owned sub-forum
// surface (/c/*, migrations 252-253): communities with join gating
// (open / request+escrow / invite-only), per-community mods and
// rules, threads + replies, and banner/icon customisation. Third
// pkg/core plugin — and the first to exercise the Points facade
// (join costs escrow through core.Points with the typed
// spend_community_join / refund_community_join ledger entries).
//
// NOT in this package: the requests domain (models.NzbRequest et
// al in the confusingly-named pkg/models/community.go), the
// /community/* pages (forums plugin + host), and /community/hidden
// (the content_hide moderation queue).
//
// The old storage triple had no mock — this package deliberately
// ships PGStore only until a test surface needs a MemStore.
package communities

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("communities", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper. Provision builds
// the store + handlers over the Core mediator, registers the
// routes, and installs the account-page "Following" hook; there
// is no background work, so Start/Stop are no-ops.
type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "communities",
		Version:     "1.0.0",
		Description: "User-owned sub-forums (/c/*) with join gating, invites, points escrow, and per-community moderation.",
		// Tables ship via the core numbered migrations in the
		// public schema until the PG17 baseline consolidation.
	}
}

// Provision wires the communities surface into the host at its
// historical /c/* paths via Router.Engine(). Every route runs the
// site's default access policy (Authenticate); per-action gates
// (owner, community mod, login) live in the handlers, matching
// the pre-extraction behaviour.
func (p *Plugin) Provision(c *core.Core) error {
	// One check for every render seam, because a missing one is not a
	// degraded page — it is a nil call on the first request. Uploads are
	// deliberately excluded: a host with no blob store loses banner images
	// and keeps the rest, which is a configuration rather than a fault.
	if !deps.ready() {
		return fmt.Errorf("communities: SetDeps not called, or missing BaseData/Markdown/PageOffset/Pagination — wire it in main() before core.Boot")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("communities: Core.Storage.DB() is nil")
	}
	store := NewPGStore(db)
	p.handlers = NewHandlers(store, c.Auth, c.Points, c.Errors)

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("communities: Core.Router.Engine() is nil")
	}

	g := engine.Group("/c")
	g.Use(c.Auth.Authenticate()...)
	g.GET("", p.handlers.Index)
	g.GET("/new", p.handlers.NewCommunityForm)
	g.POST("", p.handlers.CreateCommunity)
	// /c/join/:code is a static-prefixed sibling of /c/:slug;
	// the radix tree matches "join" before the wildcard.
	g.GET("/join/:code", p.handlers.RedeemInvite)
	g.GET("/:slug", p.handlers.View)
	g.POST("/:slug/subscribe", p.handlers.ToggleSubscribe)
	g.GET("/:slug/settings", p.handlers.Settings)
	g.POST("/:slug/settings", p.handlers.SaveSettings)
	g.GET("/:slug/requests", p.handlers.RequestQueue)
	g.POST("/:slug/requests/:rid/approve", p.handlers.ApproveRequest)
	g.POST("/:slug/requests/:rid/deny", p.handlers.DenyRequest)
	g.POST("/:slug/invites", p.handlers.CreateInvite)
	g.GET("/:slug/submit", p.handlers.NewThreadForm)
	g.POST("/:slug/submit", p.handlers.CreateThread)
	g.GET("/:slug/thread/:id", p.handlers.ViewThread)
	g.POST("/:slug/thread/:id/reply", p.handlers.Reply)
	g.POST("/:slug/thread/:id/pin", p.handlers.PinThread)
	g.POST("/:slug/thread/:id/lock", p.handlers.LockThread)
	g.POST("/:slug/thread/:id/remove", p.handlers.RemoveThread)
	g.POST("/:slug/thread/:id/post/:pid/remove", p.handlers.RemovePost)

	// Account-settings "Following" tab. In-tree this assigned a host
	// package var directly; extracted, it is PUBLISHED and the host looks it
	// up after Boot — the same arrangement the news plugin uses for its
	// home-page card. A host that never looks it up simply has no Following
	// section, which is the correct degradation for an optional surface.
	if err := c.Register(FollowedName, FollowedFunc(func(ctx context.Context, userID int) any {
		rows, err := store.ListSubscribedCommunities(ctx, userID)
		if err != nil || len(rows) == 0 {
			return nil
		}
		return rows
	})); err != nil {
		return err
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
