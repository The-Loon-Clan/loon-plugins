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
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("tracker", func() core.Plugin { return &Plugin{} })
}

// Config is plugins.tracker.* in the host's config.
type Config struct {
	// Enabled switches the whole plugin on. Default FALSE, which is unusual
	// among these plugins and deliberate here.
	//
	// A tracker is not a feature a site accidentally wants. It publishes
	// announce endpoints, mints passkeys, and starts keeping ratio accounting
	// the moment it is reachable — so the safe default for a host that merely
	// has the code compiled in is off. Everything else here (wiki, messages,
	// forum) is inert until a member visits it; this is not.
	//
	// Defaulting false also means the required site_url below is only required
	// when it will actually be used, so importing the plugin cannot break a
	// host's boot.
	Enabled bool `json:"enabled"`
	// Cheat is the detection policy (cheat.go). Off by default like the rest of
	// this file's switches that can end up accusing somebody.
	Cheat CheatPolicy `json:"cheat"`
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

	// cheat is the SAME store as p.store, held concretely.
	//
	// Detection is separate machinery — a host can run the tracker without it —
	// so its reads are on *PGStore rather than in the Store interface every
	// implementation would then have to satisfy. MemStore deliberately does not
	// have them, and the test tracker is none the worse for it.
	cheat    *PGStore
	cheatJob core.Job
	ctx      context.Context
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

	// OFF unless switched on. Checked FIRST, ahead of the Redis and site_url
	// checks below, so a host that compiles the plugin in without configuring it
	// boots cleanly instead of being refused for a site_url it has no use for.
	//
	// Logged rather than silent, for the same reason the Redis-idle path is: a
	// tracker that is not there is indistinguishable from one that is broken, and
	// the member-visible symptom of both is identical. An operator who expected
	// it on should be able to find out why from the boot log.
	//
	// Note that migrations run in Boot step 1, BEFORE any Provision, so a
	// disabled tracker still gets its (empty) schema. That is deliberate: it
	// makes enabling later a config change and a restart, with no migration step
	// and no first-run schema surprise.
	if !p.cfg.Enabled {
		log.Printf("tracker: plugins.tracker.enabled is false — the tracker is OFF " +
			"(no announce, no scrape, no member pages, no admin view). " +
			"Set plugins.tracker.enabled: true and plugins.tracker.site_url to enable it.")
		return nil
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
	pg := NewPGStore(db)
	p.store, p.cheat = pg, pg
	p.peers = NewPeerStore(c.Redis.Client())
	p.h = NewHandlers(p.store, p.peers, NewGate(c), c.Auth, p.cfg.SiteURL)

	// Torrent facts for the promotion page (pluginapi.TorrentInfoFunc):
	// name and size by hash, so a magic cast can show and price what it is
	// enchanting without reading this schema.
	if err := c.Register(pluginapi.TorrentInfoName,
		pluginapi.TorrentInfoFunc(func(ctx context.Context, hash string) (string, int64, bool, error) {
			t, err := pg.Torrent(ctx, hash)
			if err != nil || t == nil {
				return "", 0, false, nil
			}
			return t.Name, t.Size, true, nil
		})); err != nil {
		return fmt.Errorf("tracker: register torrentinfo: %w", err)
	}

	// Transfer credit for other plugins to sell (pluginapi.TrackerCredit —
	// the points store's GB items). Registered only on a RUNNING tracker:
	// selling upload credit on a site whose tracker idles would take points
	// for a number nothing displays.
	if err := c.Register(pluginapi.TrackerCreditName, pluginapi.TrackerCredit(pg)); err != nil {
		return fmt.Errorf("tracker: register credit: %w", err)
	}

	// Cheat detection (cheat_job.go). Registered even when the rules are off:
	// the sampling still runs so that switching detection on starts working at
	// the next sweep rather than the one after, and an operator can see the job
	// exists.
	p.registerCheatJob(c)

	// Placeable widgets (widgets.go). Registered HERE rather than at the top of
	// Provision because they read p.store, and because everything above this
	// point is a reason the tracker is not running — a widget offered by a
	// tracker that idled for want of Redis is a widget that can only render
	// nothing.
	p.registerWidgets(c)

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

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	// The user-multiplier system, folded into Credit best-of with the
	// installed multiplier (perks' slot). One resolve per dimension per
	// announce; sources that error are no-opinion, so an announce never
	// fails over an economy question. The resolver scans the registry per
	// call, so sources registered after this still count.
	if p.core != nil {
		reg := p.core
		setPromo(func(ctx context.Context, userID int64, infoHash string) (float64, float64) {
			mc := pluginapi.MultiplierContext{UserID: userID, InfoHash: infoHash}
			return pluginapi.ResolveMultiplier(ctx, reg, pluginapi.MultUpload, mc),
				pluginapi.ResolveMultiplier(ctx, reg, pluginapi.MultDownload, mc)
		})
	}
	// No job means the tracker is off or the host wired no scheduler; either
	// way there is nothing to loop.
	if p.cheatJob == nil || p.core == nil || p.core.Scheduler == nil {
		return nil
	}
	// Two minutes after boot, not immediately: the announce path should be up
	// and taking readings before its accounting is judged, and a restart must
	// not be the thing that decides somebody cheated.
	p.core.Scheduler.RunLoop(ctx, p.cheatJob, 2*time.Minute, CheatSweepInterval, p.runCheatSweep)
	return nil
}
func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
