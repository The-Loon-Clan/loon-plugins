// Package anidbscraper is the worked example for this repo: a real ameNZB
// background job (the AniDB scraper) extracted into a loon plugin.
//
// It is a WORKER-only, HOST-DATA plugin — it owns no schema of its own but
// reads and writes the host's anime_metadata and nzbs tables through the narrow
// ports in pluginapi. That makes it the honest template for ~40 of the site's
// jobs, which are all coupled to host tables the same way (contrast the
// self-contained, schema-owning `store` / `guestbook` plugins).
//
// The lifecycle wiring, job registration, off-peak gating, and the port seams
// below are REAL and compile against loon. The scrape internals
// (buildTitleIndex, fetchMetadata) are stubbed with `// EXTRACT:` markers
// pointing at the exact source in pkg/services/anidb_service.go that moves in.
// This file is a scaffold, not a finished migration — see JOBS-AS-PLUGINS.md.
package anidbscraper

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("anidbscraper", func() core.Plugin { return &Plugin{} })
}

// Config is the plugins.anidbscraper section of config.yml. The two AniDB
// client fields are app.anidb_client / app.anidb_client_ver today (passed as
// scalar constructor args); moving them under the plugin namespace is a CLEAN
// change with no coupling.
type Config struct {
	Client         string `json:"client"`     // AniDB HTTP API client name
	ClientVer      int    `json:"client_ver"` // AniDB HTTP API client version
	ScanThrottleMs int    `json:"scan_throttle_ms"`
}

type Plugin struct {
	core *core.Core
	cfg  Config
	ctx  context.Context // root ctx captured in Start; read by admin triggers

	titlesJob core.Job
	scanJob   core.Job
	fillJob   core.Job
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "anidbscraper",
		Version:     "0.1.0",
		Description: "Downloads the AniDB titles index, scans untagged NZBs, and enriches the anime catalog from AniList.",
		// Worker-only: the scraper registers jobs, never routes. A route-
		// registering plugin booted in the worker (no router) would nil-panic.
		Processes: []string{"worker"},
		// No Migrations: the scraper operates on host-owned anime_metadata /
		// nzbs via injected ports; it does not own a Postgres schema. (A future
		// step that makes anime_metadata plugin-owned would add them here.)
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if deps == nil {
		return fmt.Errorf("anidbscraper: SetDeps not called before core.Boot")
	}
	if deps.Catalog == nil || deps.Nzbs == nil || deps.Matcher == nil {
		return fmt.Errorf("anidbscraper: Deps has nil Catalog/Nzbs/Matcher")
	}
	if err := c.Config.PluginInto("anidbscraper", &p.cfg); err != nil {
		return fmt.Errorf("anidbscraper: config: %w", err)
	}

	// Register jobs in Provision (registering at Start races the admin view's
	// registry snapshot — see loon core/scheduler.go). The production service
	// registers six; the exemplar wires the three that carry the core loop.
	p.titlesJob = c.Scheduler.RegisterJob(
		"AniDB Titles Index",
		"Downloads and indexes anime titles from AniDB (refreshes daily)").
		MarkWrites()
	p.scanJob = c.Scheduler.RegisterJob(
		"AniDB NZB Scanner",
		"Scans NZB titles against the AniDB index and tags matched anime_id").
		MarkOffPeak().MarkWrites()
	p.fillJob = c.Scheduler.RegisterJob(
		"AniDB Metadata Fill",
		"Fetches images and metadata from AniList for the anime catalog").
		MarkOffPeak().MarkWrites()

	// Manual /admin/jobs "run now" buttons (bypass the off-peak gate). The
	// callback has no ctx of its own, so it borrows the root ctx captured in
	// Start; triggers only ever fire at runtime, well after Start.
	p.scanJob.SetTrigger(func() { go p.runScan(p.ctx) })
	p.fillJob.SetTrigger(func() { go p.runFill(p.ctx) })
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	// Titles index refresh loop. The production job self-schedules at 00:05
	// daily (see refreshTitlesDump); modeled here as loon's standard RunLoop,
	// which already honors off-peak, admin interval overrides, and ctx cancel.
	p.core.Scheduler.RunLoop(ctx, p.titlesJob, 30*time.Second, 24*time.Hour, p.runTitlesRefresh)
	return nil
}

// Stop is a no-op: every loop and goroutine derives from the Start ctx, so
// they exit when the host cancels the root context during shutdown.
func (p *Plugin) Stop(ctx context.Context) error { return nil }

// runTitlesRefresh downloads the AniDB titles dump, rebuilds the shared matcher
// index, then chains the NZB scan — mirroring the production job.
//
// EXTRACT: body of pkg/services/anidb_service.go refreshTitlesDump / runTitlesDump.
func (p *Plugin) runTitlesRefresh(ctx context.Context) {
	p.titlesJob.SetRunning()
	index, err := p.buildTitleIndex(ctx)
	if err != nil {
		p.titlesJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "anidbscraper/titles", err)
		return
	}
	deps.Matcher.Rebuild(index)
	p.titlesJob.Log("AniDB index rebuilt: %d titles", len(index))
	p.titlesJob.SetIdle(nextMidnight())
	go p.runScan(ctx) // chain the scan after a fresh index, like production
}

