package curation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

func init() {
	core.RegisterPlugin("curation", func() core.Plugin { return &Plugin{} })
}

const (
	jobName         = "Season Curation"
	defaultInterval = 24 * time.Hour
	// bootDelay keeps the sweep off a freshly booted worker: the title
	// cleaner (which fills the easy title-marker cases first) also runs at
	// boot, and there is no reason to race it for the same rows.
	bootDelay = 15 * time.Minute
)

// Plugin wires the daily inference sweep and the fail-to-parse admin page.
type Plugin struct {
	core *core.Core
	job  *schedule.JobInfo

	runMu sync.Mutex
	deps  Deps
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "curation",
		Version:     "0.1.0",
		Description: "Season/episode inference for crawled releases: applies title, AniDB-entry and TMDB-season rules daily, and reports what it cannot infer.",
		Processes:   []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if !deps.ok() {
		return fmt.Errorf("curation: SetDeps was not called with a full Deps before core.Boot")
	}
	p.deps = *deps

	if c.Process == "web" || c.Process == "all" {
		if err := c.RegisterView(core.View{
			Slug:        "curation",
			Title:       "Season Curation",
			Slot:        core.SlotAdminPage,
			MinRole:     core.RoleAdmin,
			Description: "Season/episode fill coverage and the releases no rule can infer.",
			Nav:         core.NavHint{Group: "Catalog"},
			Render:      p.render,
		}); err != nil {
			return fmt.Errorf("curation: register view: %w", err)
		}
	}

	if c.Process != "worker" && c.Process != "all" {
		return nil
	}
	// Off-peak: the sweep re-reads every still-NULL row daily and writes in
	// bulk on its first runs; it has no reason to compete with site traffic,
	// and the /admin/jobs manual trigger bypasses the gate when an operator
	// wants it NOW.
	p.job = schedule.RegisterJob(jobName,
		"Fills season/episode on anime releases from title, AniDB entry name and TMDB season structure; unresolved rows feed the curation page").
		MarkOffPeak().MarkWrites()
	p.job.IntervalMin = int(defaultInterval.Minutes())
	p.job.SetTrigger(func() { go p.runSweep(context.Background()) })
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.job == nil {
		return nil // web process: page only
	}
	go schedule.ServiceLoop(ctx, p.job, bootDelay, defaultInterval, p.runSweep)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
