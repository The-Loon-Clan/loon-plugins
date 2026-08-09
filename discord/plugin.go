// Package discord is the Discord bridge: the bot, the account-link card
// users see on /profile, and the bot's own section of /admin/settings.
//
// Two legs (Metadata.Processes = ["web","worker"]). The WORKER runs the bot —
// account linking, role sync, invite handling, and the chat relay that
// publishes Discord messages into the host-owned chat hub for the chat
// plugin's SSE clients. The WEB leg registers the /profile card (a loon
// SlotUserWidget), the admin settings view, and the unlink route, and never
// touches the gateway: a second connection from the web process would double
// every event.
//
// The bot reaches the host through Deps seams (link storage, user reads,
// typed settings, invite minting) plus the pluginapi.ChatHub contract; it
// publishes pluginapi.ReleaseNotifier so the host's agent handler can push
// completion pings without importing this package.
package discord

import (
	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("discord", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	process string
	bot     *DiscordBotService
	hub     pluginapi.ChatHub
	errs    core.ErrorReporter
	core    *core.Core
	display pluginapi.GroupDisplay
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "discord",
		Version:     "1.1.0",
		Description: "Discord bridge — the bot (worker) plus the account-link card on /profile (web).",
		// The bot belongs on the worker, but the link card and its unlink are
		// UI: they were the host's only because a worker-only plugin had
		// nowhere to put them. The web leg registers the view; the worker leg
		// runs the bot. ("all" runs both — boot skips the filter.)
		Processes: []string{"web", "worker"},
	}
}

// core and display back the two capability consumers on different legs: the
// bot's chat/role sync on worker, the settings form on web. Resolved in Start
// rather than Provision because loon provisions in topo order and "discord"
// sorts before "ranks", so the registry entry does not exist yet — while every
// Provision is guaranteed to run before any Start. A Requires edge would also
// order it, but would make ranks mandatory for Discord to boot at all, and a
// missing badge is cosmetic.
func (p *Plugin) Provision(c *core.Core) error {
	// Deps first, before c is touched: the misconfiguration this reports is
	// "SetDeps was never called", and a nil-deref on the way to saying so
	// would bury it. TestProvision_NilDeps_FailsFast passes a nil Core.
	if deps == nil {
		return fmt.Errorf("discord: SetDeps was not called before core.Boot — wire it in the host's composition root")
	}
	// Every field is validated up front, not per-leg, even though the web leg
	// only needs Links/Users/Settings/Viewer. The host stages them all in one
	// place, so a missing field is a wiring mistake whichever process notices
	// it — and finding out at boot beats finding out when the bot fails to
	// connect. (The host used to skip NewHub here and a nil hub only surfaced
	// as a panic inside Publish.)
	if deps.Links == nil || deps.Users == nil || deps.Settings == nil || deps.NewHub == nil || deps.Viewer == nil || deps.CSRFToken == nil {
		return fmt.Errorf("discord: Deps missing a required field (Links/Users/Settings/NewHub/Viewer/CSRFToken)")
	}
	p.process = c.Process
	p.core = c

	// Web leg: the /profile card + its unlink. No bot here — a second
	// gateway connection from the web process would double every event.
	if p.process == "web" || p.process == "all" {
		p.errs = c.Errors
		if err := c.RegisterView(core.View{
			Slug:   "discord-link",
			Title:  "Discord",
			Slot:   core.SlotUserWidget,
			Render: p.renderCard,
		}); err != nil {
			return fmt.Errorf("discord: register view: %w", err)
		}
		// The bot's own config section on /admin/settings. The host mounts the
		// action at POST /admin/settings/discord/save — the url the fragment's
		// form hardcodes, per the SlotAdminSettings contract.
		if err := c.RegisterView(core.View{
			Slug:    "discord",
			Title:   "Discord Bot",
			Slot:    core.SlotAdminSettings,
			MinRole: core.RoleAdmin,
			Render:  p.renderSettings,
			Actions: map[string]func(*gin.Context) (template.HTML, error){
				"save": p.saveSettings,
			},
		}); err != nil {
			return fmt.Errorf("discord: register settings view: %w", err)
		}
		// Same URL the host served, so no bookmark or muscle memory breaks.
		if engine := c.Router.Engine(); engine != nil {
			g := engine.Group("/profile")
			g.Use(c.Auth.Authenticate()...)
			g.POST("/discord-unlink", p.unlink)
		}
	}
	if p.process == "web" {
		return nil // web-only process: no bot, no chat hub
	}

	// Worker leg: the bot itself, publishing through its own hub instance;
	// the web process's chat plugin subscribes via Redis pub/sub.
	p.hub = deps.NewHub()
	p.bot = NewDiscordBotService(deps.Links, deps.Users, deps.Settings, deps.BaseURL)
	p.bot.SetChatHub(p.hub)
	p.bot.SetCreateInvite(deps.CreateInvite)

	// Published for the agent handler's completion pings in single-process
	// mode (web looks this up after Boot). Registered under the pluginapi
	// contract rather than a bare "discord.bot" string asserted to a concrete
	// type: the consumer is the host, which cannot import this package.
	if err := c.Register(pluginapi.ReleaseNotifierName, p.bot); err != nil {
		return err
	}
	// Same bot, second contract: short operational digests (curation runs,
	// review criticals) to the ops channel. Consumers nil-degrade identically.
	return c.Register(pluginapi.OpsNotifierName, p.bot)
}

// Start/Stop are no-ops on the web leg: it has views and a route, not a bot.
// Nil-checked rather than gated on p.process so the two cannot drift apart.

func (p *Plugin) Start(ctx context.Context) error {
	if p.core != nil {
		if gd, ok := pluginapi.LookupGroupDisplay(p.core); ok {
			p.display = gd
			if p.bot != nil {
				p.bot.SetGroupDisplay(gd)
			}
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
