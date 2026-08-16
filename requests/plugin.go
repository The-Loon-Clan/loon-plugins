package requests

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("requests", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper. Provision consumes the Deps
// installed by the host's SetDeps call plus the Core services, and registers
// the board's historical routes; there is no background work, so Start/Stop
// are no-ops.
type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "requests",
		Version:     "1.0.0",
		Description: "Community request board — filing, votes, points boosts, torrent scraping, fulfilment lifecycle.",
		// Processes empty → web-only, the safe default for a
		// route-registering plugin.
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if c.Process != "web" && c.Process != "all" {
		return nil
	}
	if !deps.ok() {
		return fmt.Errorf("requests: SetDeps was not called with a full Deps before core.Boot — wire it in the host's composition root (stores + chrome + vocabulary; Prowlarr/Torznab/RefreshAnime are the only optionals)")
	}
	if err := parseTemplates(); err != nil {
		return err
	}
	p.handlers = &Handlers{deps: *deps, points: c.Points, errs: c.Errors}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("requests: Core.Router.Engine() is nil")
	}

	// Historical paths, kept exactly: host code links into them
	// (/community/broken's alternatives panel, upload redirects), and the
	// host's page-policy table covers /community/calendar/* by prefix.
	g := engine.Group("/community")
	g.Use(c.Auth.Authenticate()...)
	g.GET("/requests", p.handlers.RequestsPage)
	g.GET("/request/:id", p.handlers.RequestDetail)
	g.POST("/requests", p.handlers.CreateRequest)
	g.POST("/requests/:id/edit", p.handlers.EditRequest)
	g.POST("/requests/:id/delete", p.handlers.DeleteRequest)
	g.POST("/requests/bulk-delete", p.handlers.BulkDeleteRequests)
	g.POST("/requests/:id/fulfill", p.handlers.FulfillRequest)
	g.POST("/requests/:id/retry", p.handlers.RetryRequest)
	g.POST("/requests/:id/unpark", p.handlers.UnparkRequest)
	g.POST("/requests/:id/requeue", p.handlers.RequeueRequest)
	g.POST("/requests/:id/vote", p.handlers.VoteRequest)
	g.POST("/requests/:id/boost", p.handlers.BoostRequest)
	g.GET("/requests/scrape", p.handlers.ScrapeNyaa)
	g.GET("/requests/search", p.handlers.SearchTorrents)
	g.GET("/requests/lookup", p.handlers.LookupAnime)
	// The calendar page's torrent search shares the board's search
	// backends; the path is historical.
	g.GET("/calendar/search", p.handlers.SearchNekoBT)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
