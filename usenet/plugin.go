// Package usenet is the delivery-axis plugin: a basic Usenet indexer that crawls
// the last few days of a set of newsgroups, assembles complete article sets into
// downloadable NZB files, and serves search / group-list / download through a
// capability the host's pages consume. It owns the "usenet" Postgres schema and
// groups its jobs — Crawler, Backfill, NZB Builder, Tag Fill, Prune — in one place.
//
// Staging (the transient article-assembly buffer) is pluggable behind the
// stagingStore seam: durable Postgres by default (never-lost, the base site's
// mode), or prod's Redis pipeline lifted verbatim via staging: redis (fast,
// best-effort) when the host has Redis. See USENET-PLUGIN.md and, for the seam,
// USENET-STAGING-MODES.md.
package usenet

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/nntp"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("usenet", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core    *core.Core
	cfg     Config
	ctx     context.Context
	st      Store
	staging stagingStore // transient assembly buffer (pg|redis) — see staging.go
	svc     *service
	tmpl    *template.Template // admin-view fragments (views.go)
	catalog pluginapi.Catalog  // optional — the content-taxonomy plugin (looked up in Start)

	crawlJob    core.Job
	backfillJob core.Job
	buildJob    core.Job
	tagJob      core.Job
	pruneJob    core.Job

	// per-job locks: a manual trigger (admin button / /admin/jobs) must not
	// overlap a scheduled run of the same job — they share one NNTP connection
	// and race on watermarks.
	crawlMu    sync.Mutex
	backfillMu sync.Mutex
	buildMu    sync.Mutex

	// backfillPaused is the hysteresis latch for staging back-pressure; only
	// touched inside runBackfill (serialized by backfillMu).
	backfillPaused bool

	// Shared NNTP connection pool (crawl + backfill). Opened lazily on first
	// use — see pool.go.
	poolMu  sync.Mutex
	pool    *nntp.Pool
	poolKey string
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "usenet",
		Version:     "0.1.0",
		Description: "A basic Usenet indexer: crawls recent posts, builds NZB files, and serves search / groups / download.",
		Processes:   []string{"web", "worker", "api"},
		Migrations:  migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	pg := NewPGStore(c.Storage.SchemaDB("usenet"))
	p.st = pg
	if err := c.Config.PluginInto("usenet", &p.cfg); err != nil {
		return fmt.Errorf("usenet: config: %w", err)
	}
	p.cfg.applyDefaults()
	// Staging backend behind the seam. Limits are read per-call (via effective)
	// so the admin knobs apply live. nzbs writes always go through pg regardless.
	staging, err := newStaging(p.cfg.Staging, pg, c.Redis, func(ctx context.Context) (int, int) {
		e := p.effective(ctx)
		return e.StagingMaxRows, e.StagingPruneHours
	})
	if err != nil {
		return fmt.Errorf("usenet: staging: %w", err)
	}
	p.staging = staging
	p.svc = &service{store: p.st, retentionDays: p.cfg.RetentionDays}

	// Contribute indexer totals to the stats snapshot (collected in the worker
	// process; registering everywhere is harmless).
	if err := pluginapi.RegisterStats(c, statHook{store: p.st}); err != nil {
		return err
	}

	// web/all/api: publish the READ capabilities — the public site pages AND the
	// standalone api process both serve search / browse / Newznab / download.
	if c.Process == "web" || c.Process == "all" || c.Process == "api" {
		if err := c.Register(pluginapi.UsenetIndexName, p.svc); err != nil {
			return err
		}
		if err := c.Register(pluginapi.UsenetNewznabName, p.svc); err != nil {
			return err
		}
	}

	// web/all only: the admin capability + the plugin-owned admin views (setup
	// wizard + crawl status) the host wraps in its own chrome. The api process
	// has no view system or admin surface, so these stay out of it.
	if c.Process == "web" || c.Process == "all" {
		if err := c.Register(pluginapi.UsenetAdminName, p.svc); err != nil {
			return err
		}
		if err := p.registerViews(c); err != nil {
			return err
		}
	}

	// worker/all: register the three grouped jobs. The NZB Builder is a distinct
	// job because "creating the files" is the point of an indexer.
	if c.Process == "worker" || c.Process == "all" {
		p.crawlJob = c.Scheduler.RegisterJob("Usenet Crawler",
			"Fetches recent article overviews from active newsgroups").MarkOffPeak()
		p.backfillJob = c.Scheduler.RegisterJob("Usenet Backfill",
			"Walks each group's history backward to fill the retention window").MarkOffPeak()
		p.buildJob = c.Scheduler.RegisterJob("NZB Builder",
			"Assembles complete article sets into downloadable NZB files").MarkOffPeak()
		p.tagJob = c.Scheduler.RegisterJob("NZB Tag Fill",
			"Re-parses resolution/source/codec/audio/language tags for untagged NZBs")
		p.pruneJob = c.Scheduler.RegisterJob("NZB Prune",
			"Deletes NZBs older than the retention window")
		p.crawlJob.SetTrigger(func() { go p.runCrawl(p.ctx) })
		p.backfillJob.SetTrigger(func() { go p.runBackfill(p.ctx) })
		p.buildJob.SetTrigger(func() { go p.runBuild(p.ctx) })
		p.tagJob.SetTrigger(func() { go p.runTagFill(p.ctx) })
		p.pruneJob.SetTrigger(func() { go p.runPrune(p.ctx) })
		p.svc.triggerCrawl = func() { go p.runCrawl(p.ctx) }
		p.svc.triggerBackfill = func() { go p.runBackfill(p.ctx) }
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	// Look up the content-taxonomy plugin (optional). Done in Start, not
	// Provision, since plugin provision order isn't guaranteed. Both processes
	// need it: web for Newznab caps + display, worker for build categorization.
	if v, ok := p.core.Lookup(pluginapi.CatalogName); ok {
		if cat, ok := v.(pluginapi.Catalog); ok {
			p.catalog = cat
			p.svc.catalog = cat
		}
	}
	// Make the crawl/backfill intervals live-editable: the scheduler consults
	// this hook before every inter-tick sleep, so a settings change applies on
	// the next cycle without a restart. Chain the previous hook so other
	// plugins' / the host's jobs are unaffected.
	prevInterval := schedule.IntervalOverride
	schedule.IntervalOverride = func(ctx context.Context, jobName string, def time.Duration) time.Duration {
		switch jobName {
		case "Usenet Crawler", "NZB Builder":
			return time.Duration(p.effective(ctx).CrawlIntervalMin) * time.Minute
		case "Usenet Backfill":
			return time.Duration(p.effective(ctx).BackfillIntervalMin) * time.Minute
		}
		if prevInterval != nil {
			return prevInterval(ctx, jobName, def)
		}
		return def
	}

	if p.crawlJob == nil {
		return nil // web-only process: capability is registered, no jobs run here
	}
	p.seedServer(ctx)
	interval := time.Duration(p.cfg.CrawlIntervalMin) * time.Minute
	backfillInterval := time.Duration(p.cfg.BackfillIntervalMin) * time.Minute
	p.core.Scheduler.RunLoop(ctx, p.crawlJob, time.Minute, interval, p.runCrawl)
	// Boot backfill after the forward crawl has had a pass to seed watermarks.
	p.core.Scheduler.RunLoop(ctx, p.backfillJob, 3*time.Minute, backfillInterval, p.runBackfill)
	p.core.Scheduler.RunLoop(ctx, p.buildJob, 90*time.Second, interval, p.runBuild)
	p.core.Scheduler.RunLoop(ctx, p.tagJob, 5*time.Minute, 6*time.Hour, p.runTagFill)
	p.core.Scheduler.RunLoop(ctx, p.pruneJob, 10*time.Minute, 24*time.Hour, p.runPrune)
	return nil
}

