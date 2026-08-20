package uploads

import (
	"context"
	"fmt"
	"net/http"

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
		Flavours:  []string{core.FlavourAny},
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
	// Authenticate, not RequireUser. RequireUser answers an anonymous request
	// with 401, which is right for an admin API and wrong for a page a member
	// reaches from a nav link: a lapsed session should land on the login form,
	// not a bare status code. Authenticate is the site's own access policy —
	// what /inbox, /lists and /chat use — and the viewer gate below finishes
	// the job by redirecting when there is nobody signed in.
	//
	// The host page this replaced returned 302 here. The first deploy of this
	// plugin returned 401, which is how the difference was found: measured
	// against the live site rather than reasoned about.
	g.Use(c.Auth.Authenticate()...)
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
		// The real gate for anonymous visitors on a public-mode site, since
		// Authenticate lets them through by design. Abort as well as redirect:
		// without it the handler keeps running and renders a page nobody is
		// signed in for.
		c.Redirect(http.StatusFound, "/login")
		c.Abort()
		return nil
	}
	return v
}
