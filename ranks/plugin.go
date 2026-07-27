package ranks

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Plugin-owned schema (ENTITLEMENTS.md Stage 2): loon applies these into a
// Postgres schema named after the plugin, so the groups model is this plugin's
// property rather than a slice of the host's public schema. As of Stage 3.4 it
// is the ONLY copy — every host reader moved to core entitlements (the DM gate,
// the download/API quotas) or to this plugin's GroupDisplay / GroupAudit
// capabilities, and the legacy user_ranks mirror is no longer written.
//
//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("ranks", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	process string
	expiry  *rankExpiry
	store   Store
	ents    *entSync
	log     *slog.Logger
	errs    core.ErrorReporter
	auth    core.AuthService
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "ranks",
		Version:     "1.0.0",
		Description: "Paid ranks — points purchase flow, admin catalog management, and the hourly rank-expiry job.",
		// web serves the buy-rank flow + admin catalog; worker runs the
		// Rank Expiry loop. ("all" mode runs both — boot skips the filter.)
		Processes:  []string{"web", "worker"},
		Migrations: migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.process = c.Process
	p.log = c.LoggerFor("ranks")
	p.errs = c.Errors
	p.auth = c.Auth

	// The plugin owns its schema now, so it builds its own store rather than
	// receiving a host repository.
	if c.Storage == nil || c.Storage.DB() == nil {
		return fmt.Errorf("ranks: Core.Storage has no database")
	}
	p.store = NewPGStore(c.Storage.SchemaDB("ranks"))
	// Membership changes grant into core from here on (Stage 2.3). No longer
	// write-only: the DM gate and the download/API quotas resolve from these
	// rows, so a missed grant is now user-visible rather than merely untidy.
	p.ents = &entSync{ents: c.Entitlements, store: p.store}

	// Worker leg: the hourly rank-expiry loop (started in Start). The
	// scheduler comes off the Core like every other capability, so there is no
	// SetJobDeps gate any more — nothing has to be handed in before Boot.
	if p.process == "worker" || p.process == "all" {
		p.expiry = newRankExpiry(p.store, p.ents, c.Scheduler)
	}
	// Capabilities publish on EVERY leg, including the headless worker. They
	// are process-local registry entries with no router involvement, and the
	// consumers are not all web: the discord and irc bots run on the worker and
	// read badges to colour chat, so a web-only publish left them looking at a
	// capability that did not exist in their process.
	//
	// Publish the rank-granting capability so other plugins (the store,
	// a donation-reward orchestrator) can award ranks through the
	// pluginapi.RankGranter contract without importing this package.
	if err := c.Register(pluginapi.RankGranterName,
		&rankGranter{store: p.store, ents: p.ents, errs: p.errs}); err != nil {
		return fmt.Errorf("ranks: publish RankGranter: %w", err)
	}

	// The display half. Stage 2.4 built groupDisplay and its contract but never
	// put it on the registry, so LookupGroupDisplay would have returned
	// not-found and every Stage 3.4 consumer would have silently degraded to no
	// badge — the failure mode the contract deliberately makes non-fatal, which
	// is exactly why nothing would have complained.
	if err := c.Register(pluginapi.GroupDisplayName, &groupDisplay{store: p.store}); err != nil {
		return fmt.Errorf("ranks: publish GroupDisplay: %w", err)
	}

	// The audit half, kept a separate capability so a badge consumer cannot
	// also read membership history — see audit.go.
	if err := c.Register(pluginapi.GroupAuditName, &groupAudit{store: p.store}); err != nil {
		return fmt.Errorf("ranks: publish GroupAudit: %w", err)
	}

	if p.process == "worker" {
		return nil // headless worker: no routes, no admin View
	}

	// Web / all legs: the admin catalog. No SetDeps gate — the web leg has no
	// host dependency left to check (Stage 3.2).

	// The catalog is a plugin-owned View now (/admin/p/groups), not a host
	// template: the groups model has kind, parent and visibility, none of which
	// admin_ranks.html could express. The host mounts the page, its actions and
	// an admin-hub card from this registration alone.
	//
	// No buy route here either. Ranks are sold as store items: the store
	// deducts the points and calls GrantRank through the capability published
	// above, which is the same flow every other reward type uses.
	if err := c.RegisterView(core.View{
		Slug:        "groups",
		Title:       "Groups & Ranks",
		Slot:        core.SlotAdminPage,
		Description: "Membership tiers, their entitlements, nesting and badges.",
		Render:      p.renderGroups,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"create": p.actionCreate,
			"update": p.actionUpdate,
			"delete": p.actionDelete,
		},
	}); err != nil {
		return fmt.Errorf("ranks: register view: %w", err)
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	// Rebuild the entitlement grants from the memberships that survived. The
	// grants are written outside the membership transaction, so this is where a
	// crash between the two gets repaired; Grant is an idempotent upsert, so a
	// settled system pays only the write.
	if p.ents != nil && p.ents.ents != nil {
		if n, err := p.ents.rebuildAll(ctx); err != nil {
			p.errs.Report(ctx, "ranks/entitlement-rebuild", err)
		} else if n > 0 {
			p.log.Info("entitlement grants rebuilt", "grants", n)
		}
	}
	if p.expiry != nil {
		p.expiry.start(ctx)
	}
	return nil
}
func (p *Plugin) Stop(ctx context.Context) error { return nil }
