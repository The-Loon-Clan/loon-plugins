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
	Server ServerConfig `json:"server"`
	// RetentionDays is CRAWL DEPTH: how far back to fetch and backfill. It does
	// NOT delete anything.
	RetentionDays int `json:"retention_days"` // default 6431 (~17.6y, prod parity)

	// NZBRetentionDays deletes assembled releases older than N days. 0 = keep
	// forever, which is the default and what prod does. Deleting a catalogue is
	// not something a default should ever do quietly.
	NZBRetentionDays int `json:"nzb_retention_days"` // default 0 = never delete

	CrawlIntervalMin    int `json:"crawl_interval_min"`     // crawl cadence (default 15)
	TagFillIntervalMin  int `json:"tagfill_interval_min"`   // tag-fill + recategorize cadence (default 360)
	PruneIntervalMin    int `json:"prune_interval_min"`     // prune cadence (default 1440)
	BuildDrainPerPass   int `json:"build_drain_per_pass"`   // completed sets assembled per build pass (default 500)
	Batch               int `json:"batch"`                  // article-number span per OVER request (default 3000)
	MaxGroups           int `json:"max_groups"`             // cap active groups crawled per run (default 20; 0 = all, no cap)
	CrawlMaxBatches     int `json:"crawl_max_batches"`      // forward-pass batch budget (default 20000) — the catch-up loop rolls the remainder into the next round
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
	// HoldLowUntilBackfilled stops LOW-tier groups being crawled forward
	// while any CRITICAL group still has history to backfill. See
	// holdLowTier in provider_state.go for why ordering alone is not enough.
	HoldLowUntilBackfilled bool `json:"hold_low_until_backfilled"`
	// ReadyReapPerPass bounds the dead-entry sweep of nzb:ready per build pass.
	// Default 50000: a full circuit of a multi-million-entry queue takes several
	// passes, which is the point — the sweep must not cost more than the pass it
	// is clearing the way for.
	ReadyReapPerPass      int `json:"ready_reap_per_pass"`
	BackfillBatchesPerRun int `json:"backfill_batches_per_run"` // cap backward batches per backfill pass, across all groups (default 25)
	BackfillIntervalMin   int `json:"backfill_interval_min"`    // backfill cadence (default 5)

	// Staging backend (README.md). Boot config, not a live knob:
	// switching backends at runtime would strand staged data.
	Staging StagingMode `json:"staging"` // pg (durable, default) | redis (fast, best-effort)

	// Sink is where assembled releases go: SinkInternal (the plugin's own minimal
	// nzbs table — standalone installs, the demo) or SinkHost (the host registers
	// the ReleaseSink capability and owns the NZB domain — how prod adopts the
	// crawler). Boot config: switching sinks live would split the catalogue.
	Sink              SinkMode `json:"sink"`
	StagingMaxRows    int      `json:"staging_max_rows"`    // pg back-pressure denominator: staged rows / this (default 2_000_000)
	StagingPruneHours int      `json:"staging_prune_hours"` // pg stale-staging horizon in hours (default 6)
	StagingTTLHours   int      `json:"staging_ttl_hours"`   // redis staged-key TTL in hours (default 2) — must exceed the gap between passes that stage parts of one release

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
	if c.Batch <= 0 {
		c.Batch = 3000
	}
	if c.MaxGroups <= 0 {
		c.MaxGroups = 20
	}
	if c.CrawlMaxBatches <= 0 {
		c.CrawlMaxBatches = 20000
	}
	if c.ReadyReapPerPass <= 0 {
		c.ReadyReapPerPass = 50000
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
}

// knobFields maps admin-editable integer setting keys to the Config field each
// overrides. One place to keep the settings form, the save action, and the
// override resolution in sync — no hardcoded operational values.
func (c *Config) knobFields() map[string]*int {
	return map[string]*int{
		"connections":                &c.Connections,
		"keepalive_min":              &c.KeepaliveMin,
		"retention_days":             &c.RetentionDays,
		"nzb_retention_days":         &c.NZBRetentionDays,
		"crawl_interval_min":         &c.CrawlIntervalMin,
		"tagfill_interval_min":       &c.TagFillIntervalMin,
		"prune_interval_min":         &c.PruneIntervalMin,
		"build_drain_per_pass":       &c.BuildDrainPerPass,
		"backfill_drain_wait_sec":    &c.BackfillDrainWaitSec,
		"batch":                      &c.Batch,
		"max_groups":                 &c.MaxGroups,
		"crawl_max_batches":          &c.CrawlMaxBatches,
		"max_articles_per_group":     &c.MaxArticlesPerGroup,
		"ready_reap_per_pass":        &c.ReadyReapPerPass,
		"backfill_interval_min":      &c.BackfillIntervalMin,
		"backfill_batches_per_run":   &c.BackfillBatchesPerRun,
		"staging_max_rows":           &c.StagingMaxRows,
		"staging_prune_hours":        &c.StagingPruneHours,
		"staging_ttl_hours":          &c.StagingTTLHours,
		"assign_term_min":            &c.AssignTermMin,
		"worker_stale_sec":           &c.WorkerStaleSec,
		"lease_ttl_min":              &c.LeaseTTLMin,
		"health_interval_min":        &c.HealthIntervalMin,
		"health_batch_size":          &c.HealthBatchSize,
		"health_recheck_days":        &c.HealthRecheckDays,
		"health_min_age_hours":       &c.HealthMinAgeHours,
		"health_stat_chunk":          &c.HealthStatChunk,
		"backfill_pressure_high_pct": &c.BackfillPressureHighPct,
		"crawl_pressure_high_pct":    &c.CrawlPressureHighPct,
		"backfill_pressure_low_pct":  &c.BackfillPressureLowPct,
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
