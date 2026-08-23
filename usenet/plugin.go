// Package usenet is the delivery-axis plugin: a basic Usenet indexer that crawls
// the last few days of a set of newsgroups, assembles complete article sets into
// downloadable NZB files, and serves search / group-list / download through a
// capability the host's pages consume. It owns the "usenet" Postgres schema and
// groups its jobs — Crawler, Backfill, Builder, Tag Fill, Prune, Health — under one "Usenet" family.
//
// Staging (the transient article-assembly buffer) is pluggable behind the
// stagingStore seam: durable Postgres by default (never-lost, the base site's
// mode), or prod's Redis pipeline lifted verbatim via staging: redis (fast,
// best-effort) when the host has Redis. See README.md.
package usenet

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Job names. Registry identity, cluster-wide lease keys, interval overrides,
// the dashboard's ownership filter and the Jobs tab all key off these EXACT
// strings — a mistyped literal silently detaches one of those (a job whose
// interval knob never applies, or a lease nobody else contends). The "Usenet "
// prefix is deliberate: the host registers its own jobs in the same registry,
// and a generic name once collided with a host job.
const (
	jobNameCrawl       = "Usenet Crawler"
	jobNameBackfill    = "Usenet Backfill"
	jobNameBuild       = "Usenet Builder"
	jobNameTagFill     = "Usenet Tag Fill"
	jobNamePrune       = "Usenet Prune"
	jobNameHealth      = "Usenet Health Check"
	jobNameNFO         = "Usenet NFO Fetch"
	jobNameImage       = "Usenet Image Fetch"
	jobNameSpotIndex   = "Usenet Spot Index"
	jobNameSpotFetch   = "Usenet Spot Fetch"
	jobNameJunkProbe   = "Usenet Junk Probe"
	jobNameRot18Repair = "Usenet Title Repair"
)

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
	// runsJobs marks the process that owns the pass trackers (worker/all).
	// Processes without it render the worker-PUBLISHED telemetry instead of
	// their own empty trackers — see telemetry_publish.go.
	runsJobs bool

	crawlJob     core.Job
	backfillJob  core.Job
	buildJob     core.Job
	tagJob       core.Job
	pruneJob     core.Job
	healthJob    core.Job
	nfoJob       core.Job
	imageJob     core.Job
	spotJob      core.Job
	spotFetchJob core.Job
	junkProbeJob core.Job
	rot18Job     core.Job

	// per-job locks: a manual trigger (admin button / /admin/jobs) must not
	// overlap a scheduled run of the same job — they share one NNTP connection
	// and race on watermarks. ALL SIX jobs carry one: prune and tag-fill went
	// without for a while on the theory that the job lease covered them, but
	// the lease is reentrant for our own worker id, so a trigger overlapping a
	// scheduled tick ran two full-catalogue sweeps concurrently (and the first
	// finisher's release freed the row for a sibling — a third sweep).
	crawlMu     sync.Mutex
	backfillMu  sync.Mutex
	buildMu     sync.Mutex
	healthMu    sync.Mutex
	nfoMu       sync.Mutex
	imageMu     sync.Mutex
	spotMu      sync.Mutex
	opstats     *opCounter
	spotFetchMu sync.Mutex
	junkProbeMu sync.Mutex
	rot18Mu     sync.Mutex
	pruneMu     sync.Mutex
	tagMu       sync.Mutex

	// leaseHeldMu/leaseHeld refcount this process's lease holders per
	// scope|key, so one job's release cannot delete a row the other job still
	// works under (see lease.go).
	leaseHeldMu sync.Mutex
	leaseHeld   map[string]int

	// backfillPaused is the hysteresis latch for staging back-pressure; only
	// touched inside runBackfill (serialized by backfillMu).
	backfillPaused bool

	// One connection pool per configured provider (providers.go). Article
	// numbers are per-server, so providers share nothing numeric — only the
	// staging area, where message-id dedup turns overlap into better coverage.
	fleet *providerFleet

	// Live crawl counters + a short tail of recent errors, for /admin/crawlers
	// (telemetry.go).
	tel *telemetry
	// duty records each job's busy windows via the wrapped job handles, so
	// the Jobs tab can print a trailing duty percentage (duty.go).
	duty *dutyTracker
	// crawlStallStreak counts consecutive crawl passes ending in the
	// catch-up-stalled break with a large backlog. Only the crawl job touches
	// it (under crawlMu); telemetry carries a copy for the dashboards.
	crawlStallStreak int
	// lastPressureErrAt throttles the backfill's pressure-probe failure
	// report (backfill job only, so unguarded is fine).
	lastPressureErrAt time.Time
	// backfillProvLogAt throttles backfillProvider's per-round narration to
	// the catch-up summary's 30s cadence (backfill job only).
	backfillProvLogAt time.Time

	// Fingerprint of the junk rules currently compiled into memory, so a reload
	// only recompiles when they actually changed (junk_store.go).
	junkMu sync.Mutex
	junkFP string

	// hits accumulates filter-rule hits in memory; a pass flushes them in one
	// batch. Never written per article — see blacklist.go.
	hits *filterHits
	// posterWatch/posterHits trace WHY a specific poster's releases do or do
	// not appear. Off unless the operator adds patterns, and then one substring
	// check per article — see poster_watch.go.
	posterWatch *posterWatch
	posterHits  *posterHits
	// outcomes accounts for what each build pass did with every candidate set.
	outcomes *buildOutcomes
	// grouping accumulates the corpus sample + ungrouped-stem counts at
	// ingest — the observe-only instruments behind grouping changes
	// (grouping_watch.go).
	grouping *groupingWatch
	// resolutions accumulates completion-distance records — the measured
	// basis the position-based staging window will be derived from
	// (resolutions.go).
	resolutions *resolutionLog

	// lastGood caches the most recent successfully overlaid config, so a
	// transient settings-read failure keeps the operator's tuned knobs
	// instead of silently reverting a run to the boot defaults (effective()).
	lastGoodMu  sync.Mutex
	lastGoodCfg *Config

	// censusLastAt/censusLast rate-limit the staging census during catch-up
	// (takeStagingCensus). Build job only, so unguarded is fine.
	censusLastAt time.Time
	censusLast   stagingCensus
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "usenet",
		Version:     "0.1.0",
		Description: "A basic Usenet indexer: crawls recent posts, builds NZB files, and serves search / groups / download.",
		Processes:   []string{"web", "worker", "api"},
		// The indexer half. Without this a torrent-only site runs an NNTP
		// crawler against newsgroups it will never publish.
		Flavours:   []string{core.FlavourIndexer},
		Migrations: migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// The stylesheet these pages used to carry inline. See stylesheet.go.
	pluginapi.RegisterStylesheet(c, "usenet", usenetCSS)
	p.core = c

	// Config FIRST, because enabled is in it: a disabled indexer must not
	// declare events, create schema or register anything — the host's usenet
	// lookups are all optional, and absence is how they learn it is off.
	if err := c.Config.PluginInto("usenet", &p.cfg); err != nil {
		return fmt.Errorf("usenet: config: %w", err)
	}
	p.cfg.applyDefaults()
	if p.cfg.Enabled != nil && !*p.cfg.Enabled {
		log.Printf("usenet: plugins.usenet.enabled is false — the indexer is OFF. " +
			"Nothing crawls, no usenet pages mount, no jobs register.")
		return nil
	}

	// Announce assembled releases. A SYSTEM event -- the crawler did this, not
	// a member -- so nothing can score an achievement on it, which core
	// enforces rather than leaves to good sense.
	if err := declareEvents(c); err != nil {
		return err
	}
	pg := NewPGStore(c.Storage.SchemaDB("usenet"))
	p.st = pg
	p.fleet = newProviderFleet()
	p.tel = newTelemetry()
	p.duty = newDutyTracker()
	p.hits = newFilterHits()
	p.opstats = newOpCounter()
	p.posterHits = newPosterHits()
	p.posterWatch = newPosterWatch(nil) // replaced per pass from the DB
	p.outcomes = newBuildOutcomes()
	p.grouping = newGroupingWatch()
	p.resolutions = newResolutionLog()
	// Staging backend behind the seam. Limits are read per-call (via effective)
	// so the admin knobs apply live. nzbs writes always go through pg regardless.
	staging, err := newStaging(p.cfg.Staging, pg, c.Redis, func(ctx context.Context) (int, int) {
		e := p.effective(ctx)
		return e.StagingMaxRows, e.StagingPruneHours
	}, func(ctx context.Context) int {
		return p.effective(ctx).StagingTTLHours
	}, p.tel.noteEvicted, p.reportErr, func(base string) {
		// A staged set whose article span is far too wide to be one release —
		// two postings that collided on the same base subject. Counted on the
		// Filters tab beside the junk tallies; see the call site in
		// redis_staging.go for why this is measured rather than prevented.
		p.hits.noteN("grouping", "wide_article_span", 1, base)
	})
	if err != nil {
		return fmt.Errorf("usenet: staging: %w", err)
	}
	p.staging = staging
	// Fail fast on an unknown sink: SinkMode exists because a mistyped literal
	// would silently fall through to internal mode and SPLIT THE CATALOGUE —
	// but typing alone doesn't validate; this does (newStaging already does
	// the same for staging modes).
	if p.cfg.Sink != SinkInternal && p.cfg.Sink != SinkHost {
		return fmt.Errorf("usenet: unknown sink %q (want %q or %q)", p.cfg.Sink, SinkInternal, SinkHost)
	}
	p.svc = &service{store: p.st, retentionDays: p.cfg.RetentionDays}

	// Contribute indexer totals to the stats snapshot (collected in the worker
	// process; registering everywhere is harmless).
	if err := pluginapi.RegisterStats(c, statHook{store: p.st}); err != nil {
		return err
	}

	// Liveness counts for a host's stats widget — sanitized to numbers only,
	// so the host may expose it below admin. Every process: web serves it, a
	// worker-side cache job may snapshot it.
	if err := c.Register(pluginapi.UsenetActivityName, activitySurface{p: p}); err != nil {
		return err
	}

	// Optimizations, discovered by the "<plugin>.optimizer" suffix. Registered
	// in every process because Inspect is read-only and useful wherever the
	// admin page renders; Apply reaches the same store either way.
	if err := c.Register("usenet"+pluginapi.OptimizerSuffix, optimizer{p: p}); err != nil {
		return err
	}

	// The article probe: STAT and a body-head read, for whoever is planning a
	// repair. Every process, because it is read-only and the caller may be a
	// worker job or an ops endpoint on web. This plugin owns the pool and the
	// provider credentials, so nothing else has to grow a second copy of them.
	if err := c.Register(pluginapi.ArticleProbeName, articleProbe{p: p}); err != nil {
		return err
	}

	// web/all/api: publish the READ capabilities — the public site pages AND the
	// standalone api process both serve search / browse / Newznab / download.
	if c.Process == "web" || c.Process == "all" || c.Process == "api" {
		// Shows rather than releases — the same service, a separate contract.
		if err := c.Register(pluginapi.SeriesIndexName, pluginapi.SeriesIndex(p.svc)); err != nil {
			return fmt.Errorf("usenet: register series index: %w", err)
		}
		if err := c.Register(pluginapi.UsenetIndexName, p.svc); err != nil {
			return err
		}
		if err := c.Register(pluginapi.UsenetNewznabName, p.svc); err != nil {
			return err
		}
	}

	// The ADMIN capability registers in the worker too — not just where the
	// wizard renders: a host's stats-cache job (worker-side) reads Stats()
	// through it to publish newsgroup progression on its public stats page.
	// The admin VIEWS below stay web/all.
	if c.Process == "worker" {
		if err := c.Register(pluginapi.UsenetAdminName, p.svc); err != nil {
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
		// Machine-readable status, on the ADMIN group: it carries provider
		// hostnames, group names and raw error text, none of which belong on a
		// public route. Lets a run be watched without scraping the admin HTML.
		if r := c.Router.Admin("usenet"); r != nil {
			r.GET("/status.json", func(gc *gin.Context) {
				gc.JSON(200, p.status(gc.Request.Context()))
			})
		}
	}

	// worker/all: register the six pipeline jobs (all "Usenet "-prefixed so
	// they never collide with a host's own job names in the shared registry).
	if c.Process == "worker" || c.Process == "all" {
		// Every job handle is wrapped for duty accounting (duty.go): busy
		// windows record themselves at the SetRunning/SetIdle boundary the
		// jobs already drive, and telemetry publishes a trailing duty%.
		// Every job in this pipeline WRITES, so all six carry MarkWrites: crawl and
		// backfill fill staging, build assembles NZB rows, tag fill recategorises,
		// prune DELETES, health rewrites verdicts. This is the pipeline read-only
		// exists to stop — during a migration that copies from a live database, a
		// crawler still writing is the failure that leaves no trace, because the
		// dump's snapshot is taken at its start and everything committed afterwards
		// vanishes at cutover unlogged.
		//
		// MarkWrites ALONE DOES NOT STOP THEM, and this comment used to claim it
		// did. schedule.WriteGate is consulted only in ServiceLoop and TriggerJob,
		// and the automatic dispatch here reaches neither — so on 2026-08-11, with
		// read-only engaged and every HTTP write path correctly refusing, this
		// pipeline wrote 4 rows to nzbs in 45 seconds. MarkWrites earns the
		// /admin/jobs badge and gates the manual trigger; the actual hold-back is
		// p.mayWrite at the top of each pass (writegate.go), enforced by
		// TestEveryPassAsksTheWriteGate.
		p.crawlJob = p.duty.wrap(jobNameCrawl, c.Scheduler.RegisterJob(jobNameCrawl,
			"Fetches recent article overviews from active newsgroups").MarkOffPeak().MarkWrites())
		p.backfillJob = p.duty.wrap(jobNameBackfill, c.Scheduler.RegisterJob(jobNameBackfill,
			"Walks each group's history backward to fill the retention window").MarkOffPeak().MarkWrites())
		p.buildJob = p.duty.wrap(jobNameBuild, c.Scheduler.RegisterJob(jobNameBuild,
			"Assembles complete article sets into downloadable NZB files").MarkOffPeak().MarkWrites())
		p.tagJob = p.duty.wrap(jobNameTagFill, c.Scheduler.RegisterJob(jobNameTagFill,
			"Re-parses quality tags for untagged NZBs and recategorizes default-category releases").MarkWrites())
		p.pruneJob = p.duty.wrap(jobNamePrune, c.Scheduler.RegisterJob(jobNamePrune,
			"Sweeps stale staging + junk; deletes old NZBs only when nzb_retention_days is set").MarkWrites())
		p.healthJob = p.duty.wrap(jobNameHealth, c.Scheduler.RegisterJob(jobNameHealth,
			"STATs stored NZBs to find releases whose articles have expired").MarkOffPeak().MarkWrites())
		p.spotJob = p.duty.wrap(jobNameSpotIndex, c.Scheduler.RegisterJob(jobNameSpotIndex,
			"Lists Spotnet spots from free.pt forward, then backfills its history").MarkOffPeak().MarkWrites())
		p.spotFetchJob = p.duty.wrap(jobNameSpotFetch, c.Scheduler.RegisterJob(jobNameSpotFetch,
			"Reads each indexed spot's description and NZB, and publishes it as a release").MarkOffPeak().MarkWrites())
		p.nfoJob = p.duty.wrap(jobNameNFO, c.Scheduler.RegisterJob(jobNameNFO,
			"Reads .nfo articles and stores their text (off by default; spends provider bytes)").MarkOffPeak().MarkWrites())
		p.imageJob = p.duty.wrap(jobNameImage, c.Scheduler.RegisterJob(jobNameImage,
			"Reads proof/sample image articles and stores them as release screenshots (off by default; spends provider bytes)").MarkOffPeak().MarkWrites())
		p.junkProbeJob = p.duty.wrap(jobNameJunkProbe, c.Scheduler.RegisterJob(jobNameJunkProbe,
			"Reads the bodies of junk-dropped postings to see whether the yEnc header names a real file (off by default; spends provider bytes)").MarkOffPeak().MarkWrites())
		p.rot18Job = p.duty.wrap(jobNameRot18Repair, c.Scheduler.RegisterJob(jobNameRot18Repair,
			"Rewrites titles stored before the crawler could decode ROT18-rotated subjects (off by default; changes catalogue titles)").MarkOffPeak().MarkWrites())
		p.crawlJob.SetTrigger(func() { go p.runCrawl(p.ctx) })
		p.backfillJob.SetTrigger(func() { go p.runBackfill(p.ctx) })
		p.buildJob.SetTrigger(func() { go p.runBuild(p.ctx) })
		p.tagJob.SetTrigger(func() { go p.runTagFill(p.ctx) })
		p.pruneJob.SetTrigger(func() { go p.runPrune(p.ctx) })
		p.healthJob.SetTrigger(func() { go p.runHealthCheck(p.ctx) })
		p.nfoJob.SetTrigger(func() { go p.runNFO(p.ctx) })
		p.imageJob.SetTrigger(func() { go p.runImage(p.ctx) })
		p.spotJob.SetTrigger(func() { go p.runSpotIndex(p.ctx) })
		p.spotFetchJob.SetTrigger(func() { go p.runSpotFetch(p.ctx) })
		p.junkProbeJob.SetTrigger(func() { go p.runJunkProbe(p.ctx) })
		p.rot18Job.SetTrigger(func() { go p.runRot18Repair(p.ctx) })
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
		case jobNameCrawl, jobNameBuild:
			return time.Duration(p.effective(ctx).CrawlIntervalMin) * time.Minute
		case jobNameBackfill:
			return time.Duration(p.effective(ctx).BackfillIntervalMin) * time.Minute
		case jobNameHealth:
			return time.Duration(p.effective(ctx).HealthIntervalMin) * time.Minute
		case jobNameTagFill:
			return time.Duration(p.effective(ctx).TagFillIntervalMin) * time.Minute
		case jobNamePrune:
			return time.Duration(p.effective(ctx).PruneIntervalMin) * time.Minute
		}
		if prevInterval != nil {
			return prevInterval(ctx, jobName, def)
		}
		return def
	}

	if p.crawlJob == nil {
		return nil // web-only process: capability is registered, no jobs run here
	}
	// One structural line an operator can correlate: the resolved modes (a
	// sink=host flip otherwise gives no "wiring took" confirmation) and this
	// worker's id, which is the key to its rows in leases/crawler_workers when
	// several crawlers run.
	p.core.Logger.Info("usenet worker starting",
		"worker", workerID(), "sink", p.cfg.Sink, "staging", p.cfg.Staging)
	// This process owns the pass trackers; render local telemetry and publish
	// it for the web process (telemetry_publish.go).
	p.runsJobs = true
	go p.publishTelemetry(ctx)
	p.seedServer(ctx)
	// Carry a legacy host crawler's state (watermarks, groups, blacklist) so a
	// sink=host flip RESUMES rather than restarts. One-time; no-op elsewhere.
	p.adoptHostState(ctx)
	// Announce ourselves before the first crawl, so the split sees this worker.
	p.startHeartbeat(ctx, p.cfg)
	// Ship the reference data: junk rules (then compiled into memory) and the
	// curated newsgroup pack.
	p.seedAndLoadJunkRules(ctx)
	p.seedCuratedNewsgroups(ctx)
	interval := time.Duration(p.cfg.CrawlIntervalMin) * time.Minute
	backfillInterval := time.Duration(p.cfg.BackfillIntervalMin) * time.Minute
	p.core.Scheduler.RunLoop(ctx, p.crawlJob, time.Minute, interval, p.runCrawl)
	// Boot backfill after the forward crawl has had a pass to seed watermarks.
	p.core.Scheduler.RunLoop(ctx, p.backfillJob, 3*time.Minute, backfillInterval, p.runBackfill)
	p.core.Scheduler.RunLoop(ctx, p.buildJob, 90*time.Second, interval, p.runBuild)
	// Later boot delay than the crawler: on a cold start the crawler should
	// have the pool to itself while it catches up on what members are actually
	// waiting for.
	p.core.Scheduler.RunLoop(ctx, p.spotJob, 4*time.Minute,
		time.Duration(p.effective(ctx).SpotIntervalMin)*time.Minute, p.runSpotIndex)
	// After the index pass: on a cold start there is nothing to fetch until it
	// has listed something, so starting earlier only burns an empty worklist.
	p.core.Scheduler.RunLoop(ctx, p.spotFetchJob, 6*time.Minute,
		time.Duration(p.effective(ctx).SpotFetchIntervalMin)*time.Minute, p.runSpotFetch)
	p.core.Scheduler.RunLoop(ctx, p.tagJob, 5*time.Minute,
		time.Duration(p.cfg.TagFillIntervalMin)*time.Minute, p.runTagFill)
	p.core.Scheduler.RunLoop(ctx, p.pruneJob, 10*time.Minute,
		time.Duration(p.cfg.PruneIntervalMin)*time.Minute, p.runPrune)
	// Health checking runs on idle connections (TryDo), so a long boot delay just
	// keeps it out of the way while the crawler seeds watermarks.
	p.core.Scheduler.RunLoop(ctx, p.healthJob, 15*time.Minute,
		time.Duration(p.cfg.HealthIntervalMin)*time.Minute, p.runHealthCheck)
	// Title repair walks the host catalogue; it touches no provider connections,
	// so it only waits long enough for the plugin to finish booting. Both jobs
	// below return immediately when disabled, which is the default.
	p.core.Scheduler.RunLoop(ctx, p.rot18Job, 8*time.Minute,
		time.Duration(p.cfg.Rot18RepairIntervalMin)*time.Minute, p.runRot18Repair)
	// The junk probe DOES spend provider bytes and borrows connections, so it
	// boots late and behind the health sweep. It yields to the crawler on pool
	// pressure, so a long-running crawl is never blocked by it.
	p.core.Scheduler.RunLoop(ctx, p.junkProbeJob, 20*time.Minute,
		time.Duration(p.cfg.JunkProbeIntervalMin)*time.Minute, p.runJunkProbe)
	return nil
}

