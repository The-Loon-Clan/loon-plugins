package usenet

import "strconv"

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
	Batch               int `json:"batch"`                  // article-number span per OVER request (default 3000)
	MaxGroups           int `json:"max_groups"`             // cap active groups crawled per run (default 20)
	MaxArticlesPerGroup int `json:"max_articles_per_group"` // cap the first-pass volume so a busy group can't pull millions (default 20000)

	// Connections is the NNTP pool size — how many articles can be fetched in
	// parallel. Providers cap concurrent connections per account; the pool keeps
	// whatever it can open, so overshooting is safe but pointless.
	Connections int `json:"connections"` // default 10

	SkipBackfill          bool `json:"skip_backfill"`            // "new articles only" — disable the backfill job
	BackfillBatchesPerRun int  `json:"backfill_batches_per_run"` // cap backward batches per backfill pass, across all groups (default 25)
	BackfillIntervalMin   int  `json:"backfill_interval_min"`    // backfill cadence (default 5)

	// Staging backend (USENET-STAGING-MODES.md). Boot config, not a live knob:
	// switching backends at runtime would strand staged data.
	Staging string `json:"staging"` // pg (durable, default) | redis (fast, best-effort)

	// Sink is where assembled releases go: "internal" (the plugin's own minimal
	// nzbs table — standalone installs, the demo) or "host" (the host registers
	// the ReleaseSink capability and owns the NZB domain — how prod adopts the
	// crawler). Boot config: switching sinks live would split the catalogue.
	Sink              string `json:"sink"`
	StagingMaxRows    int    `json:"staging_max_rows"`    // pg back-pressure denominator: staged rows / this (default 2_000_000)
	StagingPruneHours int    `json:"staging_prune_hours"` // pg stale-staging horizon in hours (default 6)

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
	BackfillPressureLowPct  int `json:"backfill_pressure_low_pct"`  // default 70
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
	if c.Batch <= 0 {
		c.Batch = 3000
	}
	if c.MaxGroups <= 0 {
		c.MaxGroups = 20
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
	if c.Staging == "" {
		c.Staging = "pg"
	}
	if c.Sink == "" {
		c.Sink = "internal"
	}
	if c.StagingMaxRows <= 0 {
		c.StagingMaxRows = 2_000_000
	}
	if c.StagingPruneHours <= 0 {
		c.StagingPruneHours = 6
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
		"retention_days":             &c.RetentionDays,
		"nzb_retention_days":         &c.NZBRetentionDays,
		"crawl_interval_min":         &c.CrawlIntervalMin,
		"batch":                      &c.Batch,
		"max_groups":                 &c.MaxGroups,
		"max_articles_per_group":     &c.MaxArticlesPerGroup,
		"backfill_interval_min":      &c.BackfillIntervalMin,
		"backfill_batches_per_run":   &c.BackfillBatchesPerRun,
		"staging_max_rows":           &c.StagingMaxRows,
		"staging_prune_hours":        &c.StagingPruneHours,
		"assign_term_min":            &c.AssignTermMin,
		"worker_stale_sec":           &c.WorkerStaleSec,
		"lease_ttl_min":              &c.LeaseTTLMin,
		"health_interval_min":        &c.HealthIntervalMin,
		"health_batch_size":          &c.HealthBatchSize,
		"health_recheck_days":        &c.HealthRecheckDays,
		"health_min_age_hours":       &c.HealthMinAgeHours,
		"health_stat_chunk":          &c.HealthStatChunk,
		"backfill_pressure_high_pct": &c.BackfillPressureHighPct,
		"backfill_pressure_low_pct":  &c.BackfillPressureLowPct,
	}
}

// boolFields maps admin-editable boolean setting keys to their Config field.
func (c *Config) boolFields() map[string]*bool {
	return map[string]*bool{
		"skip_backfill": &c.SkipBackfill,
	}
}

// withOverrides overlays DB settings onto the config defaults: positive ints for
// knobFields, true/false for boolFields. Invalid/missing values keep the default.
func (c Config) withOverrides(s map[string]string) Config {
	out := c
	for key, dst := range out.knobFields() {
		if raw, ok := s[key]; ok {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				*dst = n
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
