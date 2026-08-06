// Package agent owns the fleet agent's read-only surfaces: the profile fleet
// card a member sees on their own page, and the dispatch overview on admin
// settings.
//
// It is a surfaces-only plugin by design. The agent TABLES, the /api/agent/*
// runtime that agents poll, and the lock-expiry job all remain with the host,
// because agents write to those rows continuously and a half-moved runtime is
// a worse place to stop than either end. Metadata.Processes grows from web to
// web/worker/api if and when those move.
package agent

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("agent", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper.
type Plugin struct{}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "agent",
		Version:     "0.2.0",
		Description: "Fleet agent surfaces: the profile fleet card and the admin dispatch overview.",
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if c.Process != "web" && c.Process != "all" {
		return nil
	}
	// Fail loud rather than render an empty card forever: a missed SetDeps is a
	// wiring bug, and a silently blank widget is exactly the kind of thing that
	// survives a deploy unnoticed.
	if deps == nil || deps.Viewer == nil || deps.AgentsForUser == nil ||
		deps.ActiveTask == nil || deps.CountAgents == nil || deps.MaxConcurrent == nil {
		return fmt.Errorf("agent: SetDeps was not called with a full Deps before core.Boot")
	}
	if err := c.RegisterView(core.View{
		Slug:   "agent-fleet",
		Title:  "Agent Fleet",
		Slot:   core.SlotUserWidget,
		Render: p.renderCard,
	}); err != nil {
		return fmt.Errorf("agent: register fleet card view: %w", err)
	}
	// Host gates /admin/settings on admin already; MinRole is belt-and-braces.
	if err := c.RegisterView(core.View{
		Slug:    "agent-dispatch",
		Title:   "Agent Dispatch",
		Slot:    core.SlotAdminSettings,
		MinRole: core.RoleAdmin,
		Render:  p.renderDispatchPanel,
	}); err != nil {
		return fmt.Errorf("agent: register dispatch panel view: %w", err)
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
