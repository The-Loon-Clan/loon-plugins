package offers

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("offers", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper.
//
// Three processes, each doing something different: web serves the member and
// admin pages plus the external API, api serves ONLY the external API (it has
// no session or template stack), and worker owns the sweeper and pruner loops.
// Start gates on the process kind captured at Provision so a split-mode web
// process never races the worker's jobs.
type Plugin struct {
	handlers *Handlers
	process  string
	sweeper  *offerSweeper
	pruner   *offerPruner
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "offers",
		Version:     "1.0.0",
		Description: "Offer system — register deliverable content, file requests against buckets, external-agent claim/deliver API.",
		Processes:   []string{"web", "worker", "api"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.process = c.Process

	// Declared before the per-process branches below: the worker returns early
	// and would otherwise disagree with the web process about what this plugin
	// announces, which is the kind of split-brain a directory exists to prevent.
	if err := declareEvents(c); err != nil {
		return err
	}

	if p.process == "worker" || p.process == "all" {
		if !jobDeps.ok() {
			return fmt.Errorf("offers: SetJobDeps was not called with a full JobDeps before core.Boot")
		}
		p.sweeper = newOfferSweeper(*jobDeps)
		p.pruner = newOfferPruner(*jobDeps)
	}
	if p.process == "worker" {
		return nil // headless worker: no routes
	}

	// The api process serves only the external surface, so it needs the API
	// half of Deps and not the page half. Checking them separately is what
	// lets that process boot without a template stack.
	if p.process == "api" {
		if !deps.okAPI() {
			return fmt.Errorf("offers: SetDeps was not called with the API half of Deps before core.Boot")
		}
	} else if !deps.okWeb() {
		return fmt.Errorf("offers: SetDeps was not called with a full Deps before core.Boot")
	}
	p.handlers = &Handlers{core: c}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("offers: Core.Router.Engine() is nil")
	}

	// External agent API. Bearer/apikey auth happens inside resolveAPIUser --
	// these clients have no session cookie -- so the group carries no session
	// middleware, and the host's CSRF middleware skips /api/*.
	//
	// Every path here is unchanged from the host version. Agents are deployed
	// in the wild and do not redeploy when we refactor.
	api := engine.Group("/api/offers")
	api.POST("/hash-check", p.handlers.HashCheck)
	api.POST("/register", p.handlers.Register)
	api.POST("/heartbeat", p.handlers.Heartbeat)
	api.GET("/requests/pending", p.handlers.PendingRequests)
	api.POST("/requests/:id/claim", p.handlers.ClaimRequest)
	api.POST("/requests/:id/deliver", p.handlers.DeliverRequest)
	api.POST("/requests/:id/fail", p.handlers.FailRequest)
	api.GET("/buckets/:id", p.handlers.GetBucket)
	api.GET("/notifications/pending", p.handlers.PendingDeviceNotifs)
	// Sibling endpoint outside the offers group — the script's poller for
	// "has my torrent become an NZB yet?". Same auth path; lives under
	// /api/nzbs to keep the route shape consistent with the rest of /api.
	engine.GET("/api/nzbs/by-info-hash", p.handlers.NzbByInfoHash)

	if p.process == "api" {
		return nil
	}

	// Member surfaces.
	pub := engine.Group("/offers")
	pub.Use(c.Auth.Authenticate()...)
	pub.POST("/request", p.handlers.UserCreateRequest)
	pub.GET("", p.handlers.OffersPage)
	pub.GET("/search", p.handlers.SearchPage)
	pub.GET("/community", p.handlers.CommunityPage)

	// Tracker catalog + offers oversight — mod or above.
	adm := engine.Group("/admin")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("/trackers", p.handlers.AdminTrackersPage)
	adm.POST("/trackers/save", p.handlers.AdminTrackerSave)
	adm.POST("/trackers/:id/delete", p.handlers.AdminTrackerDelete)
	adm.GET("/offers", p.handlers.AdminOffersPage)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.sweeper != nil {
		p.sweeper.start(ctx)
		p.pruner.start(ctx)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
