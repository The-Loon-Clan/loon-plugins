// Package roadmap is the planning surface — the public /help/roadmap +
// /help/changelog pages (anonymous-friendly even in closed mode), the /flow
// collaborative diagram editor, and the admin roadmap/changelog management.
//
// Lifted from the ameNZB host's in-repo plugin, markup included, and almost
// fully self-contained: the roadmap/changelog tables were already
// plugin-private, and the flow domain moved wholesale (models + Postgres
// store) because the host had no other caller. What remains host-side is
// chrome, the viewer, and two sanitising renderers — see deps.go. The
// dormant WebSocket hub (never routed, per the host's own comment about
// reverse-proxy issues) was left behind, which also removed the plugin's
// only UserRepository dependency.
package roadmap

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("roadmap", func() core.Plugin { return &Plugin{} })
}

// Handlers serves the roadmap/changelog/flow surfaces.
type Handlers struct {
	deps  Deps
	store Store
	flow  FlowStore
	errs  core.ErrorReporter
}

type Plugin struct {
	handlers  *Handlers
	process   string
	snapshots *flowSnapshots
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "roadmap",
		Version:     "1.0.0",
		Description: "Public roadmap + changelog pages, the /flow collaborative planning canvas, and the periodic flow-snapshot job.",
		// web serves the pages + canvas; worker runs the Flow Snapshots loop.
		Processes: []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.process = c.Process

	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("roadmap: Core.Storage.DB() is nil")
	}

	// Worker leg: the periodic flow-snapshot loop (started in Start). The
	// flow store is plugin-owned, so the worker needs no host seams at all.
	if p.process == "worker" || p.process == "all" {
		p.snapshots = newFlowSnapshots(NewPGFlowStore(db))
	}
	if p.process == "worker" {
		return nil // headless worker: no routes
	}

	if !deps.ok() {
		return fmt.Errorf("roadmap: SetDeps was not called with a full Deps before core.Boot — wire the chrome/viewer/renderer seams in the host's composition root")
	}
	if err := parseTemplates(); err != nil {
		return err
	}
	p.handlers = &Handlers{deps: *deps, store: NewPGStore(db), flow: NewPGFlowStore(db), errs: c.Errors}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("roadmap: Core.Router.Engine() is nil")
	}

	// Public even in closed mode — anonymous visitors evaluate the
	// project before signing up; logged-in viewers get their chrome
	// via the optional session load.
	pub := engine.Group("/help")
	pub.Use(c.Auth.Optional()...)
	pub.GET("/roadmap", p.handlers.RoadmapPage)
	pub.GET("/changelog", p.handlers.ChangelogPage)

	// The flow canvas follows the site's default access policy.
	fl := engine.Group("/flow")
	fl.Use(c.Auth.Authenticate()...)
	fl.GET("", p.handlers.Page)
	fl.GET("/data", p.handlers.GraphData)
	fl.GET("/proposals", p.handlers.RecentProposals)
	fl.POST("/proposals/upload", p.handlers.UploadProposalImage)
	fl.GET("/proposals/similar", p.handlers.SimilarProposals)
	fl.GET("/proposals/:id/details", p.handlers.ProposalDetails)
	fl.POST("/node/:id/tag", p.handlers.SetNodeTag)
	fl.POST("/node/:id/status", p.handlers.SetNodeStatus)
	fl.GET("/mockups", p.handlers.MockupsIndex)
	fl.GET("/mockup/:id", p.handlers.MockupDetail)
	fl.POST("/node", p.handlers.CreateNode)
	fl.PATCH("/node/:id", p.handlers.UpdateNode)
	fl.DELETE("/node/:id", p.handlers.DeleteNode)
	fl.POST("/node/:id/promote", p.handlers.PromoteNode)
	fl.POST("/node/:id/propose-change", p.handlers.ProposeChange)
	fl.POST("/node/:id/vote", p.handlers.VoteNode)
	fl.GET("/node/:id/comments", p.handlers.ListNodeComments)
	fl.POST("/node/:id/comment", p.handlers.AddNodeComment)
	fl.GET("/node/:id/history", p.handlers.NodeHistory)
	fl.POST("/edge", p.handlers.CreateEdge)
	fl.DELETE("/edge/:id", p.handlers.DeleteEdge)

	adm := engine.Group("/admin")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("/roadmap", p.handlers.AdminRoadmapPage)
	adm.POST("/roadmap/save", p.handlers.AdminRoadmapSave)
	adm.POST("/roadmap/:id/delete", p.handlers.AdminRoadmapDelete)
	adm.GET("/changelog", p.handlers.AdminChangelogPage)
	adm.POST("/changelog/save", p.handlers.AdminChangelogSave)
	adm.POST("/changelog/:id/delete", p.handlers.AdminChangelogDelete)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.snapshots != nil {
		p.snapshots.start(ctx)
	}
	return nil
}
func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
