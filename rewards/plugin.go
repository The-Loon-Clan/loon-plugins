// Package rewards is a loon plugin: earning described as DATA rather than as
// one Go job per rule.
//
// A reward says what is earnable and on what terms; its payout lines say what
// it hands over; an event says when. The kind — one_off, recurring, per_unit —
// decides what "already paid" means, and a UNIQUE constraint enforces it, so a
// reward author never writes idempotency logic again. Three existing rules on
// the host each hand-rolled that differently; getting it wrong pays somebody
// twice.
//
// The plugin owns no member data and moves no points itself. Points leave
// through core.Points; every other payout kind (roles, medals, achievements,
// username effects) is a handler the host or a sibling plugin registers, so
// this package never learns what a medal is.
package rewards

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

//go:embed migrations/*.sql
var migrations embed.FS

// TriggerExtension is the registry key the engine is published under. A host
// login handler looks this up and calls Fire/Available; absent means the
// plugin is not installed, which is a supported configuration.
const TriggerExtension = "rewards.trigger"

// AdminExtension is the registry key the configuration store is published
// under. The host's ops API consumes it to serve /ops/rewards; unlike the
// trigger it is published on every process, because the ops listener runs
// everywhere and should answer wherever the plugin booted.
const AdminExtension = "rewards.admin"

// ValidatorExtension publishes the cross-table check, so the ops API and an
// agent can ask "is this configuration actually going to pay anyone" without
// re-deriving the rules on the other side of the wire.
const ValidatorExtension = "rewards.validator"

// Validator is the shape the host looks up.
type Validator interface {
	Validate(ctx context.Context) ([]Finding, error)
}

