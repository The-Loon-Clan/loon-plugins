// Package hitrun is the hit-and-run framework: the rules that decide whether a
// member who stopped seeding is warned, and what a site does about it.
//
// It sits OVER the tracker plugin rather than inside it. The tracker keeps the
// accounting — uploaded, downloaded, seedtime, last announce — and this decides
// what that accounting means, which is a policy question every site answers
// differently. A host can run the tracker with no punishment system at all by
// simply not enabling this.
//
//	plugins.hitrun.enabled = true
//
// Defaults follow UNIT3D's config/hitrun.php, the nearest thing this space has
// to a standard, so an operator retunes from something rather than inventing a
// number. See policy.go for each setting and what it costs to get wrong.
//
// This plugin never disables anything itself. It cannot: the tracker owns the
// download path and belongs to another plugin, and reaching into it would put
// the rule in one file and its enforcement in another that never mentions it.
// The host supplies a Notifier and decides what losing privileges means.
package hitrun

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

const jobName = "Hit and Run Sweep"

func init() {
	core.RegisterPlugin("hitrun", func() core.Plugin { return &Plugin{} })
}

// Config is plugins.hitrun.* — the Policy, read straight from the host's
// config. One type rather than a config struct that copies into a policy
// struct, because two shapes for one set of numbers is how they drift.
type Config = Policy

type Plugin struct {
	core *core.Core
	cfg  Config
	st   Store
	job  core.Job
	ctx  context.Context
	h    *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "hitrun",
		Version:     "0.1.0",
		Description: "Hit-and-run rules over the tracker's accounting: pre-warnings, warnings, expiry.",
		Migrations:  migrations,
		// web only. The api process serves machine traffic and has no business
		// issuing punishments on a timer.
		Processes: []string{"web"},
	}
}

// SweepBatch is how many snatches one pass evaluates.
//
// The work is a query plus arithmetic — no network anywhere — so this is sized
// against the database rather than a rate limit. Large enough that a site of
// this size finishes in one pass; the sweep simply sees fewer rows if it is
// ever too small, which delays a warning rather than losing one.
const SweepBatch = 20000

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	p.cfg = DefaultPolicy()
	if err := c.Config.PluginInto("hitrun", &p.cfg); err != nil {
		return fmt.Errorf("hitrun: reading config: %w", err)
	}
	p.st = NewPGStore(c.Storage.SchemaDB("hitrun"))

	// Say out loud which way round it is. A rule system that silently does
	// nothing looks exactly like one that is working and finding no offenders,
	// and an operator should not have to read the database to tell them apart.
	if p.cfg.Enabled {
		log.Printf("hitrun: ENABLED — %s seedtime required, %d day grace, %d warnings then downloads are blocked",
			time.Duration(p.cfg.normalise().Seedtime)*time.Second,
			p.cfg.normalise().GraceDays, p.cfg.normalise().MaxWarnings)
	} else {
		log.Printf("hitrun: disabled (plugins.hitrun.enabled) — warnings still EXPIRE, none are issued")
	}

	// The placeable widget (widgets.go). Web process only, for the same reason
	// as the page below: the api process serves machines, and a widget is
	// chrome. Registered whatever cfg.Enabled says, because warnings already
	// issued still EXPIRE while the rules are off — a member with an
	// outstanding warning must still be able to see it.
	if c.Process != "api" {
		p.registerWidgets(c)
	}

	// The member page, when the host wired a renderer. Mounted only for the
	// web process — the api process serves machines, which have no use for a
	// page explaining what somebody owes.
	if c.Process != "api" && pageReady() {
		tmpl, err := template.ParseFS(tmplFS, "templates/*.html")
		if err != nil {
			return fmt.Errorf("hitrun: parsing templates: %w", err)
		}
		p.h = NewHandlers(p.st, c.Auth, p.Policy)
		p.h.SetTemplates(tmpl)
		if c.Router != nil {
			// Behind the host's auth chain: a member's own standing is nobody
			// else's business, and an anonymous visitor deserves a login prompt
			// rather than an empty page.
			g := c.Router.Engine().Group("/hitrun")
			g.Use(c.Auth.RequireUser(core.RoleUser)...)
			g.GET("", p.h.StandingPage)
			g.GET("/", p.h.StandingPage)
		}
	} else if c.Process != "api" {
		// A host that wired no renderer gets the rules and the enforcement but
		// no page. Say so: a member told their downloads are disabled, with
		// nowhere to see why, is the worst version of this feature.
		log.Printf("hitrun: no RenderPage seam wired — the member page at /hitrun is NOT mounted")
	}

	if c.Scheduler != nil {
		p.job = c.Scheduler.RegisterJob(jobName,
			"Evaluates the tracker's seeding record: pre-warns, warns, and expires old warnings")
		p.job.SetTrigger(func() { go p.runSweep(p.ctx) })
	}
	return nil
}

// SweepInterval is how often the rules are applied.
//
// Hourly. The thresholds are measured in days, so a finer cadence would only
// change which hour of the day somebody is warned, and a coarser one would
// leave a member unable to act on a notice they had not yet been sent.
const SweepInterval = time.Hour

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	if p.job == nil || p.core.Scheduler == nil {
		return nil
	}
	// Five minutes after boot rather than immediately: the tracker's announce
	// path needs to be up before its accounting is judged, and a restart should
	// not be the thing that decides somebody stopped seeding.
	p.core.Scheduler.RunLoop(ctx, p.job, 5*time.Minute, SweepInterval, p.runSweep)
	return nil
}

func (p *Plugin) Stop(context.Context) error { return nil }

func (p *Plugin) runSweep(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.job.SetRunning()
	res, err := Sweep(ctx, p.st, p.cfg, notifier(), SweepBatch, time.Now())
	if err != nil {
		p.job.SetError(err.Error())
		if p.core.Errors != nil {
			p.core.Errors.Report(ctx, "hitrun/sweep", err)
		}
		return
	}
	// Log the shape of the pass, not just a count. "considered 4,000" with
	// nothing else is what a broken rule and a well-behaved membership look
	// like from the outside.
	p.job.Log("considered %d, pre-warned %d, warned %d, expired %d, blocked %d",
		res.Considered, res.Prewarned, res.Warned, res.Expired, res.Blocked)
	p.job.SetIdle(time.Now().Add(SweepInterval))
}

// Store exposes the plugin's storage to the host, for the member page and for
// a moderator lifting a warning by hand.
func (p *Plugin) Store() Store { return p.st }

// Policy returns the live rules, so a host page can explain them in the same
// numbers the sweep uses rather than in a second copy that drifts.
func (p *Plugin) Policy() Policy { return p.cfg.normalise() }
