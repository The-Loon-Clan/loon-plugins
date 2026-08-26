package usenet

import "strconv"

// SinkMode selects where assembled releases are stored. It drives the
// catalogue-splitting branch in resolveSink/resolveHealthBackend, so it is a
// closed type rather than a raw string: a mistyped literal would silently fall
// through to internal mode and split the catalogue across two tables.
type SinkMode string

const (
	SinkInternal SinkMode = "internal" // the plugin's own nzbs table (default)
	SinkHost     SinkMode = "host"     // the host's NZB domain, via the ReleaseSink capability
)

// StagingMode selects the transient article-assembly backend.
type StagingMode string

const (
	StagingPG    StagingMode = "pg"    // durable Postgres (default)
	StagingRedis StagingMode = "redis" // prod's Redis pipeline (fast, best-effort)
)

// Config is the plugins.usenet section of config.yml. The server here seeds the
// servers table on first boot if it's empty; after that the wizard owns it.
// The numeric knobs are DEFAULTS — rows in the plugin's settings table
// (edited on the host's /admin/settings page) override them at job run time
// via withOverrides.
type Config struct {
	// Enabled, ABSENT, is true — the opposite default from the tracker, and
	// deliberately so: a tracker answers announces the moment it is reachable
	// and must be asked for, while an indexer that vanished because an
	// operator upgraded and never added a key would be a catalogue going
	// quietly stale. A pointer so absence is distinguishable from an explicit
	// false, which is a torrent-flavour host saying it means it: nothing
	// crawls, no pages mount, no jobs register.
	Enabled *bool `json:"enabled"`

	Server ServerConfig `json:"server"`
	// RetentionDays is CRAWL DEPTH: how far back to fetch and backfill. It does
	// NOT delete anything.
	RetentionDays int `json:"retention_days"` // default 6431 (~17.6y, prod parity)

	// NZBRetentionDays deletes assembled releases older than N days. 0 = keep
	// forever, which is the default and what prod does. Deleting a catalogue is
	// not something a default should ever do quietly.
	NZBRetentionDays int `json:"nzb_retention_days"` // default 0 = never delete

	CrawlIntervalMin   int `json:"crawl_interval_min"`   // crawl cadence (default 15)
	TagFillIntervalMin int `json:"tagfill_interval_min"` // tag-fill + recategorize cadence (default 360)
	PruneIntervalMin   int `json:"prune_interval_min"`   // prune cadence (default 1440)
	BuildDrainPerPass  int `json:"build_drain_per_pass"` // completed sets assembled per build pass (default 500)
	Batch              int `json:"batch"`                // article-number span per OVER request (default 3000)
	MaxGroups          int `json:"max_groups"`           // cap active groups crawled per run (default 20; 0 = all, no cap)
	CrawlMaxBatches    int `json:"crawl_max_batches"`    // forward-pass batch budget (default 20000) — the catch-up loop rolls the remainder into the next round
	// CrawlHeadroom is how many articles below the server's reported high water
	// mark the forward crawl stops, leaving the newest articles for the next
	// pass. 0 disables it.
	//
	// Articles do not appear atomically. An article number can exist while its
	// overview line is still being written or still propagating between peers,
	// so a batch that runs right up to the high water mark comes back short —
	// and crawl.go then records the whole requested range as fetched coverage
	// anyway. Walk-past eviction reasons FROM that coverage, treating "covered
	// and still short" as proof the missing articles are never coming, so a
	// frontier fetched too eagerly produces false dead verdicts and salvaged
	// BROKEN releases out of content that was merely still arriving.
	//
	// Nothing is lost by waiting: the next pass picks the articles up, and the
	// catch-up loop means "the next pass" is usually seconds away. NNTmux
	// leaves a comparable window for the same reason.
	CrawlHeadroom       int `json:"crawl_headroom"`         // articles left below the high water mark (default 2 batches)
	MaxArticlesPerGroup int `json:"max_articles_per_group"` // cap the first-pass volume so a busy group can't pull millions (default 20000)

	// Connections is the NNTP pool size — how many articles can be fetched in
	// parallel. Providers cap concurrent connections per account; the pool keeps
	// whatever it can open, so overshooting is safe but pointless.
	Connections int `json:"connections"` // default 10

	// KeepaliveMin is how often idle pool connections are probed, in minutes.
	// 0 disables keepalive.
	//
	// Providers reap idle connections, and a crawl pass leaves most of the pool
	// untouched between runs — so without probing, the steady state is a pool
	// full of connections the server already closed, discovered only when the
	// next pass leases one. Not a hardcoded constant because the right value is
	// the provider's idle timeout, which differs per provider and is rarely
	// documented.
	KeepaliveMin int `json:"keepalive_min"` // default 2

	SkipBackfill   bool `json:"skip_backfill"`    // "new articles only" — disable the backfill job
	CrawlNoCatchup bool `json:"crawl_no_catchup"` // disable the catch-up loop (default off = catch-up ON)
	// BackfillNoCatchup disables the backfill's catch-up loop. Same inverted
	// sense as the crawl one: the zero value keeps catching up, because a job
	// with hundreds of millions of articles outstanding should not sleep.
	BackfillNoCatchup bool `json:"backfill_no_catchup"`
	// BuildNoCatchup disables the builder's catch-up loop. Same inverted sense:
	// a builder holding the backfill's release valve should not nap.
	BuildNoCatchup bool `json:"build_no_catchup"`
	// BackfillDrainWaitSec is how long the backfill will wait for the builder to
	// make room before ending its pass. It waits rather than returning so the
	// two jobs run together instead of taking turns — the builder is the only
	// thing that can relieve the pressure the backfill is blocked on.
	BackfillDrainWaitSec int `json:"backfill_drain_wait_sec"`
	// BackfillPressureCeilingPct is the hard stop that applies even when there is
	// nothing for the builder to drain. Above the normal high-water mark because
	// in that state pausing achieves nothing — but still short of full, because
	// at maxmemory Redis EVICTS rather than refusing the write, and the sets it
	// evicts are the ones still assembling.
	BackfillPressureCeilingPct int `json:"backfill_pressure_ceiling_pct"`
	// HoldLowUntilBackfilled stops LOW-tier groups being crawled forward
	// while any CRITICAL group still has history to backfill. See
	// holdLowTier in provider_state.go for why ordering alone is not enough.
	HoldLowUntilBackfilled bool `json:"hold_low_until_backfilled"`
	// WalkPastNoEvict disables the walk-past sweep (inverted so the zero value
	// sweeps): a set whose whole article span has been fetched and is still
	// incomplete can never complete, and every hour it waits for the TTL is an
	// hour of staging memory held against the pressure gate.
	WalkPastNoEvict bool `json:"walk_past_no_evict"`
	// WalkPastGraceMin is how long a set must go without a new article before
	// the walk-past sweep may judge it (default 15) — covers retried batches
	// and staging latency at the walk edge.
	WalkPastGraceMin int `json:"walk_past_grace_min"`
	// WalkPastSweepPerRound bounds how many staged sets the walk-past sweep
	// examines per build round (default 2000). The cursor persists, so the
	// sweep RATE is this budget times the round frequency.
	WalkPastSweepPerRound int `json:"walk_past_sweep_per_round"`
	// WalkPastNoSalvage disables broken-release salvage (inverted so the zero
	// value salvages): walk-past-dead sets holding most of their articles are
	// then evicted like the rest instead of being assembled and stored marked
	// broken (repairable gaps) or normal (par2-only gaps).
	WalkPastNoSalvage bool `json:"walk_past_no_salvage"`
	// ReadyReapPerPass bounds the dead-entry sweep of nzb:ready per build
	// ROUND (the name predates the round/pass split; the stored key stays for
	// compatibility). Default 50000: a full circuit of a multi-million-entry
	// queue takes several rounds, which is the point — the sweep must not cost
	// more than the round it is clearing the way for. Per round matters: the
	// cursor persists, so the sweep RATE is this budget times the call
	// frequency, and a catch-up pass has no round cap.
	ReadyReapPerPass      int `json:"ready_reap_per_pass"`
	BackfillBatchesPerRun int `json:"backfill_batches_per_run"` // cap backward batches per backfill pass, across all groups (default 25)
	BackfillIntervalMin   int `json:"backfill_interval_min"`    // backfill cadence (default 5)
	// DiagKeepDays is the rolling window for the observe-only diagnostic
	// series: staging_census, subject_corpus, set_resolutions.
	//
	// A knob because their volume is driven by the CRAWLER's behaviour, not by
	// ours: set_resolutions took 1,070 rows/minute while the walk-past sweep
	// cleared a backlog (settling to ~120), and the next reset will burst
	// again. At 195 bytes a row that is the difference between half a gigabyte
	// and several, and the fix must not require a deploy. Default 14 days.
	DiagKeepDays int `json:"diag_keep_days"`

	// Staging backend (README.md). Boot config, not a live knob:
	// switching backends at runtime would strand staged data.
	Staging StagingMode `json:"staging"` // pg (durable, default) | redis (fast, best-effort)

	// Sink is where assembled releases go: SinkInternal (the plugin's own minimal
	// nzbs table — standalone installs, the demo) or SinkHost (the host registers
	// the ReleaseSink capability and owns the NZB domain — how prod adopts the
	// crawler). Boot config: switching sinks live would split the catalogue.
	Sink              SinkMode `json:"sink"`
	StagingMaxRows    int      `json:"staging_max_rows"`     // pg back-pressure denominator: staged rows / this (default 2_000_000)
	StagingPruneHours int      `json:"staging_prune_hours"`  // pg stale-staging horizon in hours (default 6)
	StagingTTLHours   int      `json:"staging_ttl_hours"`    // redis staged-key TTL in hours (default 2) — must exceed the gap between passes that stage parts of one release
	EvictStaleSecs    int      `json:"evict_staleness_secs"` // redis inline hopeless-eviction staleness window in seconds (default 300) — must exceed routine staging-pressure pauses or resumed sets are judged abandoned

	// Splitting groups between crawlers (assign.go). Membership is fixed for a
	// TERM, so a crawler that joins mid-term waits for the next boundary rather
	// than changing everyone's share underneath a pass in flight.
	AssignTermMin  int `json:"assign_term_min"`  // default 15
	WorkerStaleSec int `json:"worker_stale_sec"` // presence timeout, default 90

	// Cross-host coordination (lease.go). How long a claimed lease survives
	// without renewal — long enough that a slow pass never loses its own claim,
	// short enough that a killed worker's work is picked up promptly.
	LeaseTTLMin int `json:"lease_ttl_min"` // default 15

	// NZB health checking (health.go). Segments are STATted on idle connections
	// only, so these bound how much bookkeeping runs, not how fast it must.
	HealthIntervalMin int `json:"health_interval_min"`  // sweep cadence (default 60)
	HealthBatchSize   int `json:"health_batch_size"`    // releases per sweep (default 50)
	HealthRecheckDays int `json:"health_recheck_days"`  // re-check a release this often (default 30)
	HealthMinAgeHours int `json:"health_min_age_hours"` // propagation guard: skip releases newer than this (default 24)
	HealthStatChunk   int `json:"health_stat_chunk"`    // segments STATted per connection lease (default 200)
	// HealthStatTimeoutSec bounds ONE STAT, as opposed to OpTimeoutSec which
	// bounds a whole command exchange and is sized for a 3000-article OVER.
	//
	// The sweep borrows the crawler's pool and inherited its 60s, so a socket
	// the provider had already closed cost a full minute to discover — three
	// times per release before the release was abandoned. A measured pass
	// spent 19 minutes to check ONE release. A STAT is a single short line:
	// if it has not answered in seconds the connection is dead, and the whole
	// value of finding that out is finding it out cheaply.
	HealthStatTimeoutSec int `json:"health_stat_timeout_sec"` // per-STAT deadline (default 10)
	// HealthTransportYield: how many releases in a row may fail on TRANSPORT
	// (the provider timed out mid-STAT) before the pass gives up. Not the same
	// as the pool being busy, which still yields on the first refusal so the
	// crawler keeps priority. This exists because the yield used to be decided
	// per release and end the whole pass: against a provider that times out
	// routinely the first release tripped it every time, and the sweep checked
	// nothing for weeks while logging a plausible "pool busy or failing".
	HealthTransportYield int `json:"health_transport_yield"` // consecutive transport-failed releases before yielding (default 5)

	// NFO extraction (nfo.go). The first feature built on article bodies --
	// the crawler indexes from OVERVIEW lines and has never read one.
	//
	// NFOEnabled defaults FALSE. Every other job here is bookkeeping against
	// data already paid for; this one spends provider bytes, and a block
	// account's bytes are finite and metered. An operator should choose to
	// spend them rather than discover the choice was made for them by an
	// upgrade.
	NFOEnabled bool `json:"nfo_enabled"` // read .nfo articles at all (default false)
	// Spotnet. The index pass is cheap by design -- one XOVER round trip per
	// SpotBatchSize articles -- so its budget is expressed in BATCHES, and a
	// full history sweep is a few thousand of them rather than millions.
	SpotIntervalMin int `json:"spot_interval_min"` // pass cadence (default 15)
	SpotBatchSize   int `json:"spot_batch_size"`   // articles per XOVER (default 1000)
	SpotMaxBatches  int `json:"spot_max_batches"`  // XOVER round trips per pass, forward + backfill (default 200)
	// The fetch pass is the expensive half: TWO article reads per spot (the
	// document, then the NZB). Its batch is therefore in SPOTS, not batches,
	// and is two orders of magnitude smaller than the index pass's budget.
	SpotFetchIntervalMin int `json:"spot_fetch_interval_min"` // pass cadence (default 10)
	SpotFetchBatch       int `json:"spot_fetch_batch"`        // spots per pass (default 200)

	NFOIntervalMin int `json:"nfo_interval_min"` // pass cadence (default 60)
	NFOBatchSize   int `json:"nfo_batch_size"`   // releases per pass (default 100)
	// NFOBudgetMB caps the bytes ONE PASS may read. The genuinely new control
	// this feature needs: providers meter bytes, so unlike connection pressure
	// -- which the pool already expresses and TryDo already yields to -- there
	// is nothing in the existing machinery that notices bytes being consumed.
	// Checked BEFORE each fetch, since a ceiling that one whole article can
	// exceed is not a ceiling.
	NFOBudgetMB int `json:"nfo_budget_mb"` // per-pass byte ceiling (default 64)
	// The junk-recovery probe (junk_probe.go). Off by default for the same
	// reason NFO is -- it spends metered bytes -- and additionally because it
	// answers a question rather than serving a feature: is the crawler
	// discarding real releases on the strength of a scrambled subject? The
	// batch is expressed in ARTICLES rather than MB because the wire cost of
	// one probe is a whole segment however few bytes we keep.
	// The ROT18 title repair (rot18_repair.go). Off by default because it
	// REWRITES catalogue titles: the decode is safe on rows a literal marker
	// matches, but "safe" is a property of the marker list, and an operator
	// should switch that on deliberately rather than find a thousand titles
	// changed after an upgrade. It spends no provider bytes.
	Rot18RepairEnabled     bool `json:"rot18_repair_enabled"`      // repair ROT18 titles (default false)
	Rot18RepairIntervalMin int  `json:"rot18_repair_interval_min"` // pass cadence (default 60)
	// Rot18RepairMaxMin bounds ONE pass. The walk is the whole catalogue the
	// first time (~1M rows) and nothing after that, so the budget exists to
	// keep the first pass from holding the job lease for an unbounded stretch,
	// not to ration work.
	Rot18RepairMaxMin int `json:"rot18_repair_max_min"` // minutes one pass may run (default 10)

	JunkProbeEnabled     bool `json:"junk_probe_enabled"`      // read dropped-junk bodies at all (default false)
	JunkProbeIntervalMin int  `json:"junk_probe_interval_min"` // pass cadence (default 360)
	JunkProbeBatchSize   int  `json:"junk_probe_batch_size"`   // drops per pass (default 50)
	// NFOMaxRetries bounds how many TRANSPORT failures one release may cost
	// before it is written off. A 430 is permanent and written off at once;
	// a timeout says nothing about the article, so it is counted instead --
	// but uncounted it would be retried forever, and a few unreachable
	// articles at the head of the queue consume every pass. Newznab bounds
	// the same thing by decrementing nfostatus toward a floor. 0 disables the
	// ceiling and restores retry-forever.
	NFOMaxRetries int `json:"nfo_max_retries"` // transport failures before write-off (default 3)

	// Proof-image extraction (image.go), the second body-fetch feature. Same
	// default-off reasoning as NFO — it spends metered provider bytes — and a
	// bigger per-item cost: a proof JPG spans several whole articles where an
	// NFO is one small one.
	ImageEnabled     bool `json:"image_enabled"`      // fetch proof images at all (default false)
	ImageIntervalMin int  `json:"image_interval_min"` // pass cadence (default 60)
	ImageBatchSize   int  `json:"image_batch_size"`   // releases per pass (default 25)
	ImageBudgetMB    int  `json:"image_budget_mb"`    // per-pass byte ceiling (default 128)
	ImageMaxRetries  int  `json:"image_max_retries"`  // transport failures before write-off (default 3)

	// NNTP transport bounds. Per-provider behavior lives on the servers table;
	// these are the plugin-wide dial/operation limits every pool is built with.
	// DialTimeoutSec bounds one connect+greeting attempt. OpTimeoutSec bounds
	// one whole command exchange — one GROUP+OVER round — and interacts with
	// `batch`: a bigger batch on a slow provider legitimately takes longer, and
	// an OpTimeout below the honest fetch time turns every batch into a
	// discarded connection and a reconnect storm.
	DialTimeoutSec int `json:"dial_timeout_sec"` // default 30
	OpTimeoutSec   int `json:"op_timeout_sec"`   // default 60
	// ProviderDownCooldownMin is how long a provider stays benched after
	// failing. Long enough to stop re-dialling a dead server every pass, short
	// enough that recovery is noticed the same hour.
	ProviderDownCooldownMin int `json:"provider_down_cooldown_min"` // default 10

	// Backfill back-pressure thresholds (percent of staging pressure). Backfill
	// pauses at high, resumes below low; the forward crawl is never paused.
	BackfillPressureHighPct int `json:"backfill_pressure_high_pct"` // default 85
	// CrawlPressureHighPct stops the FORWARD crawl staging when the staging
	// backend is this full. Higher than the backfill gate on purpose: new
	// articles matter more than history, so the forward crawl yields only when
	// storing would actively destroy what is already there.
	CrawlPressureHighPct   int `json:"crawl_pressure_high_pct"`   // default 95
	BackfillPressureLowPct int `json:"backfill_pressure_low_pct"` // default 70
}

type ServerConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      bool   `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *Config) applyDefaults() {
	if c.RetentionDays <= 0 {
		// Matches prod's per-group default: effectively "all retention". The
		// first backfill is long, but it is bounded per pass
		// (backfill_batches_per_run) so it fills in gradually rather than
		// hammering a provider on day one.
		c.RetentionDays = 6431
	}
	// Deliberately NOT defaulted: 0 means "keep everything".
	if c.NZBRetentionDays < 0 {
		c.NZBRetentionDays = 0
	}
	if c.CrawlIntervalMin <= 0 {
		c.CrawlIntervalMin = 15
	}
	if c.TagFillIntervalMin <= 0 {
		c.TagFillIntervalMin = 360
	}
	if c.PruneIntervalMin <= 0 {
		c.PruneIntervalMin = 1440
	}
	if c.BuildDrainPerPass <= 0 {
		c.BuildDrainPerPass = 500
	}
	if c.BackfillDrainWaitSec <= 0 {
		c.BackfillDrainWaitSec = 180
	}
	if c.BackfillPressureCeilingPct <= 0 {
		// 92 leaves ~640 MB of an 8 GB ceiling. A round stages on the order of
		// 25 MB, and pressure is re-read every round, so there is ample margin
		// to stop before maxmemory.
		c.BackfillPressureCeilingPct = 92
	}
	if c.Batch <= 0 {
		c.Batch = 3000
	}
	if c.MaxGroups <= 0 {
		c.MaxGroups = 20
	}
	if c.CrawlHeadroom < 0 {
		c.CrawlHeadroom = 0
	} else if c.CrawlHeadroom == 0 {
		// Two batch windows. Enough to cover a propagation lag measured in
		// tens of thousands of articles on a busy group, small enough that the
		// frontier never falls meaningfully behind.
		c.CrawlHeadroom = 2 * c.Batch
	}
	if c.CrawlMaxBatches <= 0 {
		c.CrawlMaxBatches = 20000
	}
	if c.DiagKeepDays <= 0 {
		c.DiagKeepDays = 14
	}
	if c.ReadyReapPerPass <= 0 {
		c.ReadyReapPerPass = 50000
	}
	if c.WalkPastGraceMin <= 0 {
		c.WalkPastGraceMin = 15
	}
	if c.WalkPastSweepPerRound <= 0 {
		c.WalkPastSweepPerRound = 2000
	}
	if c.MaxArticlesPerGroup <= 0 {
		c.MaxArticlesPerGroup = 20000
	}
	if c.BackfillBatchesPerRun <= 0 {
		c.BackfillBatchesPerRun = 25 // pull more history per pass so releases complete
	}
	if c.BackfillIntervalMin <= 0 {
		c.BackfillIntervalMin = 5 // keep backfilling frequently, not once every 30 min
	}
	if c.Connections <= 0 {
		c.Connections = 10
	}
	// Unset means "use the default"; an explicit 0 means "off" and is carried
	// through withOverrides' zero allowlist below. Negative is nonsense, and a
	// negative ticker interval panics, so clamp it to off.
	if c.KeepaliveMin == 0 {
		c.KeepaliveMin = 2
	}
	if c.KeepaliveMin < 0 {
		c.KeepaliveMin = 0
	}
	if c.Staging == "" {
		c.Staging = StagingPG
	}
	if c.Sink == "" {
		c.Sink = SinkInternal
	}
	if c.StagingMaxRows <= 0 {
		c.StagingMaxRows = 2_000_000
	}
	if c.StagingPruneHours <= 0 {
		c.StagingPruneHours = 6
	}
	if c.StagingTTLHours <= 0 {
		c.StagingTTLHours = 2
	}
	if c.EvictStaleSecs <= 0 {
		c.EvictStaleSecs = 300
	}
	if c.AssignTermMin <= 0 {
		c.AssignTermMin = 15
	}
	if c.WorkerStaleSec <= 0 {
		c.WorkerStaleSec = 90
	}
	if c.LeaseTTLMin <= 0 {
		c.LeaseTTLMin = 15
	}
	if c.HealthIntervalMin <= 0 {
		c.HealthIntervalMin = 60
	}
	if c.SpotIntervalMin <= 0 {
		c.SpotIntervalMin = defaultSpotIntervalMin
	}
	if c.SpotBatchSize <= 0 {
		c.SpotBatchSize = defaultSpotBatchSize
	}
	if c.SpotMaxBatches <= 0 {
		c.SpotMaxBatches = defaultSpotMaxBatches
	}
	if c.SpotFetchIntervalMin <= 0 {
		c.SpotFetchIntervalMin = defaultSpotFetchIntervalMin
	}
	if c.SpotFetchBatch <= 0 {
		c.SpotFetchBatch = defaultSpotFetchBatch
	}
	if c.NFOIntervalMin <= 0 {
		c.NFOIntervalMin = 60
	}
	if c.NFOBatchSize <= 0 {
		c.NFOBatchSize = 100
	}
	if c.NFOBudgetMB <= 0 {
		c.NFOBudgetMB = 64
	}
	if c.Rot18RepairIntervalMin <= 0 {
		c.Rot18RepairIntervalMin = 60
	}
	if c.Rot18RepairMaxMin <= 0 {
		c.Rot18RepairMaxMin = 10
	}
	if c.JunkProbeIntervalMin <= 0 {
		c.JunkProbeIntervalMin = 360
	}
	if c.JunkProbeBatchSize <= 0 {
		c.JunkProbeBatchSize = 50
	}
	if c.NFOMaxRetries < 0 {
		c.NFOMaxRetries = 0 // explicit opt-out: retry forever
	} else if c.NFOMaxRetries == 0 {
		c.NFOMaxRetries = 3
	}
	if c.ImageIntervalMin <= 0 {
		c.ImageIntervalMin = 60
	}
	if c.ImageBatchSize <= 0 {
		c.ImageBatchSize = 25
	}
	if c.ImageBudgetMB <= 0 {
		c.ImageBudgetMB = 128
	}
	if c.ImageMaxRetries < 0 {
		c.ImageMaxRetries = 0 // explicit opt-out: retry forever
	} else if c.ImageMaxRetries == 0 {
		c.ImageMaxRetries = 3
	}
	if c.HealthBatchSize <= 0 {
		c.HealthBatchSize = 50
	}
	if c.HealthRecheckDays <= 0 {
		c.HealthRecheckDays = 30
	}
	if c.HealthMinAgeHours <= 0 {
		c.HealthMinAgeHours = 24
	}
	if c.HealthStatChunk <= 0 {
		c.HealthStatChunk = 200
	}
	if c.HealthTransportYield <= 0 {
		c.HealthTransportYield = 5
	}
	if c.HealthStatTimeoutSec <= 0 {
		c.HealthStatTimeoutSec = 10
	}
	if c.DialTimeoutSec <= 0 {
		c.DialTimeoutSec = 30
	}
	if c.OpTimeoutSec <= 0 {
		c.OpTimeoutSec = 60
	}
	if c.ProviderDownCooldownMin <= 0 {
		c.ProviderDownCooldownMin = 10
	}
	if c.BackfillPressureHighPct <= 0 {
		c.BackfillPressureHighPct = 85
	}
	if c.CrawlPressureHighPct <= 0 {
		c.CrawlPressureHighPct = 95
	}
	if c.BackfillPressureLowPct <= 0 {
		c.BackfillPressureLowPct = 70
	}
	if c.Server.Port == 0 {
		c.Server.Port = 119
	}
	c.normalize()
}

// normalize repairs pressure-knob combinations no gate can operate under. The
// settings form rejects them loudly (validateKnobs); this is the backstop for
// values that arrive around the form — config.yml, hand-inserted settings
// rows, writes from an older binary. Repair rather than refuse, because this
// runs at effective() time with nobody to show an error to, and a mis-set
// gate must never be able to disable eviction protection: a percentage past
// 100 reverts to its default, the high gate is clamped under the ceiling, and
// low stays strictly below high so the hysteresis stays real. (The crawl's
// gate is deliberately not ordered against the ceiling here — crawlStopPct
// clamps it at the point of use.)
func (c *Config) normalize() {
	if c.BackfillPressureHighPct > 100 {
		c.BackfillPressureHighPct = 85
	}
	if c.CrawlPressureHighPct > 100 {
		c.CrawlPressureHighPct = 95
	}
	if c.BackfillPressureLowPct > 100 {
		c.BackfillPressureLowPct = 70
	}
	if c.BackfillPressureCeilingPct > 100 {
		c.BackfillPressureCeilingPct = 92
	}
	if c.BackfillPressureHighPct > c.BackfillPressureCeilingPct {
		c.BackfillPressureHighPct = c.BackfillPressureCeilingPct
	}
	if c.BackfillPressureLowPct >= c.BackfillPressureHighPct {
		c.BackfillPressureLowPct = c.BackfillPressureHighPct - 1
	}
}

// knobFields maps admin-editable integer setting keys to the Config field each
// overrides. One place to keep the settings form, the save action, and the
// override resolution in sync — no hardcoded operational values.
func (c *Config) knobFields() map[string]*int {
	return map[string]*int{
		"connections":                   &c.Connections,
		"nfo_interval_min":              &c.NFOIntervalMin,
		"nfo_batch_size":                &c.NFOBatchSize,
		"nfo_budget_mb":                 &c.NFOBudgetMB,
		"nfo_max_retries":               &c.NFOMaxRetries,
		"image_interval_min":            &c.ImageIntervalMin,
		"image_batch_size":              &c.ImageBatchSize,
		"image_budget_mb":               &c.ImageBudgetMB,
		"image_max_retries":             &c.ImageMaxRetries,
		"junk_probe_interval_min":       &c.JunkProbeIntervalMin,
		"rot18_repair_interval_min":     &c.Rot18RepairIntervalMin,
		"rot18_repair_max_min":          &c.Rot18RepairMaxMin,
		"junk_probe_batch_size":         &c.JunkProbeBatchSize,
		"keepalive_min":                 &c.KeepaliveMin,
		"retention_days":                &c.RetentionDays,
		"nzb_retention_days":            &c.NZBRetentionDays,
		"crawl_interval_min":            &c.CrawlIntervalMin,
		"tagfill_interval_min":          &c.TagFillIntervalMin,
		"prune_interval_min":            &c.PruneIntervalMin,
		"build_drain_per_pass":          &c.BuildDrainPerPass,
		"backfill_drain_wait_sec":       &c.BackfillDrainWaitSec,
		"backfill_pressure_ceiling_pct": &c.BackfillPressureCeilingPct,
		"batch":                         &c.Batch,
		"max_groups":                    &c.MaxGroups,
		"crawl_max_batches":             &c.CrawlMaxBatches,
		"max_articles_per_group":        &c.MaxArticlesPerGroup,
		"ready_reap_per_pass":           &c.ReadyReapPerPass,
		"diag_keep_days":                &c.DiagKeepDays,
		"walk_past_grace_min":           &c.WalkPastGraceMin,
		"walk_past_sweep_per_round":     &c.WalkPastSweepPerRound,
		"backfill_interval_min":         &c.BackfillIntervalMin,
		"backfill_batches_per_run":      &c.BackfillBatchesPerRun,
		"staging_max_rows":              &c.StagingMaxRows,
		"staging_prune_hours":           &c.StagingPruneHours,
		"staging_ttl_hours":             &c.StagingTTLHours,
		"evict_staleness_secs":          &c.EvictStaleSecs,
		"assign_term_min":               &c.AssignTermMin,
		"worker_stale_sec":              &c.WorkerStaleSec,
		"lease_ttl_min":                 &c.LeaseTTLMin,
		"health_interval_min":           &c.HealthIntervalMin,
		"health_batch_size":             &c.HealthBatchSize,
		"health_recheck_days":           &c.HealthRecheckDays,
		"health_min_age_hours":          &c.HealthMinAgeHours,
		"health_stat_chunk":             &c.HealthStatChunk,
		"health_transport_yield":        &c.HealthTransportYield,
		"health_stat_timeout_sec":       &c.HealthStatTimeoutSec,
		"backfill_pressure_high_pct":    &c.BackfillPressureHighPct,
		"crawl_pressure_high_pct":       &c.CrawlPressureHighPct,
		"backfill_pressure_low_pct":     &c.BackfillPressureLowPct,
		"dial_timeout_sec":              &c.DialTimeoutSec,
		"op_timeout_sec":                &c.OpTimeoutSec,
		"provider_down_cooldown_min":    &c.ProviderDownCooldownMin,
	}
}

// boolFields maps admin-editable boolean setting keys to their Config field.
func (c *Config) boolFields() map[string]*bool {
	return map[string]*bool{
		"skip_backfill": &c.SkipBackfill,
		// Inverted flag so the zero value = catch-up enabled: a crawler that
		// KNOWS it is behind should not sleep out the interval by default.
		"crawl_no_catchup": &c.CrawlNoCatchup,
		// Off by default: it deliberately starves a whole tier, which is the
		// right call on a site whose critical group is far behind and the
		// wrong one on a site that is caught up everywhere.
		"hold_low_until_backfilled": &c.HoldLowUntilBackfilled,
		"backfill_no_catchup":       &c.BackfillNoCatchup,
		"build_no_catchup":          &c.BuildNoCatchup,
		"walk_past_no_evict":        &c.WalkPastNoEvict,
		"walk_past_no_salvage":      &c.WalkPastNoSalvage,
		// Off by default: the one job here that spends provider bytes, so an
		// operator turns it on deliberately. It belongs in this map and not
		// only in config.yml, or the only way to enable it is a file edit on
		// the box -- which is the shape of setting this project already
		// decided against.
		"nfo_enabled":          &c.NFOEnabled,
		"image_enabled":        &c.ImageEnabled,
		"junk_probe_enabled":   &c.JunkProbeEnabled,
		"rot18_repair_enabled": &c.Rot18RepairEnabled,
	}
}

// withOverrides overlays DB settings onto the config defaults: positive ints for
// knobFields, true/false for boolFields. Invalid/missing values keep the default.
func (c Config) withOverrides(s map[string]string) Config {
	out := c
	for key, dst := range out.knobFields() {
		if raw, ok := s[key]; ok {
			if n, err := strconv.Atoi(raw); err == nil {
				// A stored 0 is IGNORED (keep the built-in default) for most
				// knobs, but is MEANINGFUL for a few where 0 is a real setting:
				// nzb_retention_days (0 = "keep forever", promised in the UI),
				// max_groups (0 = "crawl every active group, no cap") and
				// keepalive_min (0 = "no idle probing"). For those a stored 0
				// must override a non-zero config.yml default.
				if n > 0 || (n == 0 && (key == "nzb_retention_days" || key == "max_groups" || key == "keepalive_min")) {
					*dst = n
				}
			}
		}
	}
	for key, dst := range out.boolFields() {
		if raw, ok := s[key]; ok {
			*dst = raw == "true" || raw == "1" || raw == "on"
		}
	}
	// Stored rows can carry values the form would have refused (hand-inserted,
	// or saved by a binary from before validation existed).
	out.normalize()
	return out
}

// shouldPauseForPressure decides whether a staging backend is too full to write
// into. Extracted so the crawl loop and its test exercise the SAME code: a test
// that restates the comparison passes happily while the caller does something
// else, which is how three drifted duplicates in this plugin started.
//
// A zero threshold disables the gate — meaningful on a backend that cannot
// destroy what it holds (pg staging, or redis under noeviction, where a full
// backend refuses the write instead of evicting).
func shouldPauseForPressure(pressure float64, highPct int) bool {
	if highPct <= 0 {
		return false
	}
	return pressure >= float64(highPct)/100.0
}

// crawlStopPct is the forward crawl's EFFECTIVE pause threshold: its own high
// gate, clamped to the eviction ceiling. The crawl gate deliberately sits
// above the backfill's (95 vs 85) so new articles win the last of the room —
// but the default 95 also sat above the 92% ceiling that exists because past
// it Redis EVICTS rather than refuses, and what it evicts is the forming
// sets. The crawl was allowed to begin staging inside the exact band the
// backfill treats as a hard stop even with nothing to drain. Clamping keeps
// the crawl-beats-backfill ordering (92 is still above 85) while making the
// ceiling mean what its name says for every writer.
func (c Config) crawlStopPct() int {
	if c.BackfillPressureCeilingPct > 0 && c.CrawlPressureHighPct > c.BackfillPressureCeilingPct {
		return c.BackfillPressureCeilingPct
	}
	return c.CrawlPressureHighPct
}