func init() {
	core.RegisterPlugin("rewards", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core   *core.Core
	store  Store
	admin  AdminStore
	engine *Engine
	job    *schedule.JobInfo
	tmpl   *template.Template

	// Per-unit counters the host published, by reward slug. Snapshotted at
	// Provision because the registry is written during boot and read on a
	// worker tick; re-looking-up per tick would race a plugin still booting.
	units map[string]UnitSource

	// Achievement metric counters the host published, by metric name.
	// Snapshotted for the same reason units are: the registry is written
	// during boot and read on a worker tick.
	metrics map[string]MetricSource
}

var _ core.Plugin = (*Plugin)(nil)

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "rewards",
		Version:     "0.1.0",
		Description: "Data-driven rewards: events, payout lines, and idempotent grants.",
		Migrations:  migrations,
		// Web resolves and settles claims; worker keeps event windows
		// materialised ahead of time and expires lapsed grants.
		Processes: []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	// The SchemaDB itself, NOT .DB(): unwrapping loses the search_path
	// scoping every unqualified table name in this plugin depends on.
	pg := NewPGStore(c.Storage.SchemaDB("rewards"))
	p.store, p.admin = pg, pg
	p.engine = NewEngine(p.store, log.Printf)

	// Points is the one payout kind this plugin implements itself, because it
	// is the one loon already has a ledger facade for. Without it every
	// points-paying reward would be refused at grant time — which is the
	// correct failure, but a boot-time error naming the cause is a great deal
	// more useful than a member wondering where their daily bonus went.
	if c.Points == nil {
		return errors.New("rewards: core.Points is not wired; every points payout would be refused at grant time")
	}
	p.engine.Handle(PayoutPoints, func(ctx context.Context, g Grant, payout Payout) error {
		// The detail is the reward's slug, so a ledger reader can tell a
		// daily-login credit from a summer bonus — 30,000 tenure points
		// labelled just "reward" is the first question in every balance
		// dispute. ref is the grant payout id: it makes every ledger row
		// traceable to the exact frozen line that produced it.
		_, err := c.Points.Award(ctx, g.UserID, payout.Amount, "reward", g.RewardSlug, payout.ID)
		return err
	})

	// Every other payout kind comes from outside. A host that registers none
	// can still run points-only rewards; one that references a medal without
	// registering a handler gets a refusal at grant time rather than a grant
	// that can never settle.
	for _, kind := range []PayoutKind{PayoutRole, PayoutMedal, PayoutAchievement, PayoutUsernameFX} {
		key := "rewards.payout." + string(kind)
		v, ok := c.Lookup(key)
		if !ok {
			continue
		}
		h, ok := v.(PayoutHandler)
		if !ok {
			// Registered under the right key with the wrong shape is a wiring
			// bug wearing the same face as "not registered". Booting anyway
			// would mean rewards silently refusing to grant.
			return fmt.Errorf("rewards: extension %q is %T, want rewards.PayoutHandler", key, v)
		}
		p.engine.Handle(kind, h)
	}

	// Per-unit counters. Discovered by suffix rather than declared, so adding a
	// per_unit reward is a reward row plus one host registration and no change
	// here. A source registered for a reward that does not exist is harmless —
	// GrantUnits looks the reward up and finds nothing.
	// The callback conventions this plugin documents. Registered here, and
	// read by a test — see docExtensions.
	for _, d := range docExtensions() {
		_ = c.RegisterDef(d.def, d.value)
	}

	p.units = map[string]UnitSource{}
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, UnitSourcePrefix) {
			continue
		}
		v, _ := c.Lookup(name)
		src, ok := v.(UnitSource)
		if !ok {
			// Right key, wrong shape: the reward would silently never pay,
			// which is precisely the failure this plugin exists to stop.
			return fmt.Errorf("rewards: extension %q is %T, want rewards.UnitSource", name, v)
		}
		p.units[strings.TrimPrefix(name, UnitSourcePrefix)] = src
	}

	// Achievement metrics, discovered the same way and for the same reason: a
	// new achievement is a row plus one host registration, not a change here.
	// A metric with no source is INERT rather than an error — the validator
	// reports it, so a half-deployed site says so on the admin page instead of
	// refusing to boot.
	// The catalogue is CONFIGURATION — the reward_sources table — and this is
	// only its seed. Written once into an empty table so the dropdowns are
	// never blank at the moment they matter most, and never again: a host
	// changing its seed must not overwrite what an operator edited, and
	// re-seeding every boot would resurrect rows they deliberately deleted.
	if v, ok := c.Lookup(SourceCatalogExtension); ok {
		seed, ok := v.(SourceCatalog)
		if !ok {
			return fmt.Errorf("rewards: extension %q is %T, want rewards.SourceCatalog", SourceCatalogExtension, v)
		}
		n, err := p.seedSources(context.Background(), seed)
		if err != nil {
			// Refuse at boot rather than offer a dropdown row that cannot
			// work. An operator picking it would configure something that
			// looks right and never fires.
			return fmt.Errorf("rewards: %w", err)
		}
		if n > 0 {
			// Said out loud: a silent seed is indistinguishable from a
			// migration that did not run.
			log.Printf("rewards: seeded %d source(s) into an empty catalogue", n)
		}
	}

	p.metrics = map[string]MetricSource{}
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, MetricSourcePrefix) {
			continue
		}
		v, _ := c.Lookup(name)
		src, ok := v.(MetricSource)
		if !ok {
			// Right key, wrong shape: the achievement would silently never
			// progress, which is the failure this plugin exists to stop.
			return fmt.Errorf("rewards: extension %q is %T, want rewards.MetricSource", name, v)
		}
		p.metrics[strings.TrimPrefix(name, MetricSourcePrefix)] = src
	}

	// Tell a member when something is waiting for them. Absent notifications
	// are fine: the grant is durable and the card shows it regardless, so this
	// is a nudge rather than the delivery mechanism.
	if c.Notifications != nil {
		p.engine.Notifier(func(ctx context.Context, userID int64, title, body, link string) {
			if err := c.Notifications.Notify(ctx, userID, core.Notification{
				Kind: "reward_claim", Title: title, Body: body, Link: link,
			}); err != nil {
				log.Printf("rewards: notify user %d: %v", userID, err)
			}
		})
	}

	// Described rather than bare, so /admin/plugins can answer "what is this
	// and am I meant to call it or supply it" without anyone reading this
	// file. The three below are all services -- the host calls them.
	_ = c.RegisterDef(core.ExtensionDef{
		Name:    TriggerExtension,
		Summary: "fire a surface's rewards for one member, and list what they could earn there",
		Kind:    core.ExtService, Stable: true,
	}, p.engine)
	_ = c.RegisterDef(core.ExtensionDef{
		Name:    AdminExtension,
		Summary: "read and write reward configuration; backs the ops API",
		Kind:    core.ExtService, Stable: true,
	}, p.admin)
	_ = c.RegisterDef(core.ExtensionDef{
		Name:    ValidatorExtension,
		Summary: "cross-check the whole configuration and report what cannot pay",
		Kind:    core.ExtService, Stable: true,
	}, Validator(p))
	// ...and a host achievements page reads member standing through this.
	if err := p.registerAchievements(c); err != nil {
		return err
	}

	// The admin page is web-only: the worker has no router, and registering a
	// view there would be a boot error rather than a harmless no-op.
	if c.Process == "worker" {
		return nil
	}
	// The member-facing claim card and its POST. Registered before the admin
	// pages so a failure here is a boot error rather than a half-wired plugin
	// that serves an admin surface for a delivery mode members cannot reach.
	if err := p.registerMemberViews(c); err != nil {
		return err
	}
	return p.registerViews(c)
}