// runTagFill retrofits quality tags onto NZBs missing them (build-time tagging
// covers new rows; this catches rows from before a parser change).
func (p *Plugin) runTagFill(ctx context.Context) {
	if ctx == nil {
		return
	}
	// The read-only write gate (writegate.go). Every pass asks, because this
	// pipeline has four different ways to be started and only one of them ever
	// reached schedule.WriteGate.
	if !p.mayWrite(ctx, p.tagJob) {
		return
	}
	// TryLock prologue: see runPrune — the job lease is reentrant in-process
	// and retagUntagged's shared sweep cursor means two overlapping runs grab
	// and UPDATE the same 500 rows.
	if !p.tagMu.TryLock() {
		p.tagJob.Log("tag fill already running — skipping overlap")
		return
	}
	defer p.tagMu.Unlock()
	if !p.withLease(ctx, leaseScopeJob, jobNameTagFill, p.leaseTTL(p.effective(ctx)), func(ctx context.Context) {
		p.runTagFillLocked(ctx)
	}) {
		p.tagJob.Log("tag fill skipped — another worker holds this job")
		p.tagJob.SetIdle(p.nextTagFill(ctx))
	}
}

// nextTagFill / nextPrune are the displayed next-runs for their jobs —
// live effective interval, not a hardcoded constant, so the countdown
// tracks an admin interval change (same reason as nextCrawl).
func (p *Plugin) nextTagFill(ctx context.Context) time.Time {
	return time.Now().Add(time.Duration(p.effective(ctx).TagFillIntervalMin) * time.Minute)
}

