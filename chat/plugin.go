package chat

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("chat", func() core.Plugin { return &Plugin{} })
}

// Plugin owns the /chat surface. The hub it reads from belongs to the host —
// see the package comment — so there is no background goroutine here any
// more; Start is a no-op and the host runs the Redis subscriber.
type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "chat",
		Version:     "1.1.0",
		Description: "Site shoutbox — Discord/IRC chat bridge with SSE live updates and webhook send-back.",
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if c.Process != "web" && c.Process != "all" {
		return nil
	}
	if !deps.ok() {
		return fmt.Errorf("chat: SetDeps was not called with a full Deps before core.Boot")
	}
	p.handlers = NewHandlers().WithCore(c)
	if err := declareEvents(c); err != nil {
		return err
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("chat: Core.Router.Engine() is nil")
	}
	// The page is a view, not a route here: the host mounts SlotSitePage
	// at /p/<slug> and, for a page that predates the slot, at its original
	// url too (the host's alias map). /chat has been /chat for its whole
	// life; moving it to tidy our own code would be a cost paid by users.
	//
	// MinRole is the zero value (RoleUser) with Public false, so the host
	// requires a logged-in account before rendering — the same gate
	// c.Auth.Authenticate() gave the old route.
	if err := c.RegisterView(core.View{
		Slug:   "chat",
		Title:  "Chat",
		Slot:   core.SlotSitePage,
		Render: p.renderPage,
		Nav:    core.NavHint{Menu: core.NavHidden}, // the navbar already links /chat
	}); err != nil {
		return fmt.Errorf("chat: register view: %w", err)
	}

	g := engine.Group("/")
	g.Use(c.Auth.Authenticate()...)
	g.GET("/api/chat/recent", p.handlers.Recent)
	g.GET("/api/chat/stream", p.handlers.Stream)
	g.GET("/api/chat/online", p.handlers.Online)
	g.POST("/api/chat/send", p.handlers.Send)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
