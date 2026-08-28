// Package events owns scheduled events: named spans of time other plugins hang
// behaviour on. A season, a launch week, a daily reset, a one-off announcement
// date.
//
// The definition is a cron expression plus an optional duration; a job
// materialises concrete windows ahead of time, so "is event X open right now" is
// one indexed lookup rather than a cron evaluation per query.
//
// It was inside the rewards plugin first, whose own schema comment admitted the
// concept was "not reward-specific in meaning even though it lives here for
// now". Rewards gates recurring payouts on a window; news wants to publish a
// post when an event opens; a future leaderboard wants to reset on one. None of
// them should reach into rewards to ask.
package events

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("events", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core  *core.Core
	store Store
	svc   *Service
	job   *schedule.JobInfo
	tmpl  *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "events",
		Version:     "0.1.0",
		Description: "Scheduled events — named time windows (seasons, resets, launch weeks) other plugins can gate on.",
		Migrations:  migrations,
		// web serves the admin screens and answers the capability; worker runs
		// the generator. Both need the capability registered, because a consumer
		// on the worker asking "is the season open" is the same question.
		Processes: []string{"web", "worker"},
		Flavours:  []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c

	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("events: Core.Storage.SchemaDB is nil")
	}
	p.store = NewPGStore(db)
	p.svc = NewService(p.store)

	// Published before anything else so a consumer's own Provision can look it
	// up without depending on plugin load order.
	c.RegisterDef(core.ExtensionDef{
		Name:    pluginapi.ScheduledEventsName,
		Kind:    core.ExtService,
		Stable:  true,
		Since:   "0.1.0",
		Summary: "scheduled time windows: which events exist, which are open now, when one next opens",
	}, pluginapi.ScheduledEvents(p.svc))

	// The admin page. Registered on any process that has a router — the worker
	// has none, so registerViews is skipped there rather than failing.
	if c.Process != "worker" {
		if err := p.registerViews(c); err != nil {
			return fmt.Errorf("events: register views: %w", err)
		}
	}

	if c.Process == "worker" || c.Process == "all" {
		// Manually triggerable from /admin/jobs, which matters here: the
		// operator loop is "create an event, see its windows", and without a
		// trigger the answer to "where are my windows" is "wait up to thirty
		// minutes" — which reads as broken and is how somebody concludes the
		// event is misconfigured when it is merely early.
		p.job = schedule.RegisterJob("Event Windows",
			"Materialise scheduled-event windows ahead of time").
			MarkWrites()
		// And actually wire the trigger the comment above promises. It was
		// described and then not installed, so /admin/jobs and the ops API both
		// accepted a Run for this job and silently did nothing — the operator
		// loop the comment defends ("create an event, see its windows") was
		// exactly as broken as having no button, with the added cost that the
		// button looked like it worked. Background context, not the request's:
		// an operator's click must not be cancelled when their page finishes.
		p.job.SetTriggerAsync(func() {
			if err := p.generate(context.Background()); err != nil {
				p.job.Log("generate (triggered): %v", err)
			}
		})
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.job == nil {
		return nil
	}
	go schedule.ServiceLoop(ctx, p.job,
		2*time.Minute,  // boot delay: let migrations and the pool settle
		30*time.Minute, // default interval; operator-overridable like any job
		func(ctx context.Context) {
			// ServiceLoop takes no error back, so the sink is here. A generator
			// that stopped silently would be discovered by a daily reset
			// quietly not existing one morning.
			if err := p.generate(ctx); err != nil {
				p.job.Log("generate: %v", err)
			}
		})
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// windowHorizon is how far ahead windows are kept.
//
// Long enough that a generator down for a weekend costs nothing, short enough
// that retuning an event's cron does not leave a year of stale windows to
// unpick — some of which consumers may already have keyed work on.
const windowHorizon = 45 * 24 * time.Hour

func (p *Plugin) generate(ctx context.Context) error {
	// Mark the run, which ServiceLoop deliberately does not do. Two things
	// depend on it: /admin/jobs shows run_count 0 and a zero last_run without
	// it, so a job ticking perfectly looks exactly like one nobody scheduled;
	// and waitForJobsToDrain on SIGTERM only waits for jobs marked Running.
	p.job.SetRunning()
	defer p.job.SetIdle(time.Time{})

	now := time.Now()
	evs, err := p.store.ListEvents(ctx)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}

	var total int
	for _, ev := range evs {
		if !ev.Enabled {
			continue
		}
		// Resume from where the last window ENDS rather than from now, or a
		// contiguous event grows a hole every time the generator runs late —
		// and a hole in a daily reset is a day the thing does not exist.
		from := now
		last, err := p.store.LastWindowEnd(ctx, ev.Slug)
		if err != nil {
			p.job.Log("event %q: last window: %v", ev.Slug, err)
			continue
		}
		if !last.IsZero() {
			from = last
		}
		// A perpetual window already covers everything ahead; regenerating it
		// would do nothing but re-derive a row the UNIQUE then discards.
		if last.After(now.Add(windowHorizon)) {
			continue
		}

		ws, err := GenerateWindows(ev, from, now.Add(windowHorizon))
		if err != nil {
			// One malformed event must not stop the others: a bad cron
			// expression should cost that event its windows, not the site its
			// daily reset.
			p.job.Log("event %q: %v", ev.Slug, err)
			continue
		}
		n, err := p.store.InsertWindows(ctx, ev.Slug, ws)
		if err != nil {
			return fmt.Errorf("insert windows for %q: %w", ev.Slug, err)
		}
		total += n
	}
	if total > 0 {
		p.job.Log("%d window(s) materialised across %d event(s)", total, len(evs))
	}
	return nil
}

var _ core.Plugin = (*Plugin)(nil)
