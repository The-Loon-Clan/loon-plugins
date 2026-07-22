package usenet

import "strconv"

// Config is the plugins.usenet section of config.yml. The server here seeds the
// servers table on first boot if it's empty; after that the wizard owns it.
// The numeric knobs are DEFAULTS — rows in the plugin's settings table
// (edited on the host's /admin/settings page) override them at job run time
// via withOverrides.
type Config struct {
	Server              ServerConfig `json:"server"`
	RetentionDays       int          `json:"retention_days"`         // keep the last N days (default 3)
	CrawlIntervalMin    int          `json:"crawl_interval_min"`     // crawl cadence (default 15)
	Batch               int          `json:"batch"`                  // article-number span per OVER request (default 3000)
	MaxGroups           int          `json:"max_groups"`             // cap active groups crawled per run (default 20)
	MaxArticlesPerGroup int          `json:"max_articles_per_group"` // cap the first-pass volume so a busy group can't pull millions (default 20000)

	SkipBackfill          bool `json:"skip_backfill"`            // "new articles only" — disable the backfill job
	BackfillBatchesPerRun int  `json:"backfill_batches_per_run"` // cap backward batches per backfill pass, across all groups (default 10)
	BackfillIntervalMin   int  `json:"backfill_interval_min"`    // backfill cadence (default 30)

	// Staging backend (USENET-STAGING-MODES.md). Boot config, not a live knob:
	// switching backends at runtime would strand staged data.
	Staging           string `json:"staging"`             // pg (durable, default) | redis (fast, best-effort)
	StagingMaxRows    int    `json:"staging_max_rows"`    // pg back-pressure denominator: staged rows / this (default 2_000_000)
	StagingPruneHours int    `json:"staging_prune_hours"` // pg stale-staging horizon in hours (default 6)

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
		c.RetentionDays = 3
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
	if c.Staging == "" {
		c.Staging = "pg"
	}
	if c.StagingMaxRows <= 0 {
		c.StagingMaxRows = 2_000_000
	}
	if c.StagingPruneHours <= 0 {
		c.StagingPruneHours = 6
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
		"retention_days":             &c.RetentionDays,
		"crawl_interval_min":         &c.CrawlIntervalMin,
		"batch":                      &c.Batch,
		"max_groups":                 &c.MaxGroups,
		"max_articles_per_group":     &c.MaxArticlesPerGroup,
		"backfill_interval_min":      &c.BackfillIntervalMin,
		"backfill_batches_per_run":   &c.BackfillBatchesPerRun,
		"staging_max_rows":           &c.StagingMaxRows,
		"staging_prune_hours":        &c.StagingPruneHours,
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