func (p *Plugin) nextPrune(ctx context.Context) time.Time {
	return time.Now().Add(time.Duration(p.effective(ctx).PruneIntervalMin) * time.Minute)
}

func (p *Plugin) runTagFillLocked(ctx context.Context) {
	p.tagJob.SetRunning()
	n, err := p.st.retagUntagged(ctx, 500)
	if err != nil {
		p.tagJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/tag-fill", err)
		return
	}
	p.tagJob.Log("re-tagged %d NZB(s)", n)
	// Where each release sits in a series, read from the same title. Batched
	// bigger than the retag above because it is one UPDATE per row with no
	// parsing cost worth speaking of, and there are 160k rows to walk once.
	//
	// Reported as filed-of-read rather than just filed: two thirds of an index
	// is films and software, so "1,204 of 5,000" is the honest sentence and a
	// bare "1,204" would read as a low hit rate on a job that is working.
	if filed, read, err := p.st.fillEpisodes(ctx, 5000); err != nil {
		// Non-fatal to the pass: the retag above already succeeded, and losing
		// the whole job over the newer half would be a regression in the older.
		p.reportErr(ctx, "usenet/episode-fill", err)
		p.tagJob.Log("episode fill failed: %v", err)
	} else if read > 0 {
		p.tagJob.Log("filed %d of %d title(s) into a series", filed, read)
	}
	// Re-run the categoriser over the table and correct whatever disagrees with
	// the current rules — including rows that matched the WRONG rule, which
	// never sit at the default and so were previously never revisited.
	if p.catalog != nil {
		// Large enough to cover the whole table in ONE run, with real headroom.
		//
		// 100,000 was the first attempt and it was already short: the table
		// held 118,180 rows at the time, so a pass took two runs six hours
		// apart, and a rule change landed in halves twelve hours apart. The
		// symptom was a sweep reporting 169 corrections when 711 were due,
		// which reads like a broken rule rather than a half-finished pass.
		//
		// Cost is measured, not feared: 10,000 rows took 154ms of CPU over
		// 300ms wall, and the work is in-process categorise calls plus an
		// UPDATE only for the few percent that disagree — no network anywhere.
		// A quarter of a million rows is a few seconds.
		//
		// The cursor still paginates if the index outgrows even this, and a
		// short page wraps — so the worst case degrades to the old behaviour
		// rather than to a stall.
		if rc, err := p.st.recategorizeSweep(ctx, p.catalog.Categorize, 250000); err != nil {
			p.reportErr(ctx, "usenet/recategorize", err)
		} else if rc > 0 {
			p.tagJob.Log("recategorized %d release(s)", rc)
		}
	}
	p.tagJob.SetIdle(p.nextTagFill(ctx))
}

