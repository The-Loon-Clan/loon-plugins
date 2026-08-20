// Package scraper is the generic metadata scraper: one plugin that owns a
// registry of pluggable catalog.MetadataSource modules (anidb, tmdb, mangadex,
// …) and runs a small set of SHARED jobs over all of them — instead of ~12
// per-domain scraper jobs. Adding a new source (e.g. imdb) is a new module in
// scraper/sources/, registered on the same catalog.Registry, with zero changes
// to this plugin or the host. See SCRAPER-ARCHITECTURE.md.
//
// This file is the proof-of-concept shell: it wires the plugin to the EXISTING
// catalog.Registry (already published on the Core extension registry and
// already carrying the anime/manga/movie sources) and implements ONE generic
// job — Metadata Fill — that loops every registered source. It compiles and
// vets against loon; the host-side wiring (publishing the registry in the
// worker core + implementing pluginapi.CatalogSink over the catalog_entry
// table) is the integration step.
package scraper

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("scraper", func() core.Plugin { return &Plugin{} })
}

// Config is the plugins.scraper section of config.yml — the Prowlarr-style
// enable-list plus job tuning. Per-source settings live under their own keys
// (plugins.scraper.anidb, plugins.scraper.tmdb, …) and are read by each module.
type Config struct {
	Sources     []string `json:"sources"`      // enabled source keys; empty = all registered
	FillBatch   int      `json:"fill_batch"`   // ids per source per fill run (default 100)
	IntervalMin int      `json:"interval_min"` // fill cadence in minutes (default 60)
}

type Plugin struct {
	core     *core.Core
	cfg      Config
	ctx      context.Context // root ctx captured in Start; read by admin triggers
	registry *catalog.Registry
	fillJob  core.Job
	matchJob core.Job
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "scraper",
		Version:     "0.1.0",
		Description: "Generic metadata scraper: shared jobs over a registry of pluggable MetadataSource modules (anidb, tmdb, mangadex, …).",
		// Worker-only: the scraper runs jobs, registers no routes.
		Processes: []string{"worker"},
		Flavours:  []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if deps == nil || deps.Sink == nil {
		return fmt.Errorf("scraper: SetDeps not called with a CatalogSink before core.Boot")
	}
	if err := c.Config.PluginInto("scraper", &p.cfg); err != nil {
		return fmt.Errorf("scraper: config: %w", err)
	}

	// The host publishes the shared catalog.Registry on the Core extension
	// registry (cmd/main.go: coreMediator.Register(catalog.RegistryExtension,
	// reg)). The scraper consumes it rather than owning its own — so the same
	// sources feed both the host's title-matching and these jobs.
	v, ok := c.Lookup(catalog.RegistryExtension)
	if !ok {
		return fmt.Errorf("scraper: %q not found — the host must Register the catalog.Registry in the worker core", catalog.RegistryExtension)
	}
	reg, ok := v.(*catalog.Registry)
	if !ok {
		return fmt.Errorf("scraper: %q is %T, want *catalog.Registry", catalog.RegistryExtension, v)
	}
	p.registry = reg

	p.fillJob = c.Scheduler.RegisterJob(
		"Metadata Fill",
		"Enriches catalog entries missing metadata across every registered source").
		MarkOffPeak().MarkWrites()
	p.fillJob.SetTrigger(func() { go p.runFill(p.ctx) })

	p.matchJob = c.Scheduler.RegisterJob(
		"Catalog Match",
		"Matches releases to catalog sources by title + category and links cover art").
		MarkOffPeak().MarkWrites()
	p.matchJob.SetTrigger(func() { go p.runMatch(p.ctx) })
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	interval := time.Duration(p.cfg.IntervalMin) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	p.core.Scheduler.RunLoop(ctx, p.fillJob, 2*time.Minute, interval, p.runFill)
	p.core.Scheduler.RunLoop(ctx, p.matchJob, 3*time.Minute, interval, p.runMatch)
	return nil
}

// Stop is a no-op: the fill loop derives from the Start ctx and exits on cancel.
func (p *Plugin) Stop(ctx context.Context) error { return nil }

// runFill is the generic Metadata-Fill job: ONE loop over EVERY registered,
// enabled source — replacing the per-domain fill jobs (AniDB Metadata Fill,
// Media Enrichment, Season Catalog, Cover Backfill). A source opts in by
// implementing pluginapi.Fillable; the sink persists each fetched entry into the
// unified catalog_entry table. Sources that don't implement Fillable are skipped
// (they still serve Fetch/TitleIndex for matching).
func (p *Plugin) runFill(ctx context.Context) {
	if ctx == nil {
		return // trigger fired before Start (should never happen)
	}
	p.fillJob.SetRunning()
	batch := p.cfg.FillBatch
	if batch <= 0 {
		batch = 100
	}

	var enriched, skipped int
	for _, src := range p.registry.Sources() {
		// Every early return after SetRunning must SetIdle, or the job
		// reads as running forever and stalls the host's shutdown drain.
		if ctx.Err() != nil {
			p.fillJob.SetIdle(time.Now().Add(p.fillInterval()))
			return
		}
		key := src.Domain().Key
		if !p.enabled(key) {
			continue
		}
		fillable, ok := src.(pluginapi.Fillable)
		if !ok {
			skipped++
			continue
		}
		ids, err := fillable.PendingIDs(ctx, batch)
		if err != nil {
			p.core.Errors.Report(ctx, "scraper/fill-pending", fmt.Errorf("source %s: %w", key, err))
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				p.fillJob.SetIdle(time.Now().Add(p.fillInterval()))
				return
			}
			entry, err := src.Fetch(ctx, id)
			if err != nil {
				p.core.Errors.Report(ctx, "scraper/fill-fetch", fmt.Errorf("source %s id %d: %w", key, id, err))
				continue
			}
			if err := deps.Sink.Upsert(ctx, entry); err != nil {
				p.core.Errors.Report(ctx, "scraper/fill-upsert", fmt.Errorf("source %s id %d: %w", key, id, err))
				continue
			}
			enriched++
		}
	}
	p.fillJob.Log("metadata fill: %d entries enriched, %d sources not fill-capable", enriched, skipped)
	p.fillJob.SetIdle(time.Now().Add(p.fillInterval()))
}

func (p *Plugin) fillInterval() time.Duration {
	if p.cfg.IntervalMin > 0 {
		return time.Duration(p.cfg.IntervalMin) * time.Minute
	}
	return time.Hour
}

// enabled reports whether a source key is in the configured allow-list. An empty
// list means "all registered sources" (the default).
func (p *Plugin) enabled(key string) bool {
	if len(p.cfg.Sources) == 0 {
		return true
	}
	for _, s := range p.cfg.Sources {
		if s == key {
			return true
		}
	}
	return false
}