// Start launches the window generator on the worker only.
//
// Web processes must not run it: several web containers generating the same
// windows would race, and while ON CONFLICT makes that harmless it is pointless
// contention on a table every login reads.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core.Process != "worker" {
		return nil
	}
	p.job = schedule.RegisterJob("Reward Windows", "Materialise event windows ahead and expire lapsed grants")
	// Triggerable, because the operator loop is "create an event, see its
	// windows". Without this the answer to "where are my windows" is "wait up
	// to thirty minutes", which reads as broken and is how someone concludes
	// the event is misconfigured when it is merely early.
	p.job.SetTrigger(func() {
		go func() {
			if err := p.maintain(context.Background()); err != nil {
				p.job.Log("manual run: %v", err)
			}
		}()
	})
	go schedule.ServiceLoop(ctx, p.job,
		2*time.Minute,  // boot delay: let migrations and the pool settle
		30*time.Minute, // default interval; operator-overridable like any job
		func(ctx context.Context) {
			// ServiceLoop takes no error back, so the sink is here: a
			// generator that stops silently would be discovered by a daily
			// reward quietly not existing one morning.
			if err := p.maintain(ctx); err != nil {
				p.job.Log("maintain: %v", err)
				log.Printf("rewards: maintain: %v", err)
			}
		})
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// windowHorizon is how far ahead windows are kept.
//
// Long enough that a generator down for a weekend costs nothing, short enough
// that retuning an event's cron does not leave a year of stale windows to
// unpick — some of which may already have grants keyed on them.
const windowHorizon = 45 * 24 * time.Hour

// expireBatch bounds one sweep. Lapsed grants are rare and the next tick picks
// up the remainder; an unbounded UPDATE on a growing table is how a
// maintenance job becomes an outage.
const expireBatch = 500

func (p *Plugin) maintain(ctx context.Context) error {
	// Mark the run, which ServiceLoop deliberately does not do for us. Two
	// things depend on it and both failed silently without it: /admin/jobs
	// showed run_count 0 and a zero last_run, so a job that was ticking
	// perfectly looked exactly like one nobody had scheduled; and
	// waitForJobsToDrain on SIGTERM only waits for jobs marked Running, so a
	// deploy could kill this mid-grant. The grant model resumes rather than
	// replays, so that was survivable -- but being unkillable-by-accident is
	// cheaper than relying on it.
	p.job.SetRunning()
	defer p.job.SetIdle(time.Time{})

	now := time.Now()
	events, err := p.store.EventsWithCron(ctx)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}

	var totalNew int
	for _, ev := range events {
		// Resume from where the last window ends rather than from now, or a
		// contiguous reset would grow a hole every time the generator runs
		// late — and a hole in a reset is a day the reward does not exist.
		from := now
		last, err := p.store.LastWindowEnd(ctx, ev.ID)
		if err != nil {
			p.job.Log("event %q: last window: %v", ev.Slug, err)
			continue
		}
		if !last.IsZero() {
			from = last
		}
		windows, err := GenerateWindows(ev, from, now.Add(windowHorizon))
		if err != nil {
			// One malformed event must not stop the others: a bad cron
			// expression should cost that event its windows, not the site its
			// daily reward.
			p.job.Log("event %q: %v", ev.Slug, err)
			continue
		}
		n, err := p.store.InsertWindows(ctx, windows)
		if err != nil {
			return fmt.Errorf("insert windows for %q: %w", ev.Slug, err)
		}
		totalNew += n
	}

	// Per-unit rewards: pay whatever each counter has moved by. Runs on the
	// same tick as window generation because both are "keep the world
	// consistent" work and neither is urgent — a tenure year is not less owed
	// for arriving twenty minutes late, which is the entire point of paying on
	// a high-water mark instead of on an anniversary DAY.
	for slug, src := range p.units {
		n, err := p.engine.GrantUnits(ctx, slug, src)
		if err != nil {
			p.job.Log("units %q: %v", slug, err)
			continue
		}
		if n > 0 {
			p.job.Log("%s: granted %d member(s)", slug, n)
		}
	}

	expired, err := p.store.ExpireGrants(ctx, now, expireBatch)
	if err != nil {
		return fmt.Errorf("expire grants: %w", err)
	}
	if totalNew > 0 || expired > 0 {
		p.job.Log("%d window(s) materialised, %d grant(s) expired", totalNew, expired)
	}
	return nil
}