func (p *Plugin) Stop(ctx context.Context) error {
	if p.fleet != nil {
		p.fleet.closeAll()
	}
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
	if _, ok, err := p.st.getServer(ctx); err != nil {
		// A transient read error must NOT read as "table empty": proceeding
		// would saveServer the config seed over an operator-edited row.
		p.core.Errors.Report(ctx, "usenet/seed-server", err)
		return
	} else if ok {
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
// next run without a restart. On a read error it returns the LAST
// successfully overlaid config, not the boot config: reverting tuned knobs
// (batch size, walk-past toggles, caps) for one run because the settings
// table was briefly unreachable is a silent behaviour change on exactly the
// kind of run — mid-incident — where the operator's overrides matter most.
// Only a plugin that has never read its settings falls back to boot defaults.
func (p *Plugin) effective(ctx context.Context) Config {
	s, err := p.st.getSettings(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/settings", err)
		p.lastGoodMu.Lock()
		cached := p.lastGoodCfg
		p.lastGoodMu.Unlock()
		if cached != nil {
			return *cached
		}
		return p.cfg
	}
	cfg := p.cfg.withOverrides(s)
	p.lastGoodMu.Lock()
	p.lastGoodCfg = &cfg
	p.lastGoodMu.Unlock()
	return cfg
}

// runPrune deletes NZBs past the retention window + stale staged articles.
func (p *Plugin) runPrune(ctx context.Context) {
	if ctx == nil {
		return
	}
	// The read-only write gate (writegate.go). Every pass asks, because this
	// pipeline has four different ways to be started and only one of them ever
	// reached schedule.WriteGate.
	if !p.mayWrite(ctx, p.pruneJob) {
		return
	}
	// Same TryLock prologue as the other jobs: the cluster-wide lease does NOT
	// substitute (it is reentrant for our own worker id), so without this a
	// Jobs-tab click overlapping a scheduled tick ran two full-catalogue
	// sweeps at once.
	if !p.pruneMu.TryLock() {
		p.pruneJob.Log("prune already running — skipping overlap")
		return
	}
	defer p.pruneMu.Unlock()
	// Not group-scoped, so it must run once across the cluster or two workers
	// duplicate the sweep.
	if !p.withLease(ctx, leaseScopeJob, jobNamePrune, p.leaseTTL(p.effective(ctx)), func(ctx context.Context) {
		p.runPruneLocked(ctx)
	}) {
		p.pruneJob.Log("prune skipped — another worker holds this job")
		p.pruneJob.SetIdle(p.nextPrune(ctx))
	}
}

func (p *Plugin) runPruneLocked(ctx context.Context) {
	p.pruneJob.SetRunning()
	cfg := p.effective(ctx)
	// Releases are kept forever unless an operator explicitly sets a horizon.
	// This used to delete anything older than the CRAWL DEPTH (3 days by
	// default), which quietly destroyed the catalogue of any install left
	// running — the two are completely different concerns.
	var n int64
	if cfg.NZBRetentionDays > 0 {
		var err error
		n, err = p.st.pruneNzbs(ctx, cfg.NZBRetentionDays)
		if err != nil {
			p.pruneJob.SetError(err.Error())
			p.reportErr(ctx, "usenet/prune", err)
			return
		}
	}
	staged, err := p.staging.prune(ctx)
	if err != nil {
		// A silently-failing prune lets stale pg staging grow unbounded (only
		// masked by backfill back-pressure); surface it like its siblings below.
		p.reportErr(ctx, "usenet/prune-staged", err)
	}
	// Sweep junk left over from before ingest filtering (obfuscated random-token
	// titles that assembled into garbage releases). Staged junk needs no sweep:
	// ingest + build filtering never let it accumulate, and the staging age
	// horizon clears anything that predates them — the old per-tick sweep
	// full-scanned every distinct staged base_subject to delete nothing.
	junkNzbs, err := p.st.deleteJunkNzbs(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-junk-nzbs", err)
	}
	// The diagnostic series stay bounded here too. (The census prune existed
	// unwired since the table was born — unbounded growth in a diagnostic
	// table is how diagnostics become the problem they were added to find.)
	if _, err := p.st.pruneStagingCensus(ctx, cfg.DiagKeepDays); err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-census", err)
	}
	if _, err := p.st.pruneSubjectCorpus(ctx, cfg.DiagKeepDays); err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-corpus", err)
	}
	if _, err := p.st.pruneSetResolutions(ctx, cfg.DiagKeepDays); err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-resolutions", err)
	}
	// Instrument counters in filter_hits are the fastest-growing of the lot —
	// one row per novel subject stem, 2,260 in the watch's first day. Rule
	// counters in the same table are lifetime state and are never pruned.
	if _, err := p.st.pruneFilterDiagnostics(ctx, cfg.DiagKeepDays); err != nil {
		p.core.Errors.Report(ctx, "usenet/prune-filter-diagnostics", err)
	}
	kept := "kept forever"
	if cfg.NZBRetentionDays > 0 {
		kept = fmt.Sprintf("older than %dd", cfg.NZBRetentionDays)
	}
	p.pruneJob.Log("pruned %d NZB(s) (%s) + %d stale staged; swept %d junk NZBs",
		n, kept, staged, junkNzbs)
	p.pruneJob.SetIdle(p.nextPrune(ctx))
}

// runCrawl (crawl.go) and runBuild (assemble.go) implement the crawl + assembly.
