package uploads

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("uploads", func() core.Plugin { return &Plugin{} })
}

// Plugin serves the member's own upload management, and — as later slices land
// — the upload flows themselves.
type Plugin struct {
	core *core.Core
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "uploads",
		Version:     "0.1.0",
		Description: "Member upload domain: the owner's management page today, the upload and batch flows as later slices land.",
		// Web only. Nothing here is scheduled — ingest happens on a request,
		// and the jobs that groom uploaded rows afterwards belong to the
		// catalog and curation domains rather than to this one.
		Processes: []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if !deps.ok() {
		return fmt.Errorf("uploads: SetDeps was not called with a full Deps before core.Boot")
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("uploads: Core.Router.Engine() is nil")
	}

	// Mounted where the host's page already lived, not at a new path. A member
	// with this tab bookmarked, or a forum post linking it, must not discover
	// the lift — the point of moving code is that nobody outside can tell.
	g := engine.Group("/account-settings/uploads")
	g.Use(c.Auth.RequireUser(core.RoleUser)...)
	g.GET("", p.index)
	g.POST("/bulk", p.bulkAction)
	g.POST("/torrent-visibility", p.torrentVisibility)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)

// viewer resolves the member or finishes the request. Returns nil when it has
// already answered, so callers return immediately — the polarity that the
// roadmap lift got backwards once and rendered pages to signed-out visitors.
func (p *Plugin) viewer(c *gin.Context) *Viewer {
	v := deps.Viewer(c)
	if v == nil {
		// RequireUser above should make this unreachable. Answering anyway
		// rather than dereferencing nil: an auth adapter that changes shape
		// should cost a redirect, not a panic on the request path.
		c.Redirect(302, "/login")
		return nil
	}
	return v
}
