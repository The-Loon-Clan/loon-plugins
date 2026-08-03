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
	pg := NewPGStore(c.Storage.SchemaDB("rewards").DB())
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
	p.engine.Handle(PayoutPoints, func(ctx context.Context, userID int64, payout Payout) error {
		// The reason code is per reward, not per payout, so a ledger reader
		// can tell a daily-login credit from a summer bonus. ref is the grant
		// payout id: it makes every ledger row traceable to the exact frozen
		// line that produced it, which is the first question asked when a
		// balance is disputed.
		_, err := c.Points.Award(ctx, userID, payout.Amount, "reward", "", payout.ID)
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

	// The host's login path calls Fire and Available through this.
	c.Register(TriggerExtension, p.engine)
	// ...and the ops API reads and writes configuration through this.
	c.Register(AdminExtension, p.admin)
	c.Register(ValidatorExtension, Validator(p))

	// The admin page is web-only: the worker has no router, and registering a
	// view there would be a boot error rather than a harmless no-op.
	if c.Process == "worker" {
		return nil
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
