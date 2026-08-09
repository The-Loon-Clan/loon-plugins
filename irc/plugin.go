// Package irc is the IRC bridge: the bot, and the account-link card users see
// on /profile.
//
// Two legs (Metadata.Processes = ["web","worker"]). The WORKER runs the bot —
// chat relay, account linking, DM delivery — and is inert until an admin sets
// irc_server / irc_channel and flips irc_enabled. The WEB leg registers the
// /profile card (a loon SlotUserWidget) and its unlink route, and never opens a
// connection: a second client from the web process would join the channel twice.
//
// Companion to the discord plugin, sharing the same Redis-backed chat hub
// behind pluginapi.ChatHub. The flow is NOT a full mesh: IRC publishes to the
// hub and subscribes back from it, so IRC ↔ site works and discord → IRC
// works — but nothing forwards IRC messages to Discord, because the discord
// bot does not subscribe and only a site user's Send hits the webhook.
package irc

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("irc", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	process string
	bot     *IRCBotService
	hub     pluginapi.ChatHub
	errs    core.ErrorReporter
	// core is kept so Start can resolve the badge capability. It cannot be
	// resolved in Provision: loon provisions plugins in topo order and "irc"
	// sorts before "ranks", so the registry entry does not exist yet. Every
	// Provision runs before any Start, which is what makes Start the right
	// phase for an OPTIONAL capability — a Requires edge would work too but
	// would make ranks mandatory for chat to boot at all.
	core *core.Core
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "irc",
		Version:     "1.1.0",
		Description: "IRC bridge — the bot (worker) plus the account-link card on /profile (web).",
		// Same split as discord: the bot is background work, the link card is
		// UI. It sat in the host only because a worker-only plugin had nowhere
		// to put a page fragment.
		Processes: []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// Deps before the Core is touched: this reports "SetDeps was never called",
	// and a nil-deref on the way to saying so would bury it.
	if deps == nil {
		return fmt.Errorf("irc: SetDeps was not called before core.Boot — wire it in the host's composition root")
	}
	// Validated up front, not per-leg: the host stages every field in one
	// place, so a missing one is a wiring mistake whichever process notices.
	// DMs is required because the PM whisper command dereferences it. (The
	// host's Deps also carried a SettingsService that nothing ever read; that
	// dead field is gone.)
	if deps.DMs == nil || deps.Links == nil || deps.Settings == nil || deps.Users == nil || deps.NewHub == nil || deps.Viewer == nil {
		return fmt.Errorf("irc: Deps missing a required field (DMs/Links/Settings/Users/NewHub/Viewer)")
	}
	p.process = c.Process
	p.core = c

	// Web leg: the /profile card + its unlink. No bot — a second IRC
	// connection from the web process would join the channel twice.
	if p.process == "web" || p.process == "all" {
		p.errs = c.Errors
		if err := c.RegisterView(core.View{
			Slug:   "irc-link",
			Title:  "IRC",
			Slot:   core.SlotUserWidget,
			Render: p.renderCard,
		}); err != nil {
			return fmt.Errorf("irc: register view: %w", err)
		}
		// The host's original URL, so nothing user-facing moves.
		if engine := c.Router.Engine(); engine != nil {
			g := engine.Group("/profile")
			g.Use(c.Auth.Authenticate()...)
			g.POST("/irc-unlink", p.unlink)
		}
	}
	if p.process == "web" {
		return nil // web-only process: no bot, no chat hub
	}

	p.hub = deps.NewHub()
	p.bot = NewIRCBotService(deps.DMs, deps.Links, deps.Settings, deps.Users, deps.BaseURL)
	p.bot.SetChatHub(p.hub)
	p.bot.SetCreateInvite(deps.CreateInvite)
	return nil
}

// Start/Stop are no-ops on the web leg, which has a view and a route but no
// bot. Nil-checked rather than gated on p.process so the two cannot drift.

func (p *Plugin) Start(ctx context.Context) error {
	// Badge capability resolved here rather than in Provision — see the core
	// field. Absent is fine and expected on a host without the ranks plugin.
	if p.bot != nil && p.core != nil {
		if d, ok := pluginapi.LookupGroupDisplay(p.core); ok {
			p.bot.SetGroupDisplay(d)
		}
	}
	if p.hub != nil {
		p.hub.Start()
	}
	if p.bot != nil {
		p.bot.Start()
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error {
	if p.bot != nil {
		p.bot.Shutdown()
	}
	return nil
}