// runTagFill retrofits quality tags onto NZBs missing them (build-time tagging
// covers new rows; this catches rows from before a parser change).
func (p *Plugin) runTagFill(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.tagJob.SetRunning()
	n, err := p.st.retagUntagged(ctx, 500)
	if err != nil {
		p.tagJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/tag-fill", err)
		return
	}
	p.tagJob.Log("re-tagged %d NZB(s)", n)
	// Recategorize releases still at the default (built before categorization or
	// before a catalog-rule change).
	if p.catalog != nil {
		if rc, err := p.st.recategorizeDefaults(ctx, p.catalog.Categorize, 2000); err != nil {
			p.core.Errors.Report(ctx, "usenet/recategorize", err)
		} else if rc > 0 {
			p.tagJob.Log("recategorized %d release(s)", rc)
		}
	}
	p.tagJob.SetIdle(time.Now().Add(6 * time.Hour))
}

func (p *Plugin) Stop(ctx context.Context) error {
	p.closePool()
	return nil
}

// categoryFor returns the Newznab category id for a release via the catalog
// plugin, or 8010 (Other/Misc) when the catalog isn't installed.
func (p *Plugin) categoryFor(group, title string) int {
	if p.catalog != nil {
		return p.catalog.Categorize(group, title)
	}
	return 8010
}

// seedServer inserts the config server if the table is empty (best-effort). Runs
// in Start (not Provision — no I/O there) so the crawler has a server to use.
func (p *Plugin) seedServer(ctx context.Context) {
	if p.cfg.Server.Host == "" {
		return
	}
	if _, ok, _ := p.st.getServer(ctx); ok {
		return
	}
	err := p.st.saveServer(ctx, pluginapi.Server{
		Host: p.cfg.Server.Host, Port: p.cfg.Server.Port, TLS: p.cfg.Server.TLS,
		Username: p.cfg.Server.Username, Password: p.cfg.Server.Password, Enabled: true,
	})
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/seed-server", err)
	}
}

// effective returns the config with any admin-edited settings overlaid (the
// /admin/settings knobs). Jobs call this at run start so edits apply on the
// next run without a restart. Falls back to the boot config on read error.
func (p *Plugin) effective(ctx context.Context) Config {
	s, err := p.st.getSettings(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/settings", err)
		return p.cfg
	}
	return p.cfg.withOverrides(s)
}

// runPrune deletes NZBs past the retention window + stale staged articles.
func (p *Plugin) runPrune(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.pruneJob.SetRunning()
	cfg := p.effective(ctx)
	n, err := p.st.pruneNzbs(ctx, cfg.RetentionDays)
	if err != nil {
		p.pruneJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/prune", err)
		return
	}
	staged, _ := p.staging.prune(ctx)
	// Sweep junk left over from before ingest filtering (obfuscated random-token
	// titles that assembled into garbage releases / clog staging).
	junkNzbs, err := p.st.deleteJunkNzbs(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-junk-nzbs", err)
	}
	junkStaged, err := p.staging.deleteJunkStaged(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-junk-staged", err)
	}
	p.pruneJob.Log("pruned %d NZBs (older than %dd) + %d stale staged; swept %d junk NZBs + %d junk staged",
		n, cfg.RetentionDays, staged, junkNzbs, junkStaged)
	p.pruneJob.SetIdle(time.Now().Add(24 * time.Hour))
}

// runCrawl (crawl.go) and runBuild (assemble.go) implement the crawl + assembly.
