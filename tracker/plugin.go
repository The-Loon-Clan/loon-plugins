// Package tracker is a private BitTorrent tracker: the HTTP announce/scrape
// endpoints a client talks to, the .torrent download that bakes in a member's
// passkey, and the member and admin pages around them.
//
// Lifted out of the host, where it was ~1,800 lines across web/handlers,
// pkg/tracker and the storage layer. The protocol code (bencode, peer store,
// announce parsing) came across near-verbatim; what changed is where the three
// host couplings go — see migrations/001_init.sql.
package tracker

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("tracker", func() core.Plugin { return &Plugin{} })
}

// Config is plugins.tracker.* in the host's config.
type Config struct {
	// SiteURL is the absolute base (scheme + host) the announce URL is built
	// from and baked into every downloaded .torrent.
	//
	// Required, and there is no sensible default. A guess here does not fail
	// loudly — it produces .torrent files that point somewhere unable to answer,
	// and the member finds out when their client reports the tracker as dead.
	SiteURL string `json:"site_url"`
}

type Plugin struct {
	core  *core.Core
	cfg   Config
	store Store
	peers *PeerStore
	h     *Handlers
	tmpl  *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "tracker",
		Version:     "0.1.0",
		Description: "Private BitTorrent tracker — announce/scrape, passkeys, per-member ratio accounting.",
		Migrations:  migrations,
		// web AND api, because the host registered announce/scrape on both and a
		// torrent client hits whichever hostname is in its .torrent. Dropping one
		// would break every client pointed at it, with nothing on the site saying
		// so — the failure surfaces only in the client.
		Processes: []string{"web", "api"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if err := c.Config.PluginInto("tracker", &p.cfg); err != nil {
		return fmt.Errorf("tracker: config: %w", err)
	}

	// REDIS IS REQUIRED, AND ITS ABSENCE IS AN IDLE RATHER THAN A BOOT FAILURE.
	//
	// The peer store is the live swarm — who is on which torrent right now — and
	// it has no Postgres fallback because it is a few thousand short-lived keys
	// rewritten every announce interval, which is what Redis is for and what a
	// table is not. core.Redis is OPTIONAL infrastructure by contract, so a host
	// may legitimately have none.
	//
	// Idling rather than failing Provision: a site without Redis has not
	// misconfigured the tracker, it simply cannot run one, and taking the whole
	// site down over an opt-in feature would be the wrong trade. But it is LOUD,
	// because a tracker that quietly is not there is indistinguishable from one
	// that is broken — and the member-visible symptom is identical.
	if c.Redis == nil || c.Redis.Client() == nil {
		log.Printf("tracker: no Redis configured — the tracker is IDLE (announce, scrape and its pages are not registered). " +
			"The peer store needs Redis and has no durable fallback; set the host's Redis config to enable it.")
		return nil
	}
	if p.cfg.SiteURL == "" {
		// Refused rather than defaulted, for the reason on the field: a wrong
		// SiteURL produces .torrent files that point nowhere, and nothing on this
		// side ever notices.
		return fmt.Errorf("tracker: plugins.tracker.site_url is required — it is baked into every .torrent's announce URL")
	}

	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("tracker: Core.Storage.SchemaDB is nil")
	}
	p.store = NewPGStore(db)
	p.peers = NewPeerStore(c.Redis.Client())
	p.h = NewHandlers(p.store, p.peers, NewGate(c), c.Auth, p.cfg.SiteURL)

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("tracker: Core.Router.Engine() is nil")
	}

	// ── The BitTorrent endpoints ────────────────────────────────────────────
	//
	// Off the raw engine with NO session middleware, and that is the whole point:
	// the caller is a torrent client. It has no cookie, cannot follow a login
	// redirect, and would parse a login page as a bencoded response. The passkey
	// in the path is the entire authentication story, and the page-policy table
	// declares these Public for exactly this reason.
	engine.GET("/api/tracker/announce/:passkey", p.h.Announce)
	engine.GET("/api/tracker/scrape/:passkey", p.h.Scrape)

	// The api process serves only the machine-facing surface — no templates, no
	// session — so it stops here.
	if c.Process == "api" {
		return nil
	}

	// Templates and the host render seams are needed by the member pages below
	// AND by the admin view, so they are established before either is mounted.
	// Refused rather than deferred: an unwired seam surfaces as a 500 on a page
	// a member opened, which is a worse place to learn about it than boot.
	if !depsReady() {
		return fmt.Errorf("tracker: SetDeps was not called with a full Deps " +
			"(RenderPage, CSRFToken, RelativeTime) before core.Boot")
	}
	if err := p.parseTemplates(); err != nil {
		return fmt.Errorf("tracker: parsing templates: %w", err)
	}
	p.h.SetTemplates(p.tmpl)

	// ── Member pages ────────────────────────────────────────────────────────
	//
	// Two gates, in order: the host's own auth chain first (RequireUser), then the
	// tracker's entitlement. Order matters — checking the entitlement first would
	// mean resolving one for an anonymous request, and the honest answer to "may
	// nobody use the tracker" is a login prompt rather than a denial.
	authed := engine.Group("/tracker")
	authed.Use(c.Auth.RequireUser(core.RoleUser)...)
	authed.Use(p.requireEntitled)
	authed.GET("", p.h.IndexPage)
	authed.GET("/my", p.h.MyStatsPage)
	authed.GET("/download/:info_hash", p.h.Download)
	authed.POST("/passkey/rotate", p.h.RotatePasskey)

	return p.registerViews(c)
}

// requireEntitled is the tracker's own gate for browser traffic.
//
// Sends a member who lacks the entitlement to "/" rather than to /login, because
// they ARE logged in — a login prompt would be a lie, and they would loop. The
// host's original did the same thing for the same reason.
func (p *Plugin) requireEntitled(gc *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil || !p.h.gate.Entitled(gc.Request.Context(), u.ID) {
		gc.Redirect(302, "/")
		gc.Abort()
		return
	}
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)