// runScan cursor-walks untagged NZBs, matches each title, and writes anime_id
// back through the tag sink. This is the deepest coupling: it both READS and
// WRITES the host `nzbs` table via pluginapi.NzbTagSink.
//
// EXTRACT: body of pkg/services/anidb_service.go runNzbScan (with the per-batch
// OffPeakGate re-check and the 1ms/row throttle).
func (p *Plugin) runScan(ctx context.Context) {
	if ctx == nil {
		return // trigger fired before Start (should never happen)
	}
	p.scanJob.SetRunning()
	throttle := time.Duration(p.cfg.ScanThrottleMs) * time.Millisecond
	var cursor int64
	var tagged int
	for {
		// Every early return after SetRunning must SetIdle, or the job
		// reads as running forever and stalls the host's shutdown drain.
		if ctx.Err() != nil {
			p.scanJob.SetIdle(nextMidnight())
			return
		}
		rows, next, err := deps.Nzbs.UntaggedBatch(ctx, cursor, 500)
		if err != nil {
			p.scanJob.SetError(err.Error())
			p.core.Errors.Report(ctx, "anidbscraper/scan", err)
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if aid, ok := deps.Matcher.Find(row.Title); ok {
				if err := deps.Nzbs.SetAnimeID(ctx, row.ID, aid); err != nil {
					p.core.Errors.Report(ctx, "anidbscraper/scan-write", err)
					continue
				}
				tagged++
			}
			if throttle > 0 {
				time.Sleep(throttle)
			}
		}
		cursor = next
	}
	p.scanJob.Log("AniDB scan complete: %d newly tagged", tagged)
	p.scanJob.SetIdle(nextMidnight())
}

// runFill enriches catalog rows missing metadata/covers from AniList.
//
// EXTRACT: body of pkg/services/anidb_service.go runMetadataFill (AniList
// GraphQL client + its 1.5s rate limiter move with the plugin — CLEAN).
func (p *Plugin) runFill(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.fillJob.SetRunning()
	ids, err := deps.Catalog.IDsNeedingMetadata(ctx, 200)
	if err != nil {
		p.fillJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "anidbscraper/fill", err)
		return
	}
	var filled int
	for _, aid := range ids {
		if ctx.Err() != nil {
			p.fillJob.SetIdle(time.Time{})
			return
		}
		m, err := p.fetchMetadata(ctx, aid)
		if err != nil {
			p.core.Errors.Report(ctx, "anidbscraper/fill-fetch", err)
			continue
		}
		if err := deps.Catalog.Save(ctx, m); err != nil {
			p.core.Errors.Report(ctx, "anidbscraper/fill-save", err)
			continue
		}
		filled++
	}
	p.fillJob.Log("AniDB metadata fill: %d/%d anime", filled, len(ids))
	p.fillJob.SetIdle(time.Time{})
}

// ---- extraction stubs -------------------------------------------------------
// The bodies below move verbatim from pkg/services/anidb_service.go. They are
// stubbed so the package compiles and the wiring above is demonstrably correct.

// buildTitleIndex downloads animetitles.xml, merges it with catalog aliases, and
// builds the normalized-title -> AID map the matcher consumes.
//
// EXTRACT: refreshTitlesDump / loadTitlesCache (~L169-330). The dump fetch must
// switch from the bare http.Get it uses today to p.core.HTTPClient (SSRF-safe).
func (p *Plugin) buildTitleIndex(ctx context.Context) (map[string]int, error) {
	// Placeholder: the real index also parses the XML dump. Combining the two
	// catalog maps proves the AnimeCatalog port is sufficient for the read side.
	titles, err := deps.Catalog.AllTitles(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := deps.Catalog.AllAliases(ctx)
	if err != nil {
		return nil, err
	}
	for norm, aid := range aliases {
		if _, exists := titles[norm]; !exists {
			titles[norm] = aid
		}
	}
	return titles, nil
}

// fetchMetadata pulls AniList metadata + cover for one AID.
//
// EXTRACT: runMetadataFill / fetchFromAPI (~L1230-1600) — the AniList GraphQL
// client, its rate limiter, and the cover download (via deps.Covers) all move
// with the plugin.
func (p *Plugin) fetchMetadata(ctx context.Context, aid int) (*pluginapi.AnimeMetadata, error) {
	return nil, fmt.Errorf("anidbscraper: fetchMetadata not yet extracted (aid=%d)", aid)
}

// nextMidnight returns 00:05 tomorrow in local time (the production titles-dump
// cadence). EXTRACT: anidb_service.go nextMidnight.
func nextMidnight() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 5, 0, 0, now.Location())
}
