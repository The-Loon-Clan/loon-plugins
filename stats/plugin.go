// Package stats is the site-stats collector + presenter: on a cache job it
// gathers every plugin's StatContributor hook into one snapshot, caches it via
// the host sink, and serves it back through two loon views — a role-gated
// "Site stats" page and a small home-page widget. Plugins opt in by
// implementing pluginapi.StatContributor and calling pluginapi.RegisterStats
// in Provision — discovered off the core extension registry, so adding a
// stat-contributing plugin needs no change here.
package stats

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("stats", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core *core.Core
	ctx  context.Context
	job  core.Job

	// latest is the most recent snapshot, kept in memory for the views. In a
	// split web/worker deployment the web process has no collector — its views
	// show "no snapshot yet" until a shared read-back seam lands in Deps.
	mu       sync.Mutex
	latest   []pluginapi.Stat
	latestAt time.Time
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "stats",
		Version:     "0.2.0",
		Description: "Collects plugin StatContributor hooks into a cached snapshot and serves the site-stats page + widget.",
		Processes:   []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c

	// worker/all: the collector job. The host cache sink is required here.
	if c.Process == "worker" || c.Process == "all" {
		if deps == nil || deps.Cache == nil {
			return fmt.Errorf("stats: SetDeps not called with a Cache sink before core.Boot")
		}
		p.job = c.Scheduler.RegisterJob("Stats Cache", "Collects plugin StatContributor hooks into the cached stats snapshot")
		p.job.SetTrigger(func() { go p.run(p.ctx) })
	}

	// web/all: the presentation views ("if a user is logged in they can see
	// site stats" — any account; bump MinRole to gate by rank).
	if c.Process == "web" || c.Process == "all" {
		if err := p.registerViews(c); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	if p.job == nil {
		return nil // web-only process: views registered, no collector here
	}
	p.core.Scheduler.RunLoop(ctx, p.job, time.Minute, time.Hour, p.run)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

func (p *Plugin) run(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.job.SetRunning()

	var all []pluginapi.Stat
	contributors := pluginapi.StatContributors(p.core)
	for _, sc := range contributors {
		// Every early return after SetRunning must SetIdle, or the job
		// reads as running forever and stalls the host's shutdown drain.
		if ctx.Err() != nil {
			p.job.SetIdle(time.Now().Add(time.Hour))
			return
		}
		stats, err := sc.Stats(ctx)
		if err != nil {
			p.core.Errors.Report(ctx, "stats/collect", fmt.Errorf("%s: %w", sc.StatsName(), err))
			continue
		}
		all = append(all, stats...)
	}

	p.mu.Lock()
	p.latest, p.latestAt = all, time.Now()
	p.mu.Unlock()

	if err := deps.Cache(ctx, all); err != nil {
		p.job.SetError(err.Error())
		p.core.Errors.Report(ctx, "stats/cache", err)
		return
	}
	p.job.Log("stats cached: %d metrics from %d contributors", len(all), len(contributors))
	p.job.SetIdle(time.Now().Add(time.Hour))
}

func (p *Plugin) snapshot() ([]pluginapi.Stat, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest, p.latestAt
}
