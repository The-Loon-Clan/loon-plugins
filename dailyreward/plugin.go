// Package dailyreward is a loon plugin: a home-page card where a signed-in user
// claims a daily reward — once per day, behind a captcha — for points and a
// growing streak. It shows off composing several core seams at once: the view
// system (a SlotSiteWidget card), a plugin route for the claim POST, the points
// ledger (core.Points), schema-scoped storage, and the host's CAPTCHA exposed
// as a cross-cutting capability.
//
// The captcha is looked up structurally off the extension registry (key
// "captcha"), so this plugin never imports the host's captcha package.
//
// Two cases that look identical from here and are not: a host that registered
// NO captcha has chosen not to gate, and the claim runs ungated (safe enough
// behind auth + once-per-day; logged at boot so it is a choice rather than a
// surprise). A host that registered one we cannot use has a wiring bug, and
// booting anyway would serve an ungated points endpoint while looking healthy —
// so that fails Provision.
package dailyreward

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

// captchaExtension is the registry key the host publishes its captcha under.
// Kept in sync by convention with the host (loon-baseline captcha).
const captchaExtension = "captcha"

func init() {
	core.RegisterPlugin("dailyreward", func() core.Plugin { return &Plugin{} })
}

// captchaCap is the structural view of the host captcha capability — stdlib
// types only, so the plugin needn't import the host's captcha package.
type captchaCap interface {
	Verify(ctx context.Context, token, ip string) error
	WidgetHTML() template.HTML
}

type Plugin struct {
	core    *core.Core
	st      Store
	captcha captchaCap // nil when the host registered none — claim runs ungated
	tmpl    *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "dailyreward",
		Version:     "0.1.0",
		Description: "Daily-login reward: claim once a day (behind a captcha) for points + a streak.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	p.st = NewPGStore(c.Storage.SchemaDB("dailyreward"))

	// A host that registered a captcha and had it silently dropped would serve
	// an UNGATED points-granting POST, with no error anywhere to say so. That
	// is the difference between "this host chose not to gate" (fine, below) and
	// "this host tried to gate and we lost it" (a wiring bug wearing the same
	// face). `p.captcha, _ = v.(captchaCap)` could not tell them apart.
	//
	// loon/core says so directly: "A failed type assertion is a programmer
	// error the consumer should surface from Provision (aborting boot), not
	// swallow." plugins/store does exactly that for RankGranter. So does this.
	if v, ok := c.Lookup(captchaExtension); ok {
		cap, ok := v.(captchaCap)
		if !ok {
			return errors.New(formatCaptchaMismatch(captchaExtension, v))
		}
		p.captcha = cap
	}
	// No captcha registered at all is a deliberate host choice — the claim runs
	// ungated, which is safe enough behind auth + once-per-day. Say so out loud
	// anyway: an operator who MEANT to wire one should not have to read this
	// file to discover they didn't.
	if p.captcha == nil {
		log.Printf("dailyreward: WARNING no %q extension registered — the daily claim POST will run UNGATED",
			captchaExtension)
	}

	t, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t

	// SlotSiteWidget ignores Actions, so the claim POST is a plugin route
	// (/plugin/dailyreward/claim) that inherits the host middleware stack.
	c.Router.Mount("dailyreward").POST("/claim", p.claim)

	// Per-user claim state, for hosts that want a compact control of their own
	// rather than (or as well as) the card below. Registered before the views
	// so a host looking it up cannot race the widget's own registration.
	c.Register(StatusExtension, StatusFunc(p.status))

	// Announce the claim so achievements and stats can react without this
	// plugin knowing they exist. A duplicate declaration is a wiring bug worth
	// failing Provision for, not a warning.
	if err := declareEvents(c); err != nil {
		return err
	}

	if err := c.RegisterView(core.View{
		Slug: "daily-reward", Title: "Daily reward", Slot: core.SlotSiteWidget,
		MinRole: core.RoleUser, // logged-in only
		Render:  p.renderWidget,
	}); err != nil {
		return err
	}
	// A streak card on any user's public profile (SlotUserWidget) — rendered
	// for the profile SUBJECT via core.ViewSubject, showing that a plugin can
	// contribute to profiles it knows nothing about.
	return c.RegisterView(core.View{
		Slug: "daily-streak", Title: "Daily streak", Slot: core.SlotUserWidget,
		Public: true,
		Render: p.renderProfileStreak,
	})
}

// formatCaptchaMismatch builds the boot error for a captcha registered under
// the right key with the wrong shape. Split out so a test can assert the
// message stays actionable: it must name the key, the offending type, and what
// swallowing it would have cost.
func formatCaptchaMismatch(key string, v any) string {
	return fmt.Sprintf("dailyreward: extension %q is %T, which does not implement the captcha capability "+
		"(Verify(ctx, token, ip) error + WidgetHTML() template.HTML) — refusing to boot rather than serve "+
		"an ungated points endpoint", key, v)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
