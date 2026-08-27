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

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("agent", func() core.Plugin { return &Plugin{} })
}

// Plugin is the core.Plugin lifecycle wrapper.
type Plugin struct {
	// core is kept for the member page's CSRF mint; set in Provision.
	core *core.Core
}

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
	// survives a deploy unnoticed. Only the ORIGINAL five are required — the
	// member page's seams (AgentsDetail, the self-service trio, the opt-in
	// pair) are optional by contract: each nil degrades one page feature, so
	// a host adopts them at its own pace without a boot barrier.
	if deps == nil || deps.Viewer == nil || deps.AgentsForUser == nil ||
		deps.ActiveTask == nil || deps.CountAgents == nil || deps.MaxConcurrent == nil {
		return fmt.Errorf("agent: SetDeps was not called with a full Deps before core.Boot")
	}
	p.core = c
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
	if err := p.registerMemberPage(c); err != nil {
		return fmt.Errorf("agent: register member page: %w", err)
	}
	// Optional like the member page's seams: no AllAgents, no roster page.
	if err := p.registerAdminPage(c); err != nil {
		return fmt.Errorf("agent: register admin roster page: %w", err)
	}
	if err := p.registerGroupsPage(c); err != nil {
		return fmt.Errorf("agent: register agent-groups page: %w", err)
	}
	// Once, at Provision — the host serves it hashed and cached. No-ops on a
	// host with no stylesheet sink, where the page draws unstyled: visible
	// rather than silent, the right failure for a missing seam.
	pluginapi.RegisterStylesheet(c, "agent", agentCSS)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
