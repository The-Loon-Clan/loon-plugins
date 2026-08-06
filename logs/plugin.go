package logs

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("logs", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin wrapper. Dual-process: the web leg serves
// the /admin/logs search UI + JSON API; the worker leg owns the daily
// Error Log Cleanup loop. Start gates on the process kind captured at
// Provision so a split-mode web process never races the worker's job.
type Plugin struct {
	handlers *Handlers
	process  string
	cleaner  *cleaner
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "logs",
		Version:     "1.1.0",
		Description: "Error-log search (ES-style query DSL, facets, histogram, live tail) over the host error-log sink, plus the daily cleanup job.",
		// web serves the search UI; worker owns the cleanup loop. "all"
		// is a run-mode, not a declarable kind — boot skips the Processes
		// filter entirely when Process=="all", and the Provision/Start
		// gates already handle it, so listing it would be inert.
		Processes: []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.process = c.Process

	// Worker leg: build the cleanup loop (started in Start).
	if p.process == "worker" || p.process == "all" {
		if !jobDeps.ok() {
			return fmt.Errorf("logs: SetJobDeps was not called with a full JobDeps before core.Boot")
		}
		p.cleaner = newCleaner(*jobDeps)
	}
	if p.process == "worker" {
		return nil // headless worker: no routes
	}

	// Web / all legs serve the admin search surface.
	if !deps.ok() {
		return fmt.Errorf("logs: SetDeps was not called with a full Deps before core.Boot")
	}
	p.handlers = &Handlers{}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("logs: Core.Router.Engine() is nil")
	}
	// The PAGE is a view: the host mounts SlotAdminPage at /admin/p/<slug>
	// inside the site chrome, which is what lets the markup live here rather
	// than in the host's template directory.
	if err := c.RegisterView(core.View{
		Slug:        "logs",
		Title:       "Log Search",
		Slot:        core.SlotAdminPage,
		MinRole:     core.RoleAdmin,
		Description: "Search the persistent error sink: query DSL, facets, histogram, live tail.",
		Nav:         core.NavHint{Group: "Operations"},
		Render:      p.renderPage,
	}); err != nil {
		return fmt.Errorf("logs: register page view: %w", err)
	}

	// The two JSON endpoints stay where they were. They are not pages, so
	// nothing is gained by moving them under /admin/p, and the live-tail
	// client that calls them ships in this plugin's own fragment — one URL
	// changing is enough.
	//
	// Error logs can carry internal detail (paths, SQL fragments), so
	// gate at admin — matching the legacy /admin/errors access level,
	// stricter than the RoleMod plugins.
	adm := engine.Group("/admin/logs")
	adm.Use(c.Auth.RequireUser(core.RoleAdmin)...)
	adm.GET("/search.json", p.handlers.LogsSearch)
	adm.POST("/:id/archive", p.handlers.ArchiveLog)
	// /admin/logs was this page's address for its whole life. Operators have
	// it bookmarked; send them on rather than 404.
	adm.GET("", func(gc *gin.Context) {
		gc.Redirect(http.StatusMovedPermanently, "/admin/p/logs?"+gc.Request.URL.RawQuery)
	})
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.cleaner != nil {
		p.cleaner.start(ctx)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
