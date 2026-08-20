package tickets

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("tickets", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "tickets",
		Version:     "1.0.0",
		Description: "Support tickets — submission, replies, admin triage.",
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// One check for every seam a render cannot proceed without. Notifications
	// and role chrome are excluded on purpose: each degrades a feature rather
	// than the page, so a host without them still has working support tickets.
	if !deps.ready() {
		return fmt.Errorf("tickets: SetDeps not called, or missing RenderPage/RenderEditor/Markdown/Viewer/PageOffset/RenderPagination — wire it in main() before core.Boot")
	}
	// Templates are parsed here, not at package init: the markdown helper is a
	// Deps function and Deps does not exist until now.
	parseTemplates()
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("tickets: Core.Storage.DB() is nil")
	}
	p.handlers = &Handlers{deps: *deps, store: NewPGStore(db), errs: c.Errors, core: c}

	// Announce tickets. Opening one is not countable -- nobody should be
	// rewarded for needing help -- but a STAFF reply is a real contribution.
	if err := declareEvents(c); err != nil {
		return err
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("tickets: Core.Router.Engine() is nil")
	}

	g := engine.Group("/support")
	g.Use(c.Auth.Authenticate()...)
	g.GET("", p.handlers.SupportPage)
	g.POST("", p.handlers.SubmitTicket)
	g.GET("/:id", p.handlers.TicketDetail)
	g.POST("/:id/reply", p.handlers.ReplyTicket)

	adm := engine.Group("/admin/tickets")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.Tickets)
	adm.POST("/:id/update", p.handlers.UpdateTicket)
	adm.POST("/:id/delete", p.handlers.DeleteTicket)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
