package lists

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("lists", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "lists",
		Version:     "1.1.0",
		Description: "Watchlists / collections — personal NZB lists, sharing, follows, bulk download, discovery grid.",
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if c.Process != "web" && c.Process != "all" {
		return nil
	}
	if !deps.ok() {
		return fmt.Errorf("lists: SetDeps was not called with a full Deps before core.Boot")
	}
	p.handlers = &Handlers{}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("lists: Core.Router.Engine() is nil")
	}
	g := engine.Group("/")
	g.Use(c.Auth.Authenticate()...)
	g.GET("/lists", p.handlers.UserLists)
	g.POST("/lists", p.handlers.CreateList)
	g.POST("/lists/:id/delete", p.handlers.DeleteList)
	g.POST("/lists/:id/visibility", p.handlers.SetListPublic)
	g.GET("/lists/:id", p.handlers.ViewList)
	g.GET("/lists/:id/download-all", p.handlers.DownloadAllList)
	g.POST("/lists/:id/follow", p.handlers.FollowList)
	g.POST("/lists/:id/unfollow", p.handlers.UnfollowList)
	g.POST("/lists/:id/copy", p.handlers.CopyList)
	g.POST("/lists/add", p.handlers.AddToList)
	g.POST("/lists/remove", p.handlers.RemoveFromList)
	g.GET("/release/:id/lists", p.handlers.ReleaseLists)
	g.GET("/community/watchlists", p.handlers.WatchLists)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
