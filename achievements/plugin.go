// Package achievements is a loon plugin: earnable badges, described as DATA.
//
// An achievement is a criterion — "reach N of X" scored on a host counter or
// a countable event, or "the moment Y happens" on a declared event's trigger —
// plus a look, plus an OPTIONAL reward. The reward is a one_off in the
// REWARDS plugin, named by slug and paid through
// pluginapi.RewardBySlugGranter; an achievement with no reward_slug is a pure
// badge, which is a legitimate achievement and the reason this plugin exists
// apart from rewards at all.
//
// The two used to share a schema so a completion and its reward grant could
// land in one transaction. Once the reward became optional that transaction
// stopped being the spine: a pure badge has nothing to be atomic with, and a
// paid one crosses a plugin boundary where no shared transaction can exist.
// The replacement is IDEMPOTENCE — the completion commits as this plugin's
// own atomic fact, payment is an at-least-once call to an idempotent granter,
// and the scoring job repairs the crash window between the two (see
// subscribe.go and jobs.go).
package achievements

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

func init() {
	core.RegisterPlugin("achievements", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core  *core.Core
	store Store
	// admin is the definition CRUD, PG-only: there is no admin surface to
	// serve from the memory store, and the actions refuse politely without
	// one rather than 500ing.
	admin *PGStore
	job   core.Job
	tmpl  *template.Template

	// Achievement metric counters the host published, by metric name.
	// Snapshotted at Provision because the registry is written during boot
	// and read on a worker tick; re-looking-up per tick would race a plugin
	// still booting. The host side of this is documented as BEFORE-Boot: a
	// source registered after Boot is silently never seen.
	metrics map[string]MetricSource

	// files stores badge images; nil when no host registered
	// achievements.files, which hides the upload control rather than
	// rendering it broken.
	files blob.Store
	// iconOptions is the host's sprite vocabulary (achievements.icons), for
	// the definition form's default-icon picker. Empty means free text.
	iconOptions []string

	// granter pays achievements through the rewards plugin. Looked up in
	// Start rather than Provision — Boot runs every plugin's Provision before
	// any Start, so this sees rewards' registration whatever the boot order,
	// without making rewards a hard Requires (a host running pure badges
	// needs no rewards plugin at all). nil means unpaid completions stay
	// pending and the scoring job says so.
	granter pluginapi.RewardBySlugGranter
}

var _ core.Plugin = (*Plugin)(nil)

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "achievements",
		Version:     "0.1.0",
		Description: "Earnable badges: thresholds on site counters, event triggers, and an optional reward paid through the rewards plugin.",
		Migrations:  migrations,
		// Both legs, mirroring rewards (which also declares web+worker) and
		// for the same reason: web serves the admin page, the profile card
		// and the wiki block, and completes achievements from events emitted
		// on web requests; worker runs the hourly scoring job and completes
		// from events emitted by background work. Event subscription has to
		// live wherever events fire, and they fire on both.
		Processes: []string{"web", "worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	// The SchemaDB itself, NOT .DB(): unwrapping loses the search_path
	// scoping every unqualified table name in this plugin depends on.
	pg := NewPGStore(c.Storage.SchemaDB("achievements"))
	p.store, p.admin = pg, pg

	// The callback convention this plugin documents. Registered so the
	// extension directory can list it; read by a test — see docExtensions.
	for _, d := range docExtensions() {
		_ = c.RegisterDef(d.def, d.value)
	}

	// Achievement metrics, discovered by prefix rather than declared, so a
	// new achievement is a row plus one host registration and no change
	// here. A metric with no source is INERT rather than an error — it
	// simply never moves; a source with no achievement is one read the job
	// skips (scoreMetric reads the defs first).
	p.metrics = map[string]MetricSource{}
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, MetricSourcePrefix) {
			continue
		}
		v, _ := c.Lookup(name)
		src, ok := v.(MetricSource)
		if !ok {
			// Right key, wrong shape: the achievement would silently never
			// progress, which is precisely the failure worth refusing boot
			// over.
			return fmt.Errorf("achievements: extension %q is %T, want achievements.MetricSource", name, v)
		}
		p.metrics[strings.TrimPrefix(name, MetricSourcePrefix)] = src
	}

	// The two optional host extras for the definition page. Soft on purpose:
	// a host without uploads still defines achievements, it just cannot give
	// them images.
	if v, ok := c.Lookup("achievements.files"); ok {
		fs, ok := v.(blob.Store)
		if !ok {
			return fmt.Errorf("achievements: %q is %T, want blob.Store", "achievements.files", v)
		}
		p.files = fs
	}
	if v, ok := c.Lookup("achievements.icons"); ok {
		if icons, ok := v.([]string); ok {
			p.iconOptions = icons
		}
	}

	// What this plugin says and offers.
	if err := p.declareEvents(c); err != nil {
		return err
	}
	if err := p.registerList(c); err != nil {
		return err
	}
	if err := p.registerGranter(c); err != nil {
		return err
	}

	// Listen to the site. Same caveat rewards documents: Boot provisions in
	// dependency order and there is no post-Boot hook, so an emitter that
	// declares AFTER this plugin provisions is not seen here. The directory
	// shows anything missed as an event with no listener.
	p.subscribe(c)

	// The scoring job belongs to the worker leg ("all" counts as the worker:
	// on a single-process deployment it IS the worker, and gating on
	// "worker" alone is exactly how rewards once shipped a maintenance half
	// that silently did not exist on the simplest way to run the site).
	// Registered here rather than in Start because core.Scheduler warns that
	// Start-time registration races the admin view's registry snapshot.
	if c.Process == "worker" || c.Process == "all" {
		p.registerScoringJob(c.Scheduler)
	}

	// Everything below is UI. The worker has no router, and registering a
	// view there would be a boot error rather than a harmless no-op.
	if c.Process == "worker" {
		return nil
	}
	if err := p.parseTemplates(); err != nil {
		return err
	}
	// The profile card. Order against registerViews (which registers the
	// admin page) does not matter: registration stores a Render closure, and
	// nothing renders until a request arrives long after Boot.
	if err := p.registerProfileWidget(c); err != nil {
		return err
	}
	// The public catalogue, as a content block the wiki can embed.
	// Registered rather than routed: the page that shows it is
	// editor-authored, so the plugin supplies the table and the site
	// supplies the words around it.
	if err := p.registerBlock(c); err != nil {
		return err
	}
	return p.registerViews(c)
}

// Start looks up the payer and launches the scoring loop.
//
// The granter lookup lives here, not in Provision: Boot calls every plugin's
// Provision before any Start, so by now rewards has registered (or genuinely
// is not installed) regardless of provisioning order — which is what lets the
// dependency stay SOFT instead of a Requires edge a badge-only host cannot
// satisfy.
func (p *Plugin) Start(ctx context.Context) error {
	if v, ok := p.core.Lookup(pluginapi.RewardBySlugGranterName); ok {
		g, ok := v.(pluginapi.RewardBySlugGranter)
		if !ok {
			// Registered under the right key with the wrong shape is a
			// wiring bug wearing the same face as "not registered". Booting
			// anyway would mean every paid achievement silently staying
			// pending.
			return fmt.Errorf("achievements: extension %q is %T, want pluginapi.RewardBySlugGranter",
				pluginapi.RewardBySlugGranterName, v)
		}
		p.granter = g
	} else {
		// Degrade, out loud, exactly once. Pure badges are unaffected; an
		// achievement naming a reward_slug completes and sits pending, and
		// the scoring job repeats this message with a count whenever it has
		// unpaid rows it cannot pay.
		log.Printf("achievements: %q is not registered — achievements with a reward_slug will "+
			"complete but cannot be paid until the rewards plugin provides it",
			pluginapi.RewardBySlugGranterName)
	}

	if p.core.Process != "worker" && p.core.Process != "all" {
		return nil
	}
	p.core.Scheduler.RunLoop(ctx, p.job,
		2*time.Minute, // boot delay: let migrations and the pool settle
		scoringInterval, p.runScoring)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
