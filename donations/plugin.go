// Package donations is the donation system — the public donate
// page, BTCPay invoice creation + webhook, points-package claims,
// and the admin cost/points/log/wallet/package management. Clean
// leaf (no other surface reads the donation tables).
package donations

import (
	"context"
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("donations", func() core.Plugin { return &Plugin{} })
}

// Settings is the plugin's view of the host's site-settings store —
// the two methods it actually calls, satisfied structurally by the
// host's settings repository.
type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Deps are the host seams. The donation tables themselves are
// plugin-private — the plugin builds its own Store at Provision;
// only genuinely shared domains (settings, users, the donate
// toggle, page chrome) arrive here. SetDeps must be called before
// core.Boot; Provision fails loud otherwise.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The
	// plugin owns both pages' markup now (templates/, embedded), so
	// chrome crosses rather than a data map.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)
	// RenderError shows the host's error page. error.html is the
	// site-wide error surface (the global 404, the download-limit
	// page) and stays host-owned; donations only needs to reach it.
	// title crosses because the BTCPay-unconfigured 503 carries
	// custom copy; empty title lets the page pick its per-code
	// default.
	RenderError func(c *gin.Context, code int, title, msg string)
	// CSRFToken feeds the pages' POST forms — minted by host
	// middleware, so only the host can answer it.
	CSRFToken func(c *gin.Context) string
	// RelativeTime is the site's time wording ("2 hours ago").
	RelativeTime func(v any) string

	// SiteName is what this deployment calls itself.
	//
	// It exists because this page said "ameNZB" five times in copy a visitor
	// reads — the name of the site this plugin was lifted out of — on every
	// host that installed it. A plugin cannot know the name and must not guess
	// it, and there is exactly one right answer per deployment, so the host
	// says. Same seam wiki added for the same reason.
	//
	// Optional: absent, the copy reads "this site", which is true everywhere.
	SiteName func() string

	// Settings is the shared site-settings store (donate_* keys,
	// BTCPay credentials, tip-jar goals).
	Settings Settings
	// IsDonateEnabled reports the master donate toggle — drives the
	// public route's 404 gate for non-admins.
	IsDonateEnabled func() bool
	// SetDonateEnabled flips the master toggle NOW (in-process state
	// + persisted setting in one call; no restart).
	SetDonateEnabled func(ctx context.Context, enabled bool) error
	// LookupUsername resolves a donor user id to a username for the
	// admin log view. ok=false when the user doesn't exist.
	LookupUsername func(ctx context.Context, userID int) (string, bool)
	// LookupUserID resolves a username (case-insensitive on the
	// ameNZB host) to a user id for manual-donation attribution.
	LookupUserID func(ctx context.Context, username string) (int, bool)
}

var deps *Deps

// SetDeps hands the plugin its host seams. Call from main() before
// core.Boot.
func SetDeps(d Deps) { deps = &d }

// renderContractOK reports whether Deps carries a complete render contract:
// fragments plus the host's chrome and error seams. Half of one is not a
// contract — a host that wired some seams would serve some pages and blank
// others, which reads as a broken site rather than a missing call.
func (d *Deps) renderContractOK() bool {
	modern := d.RenderPage != nil && d.RenderError != nil &&
		d.CSRFToken != nil && d.RelativeTime != nil
	return modern
}

// siteName resolves the deployment's name, or a neutral stand-in.
//
// The stand-in is a phrase rather than an empty string because every use here
// is mid-sentence: "keeping  fast, secure, and online" is worse than a generic
// noun, and worse than saying nothing at all would have been.
func siteName() string {
	if deps == nil || deps.SiteName == nil {
		return "this site"
	}
	if n := strings.TrimSpace(deps.SiteName()); n != "" {
		return n
	}
	return "this site"
}

// siteNameCap is siteName for the start of a sentence.
//
// Two functions rather than one piped through a title-caser: a caser would
// turn a real name like "ameNZB" into "Amenzb", which is worse than the
// problem it solves.
func siteNameCap() string {
	if n := siteName(); n != "this site" {
		return n
	}
	return "This site"
}

// Handlers serves the donation surfaces.
type Handlers struct {
	deps  Deps
	store Store
	// core is the mediator, for announcing settled donations. Nil in tests.
	core *core.Core
	auth core.AuthService
	errs core.ErrorReporter
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "donations",
		Version:     "1.0.0",
		Description: "Donations — BTCPay invoices + webhook, points packages, admin cost/log management.",
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps == nil {
		return fmt.Errorf("donations: SetDeps was not called before core.Boot — wire it in main()")
	}
	// Data seams, checked separately from the render ones so the error can
	// say which half is missing.
	if deps.Settings == nil || deps.IsDonateEnabled == nil ||
		deps.SetDonateEnabled == nil || deps.LookupUsername == nil || deps.LookupUserID == nil {
		return fmt.Errorf("donations: Deps missing a required field")
	}
	if !deps.renderContractOK() {
		return fmt.Errorf("donations: SetDeps not called, or a render seam is missing — " +
			"wire RenderPage, RenderError, CSRFToken and RelativeTime in main() before core.Boot")
	}
	// Parsed here, not at package init: the FuncMap binds deps.RelativeTime.
	// A parse failure fails boot rather than the first page view. Skipped on
	// the legacy contract, where the host renders its own copies by name.
	if deps.RenderPage != nil {
		if err := parseTemplates(); err != nil {
			return err
		}
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("donations: Core.Storage.DB() is nil")
	}
	p.handlers = &Handlers{deps: *deps, store: NewPGStore(db), auth: c.Auth, errs: c.Errors}
	p.handlers.WithCore(c)
	if err := declareEvents(c); err != nil {
		return err
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("donations: Core.Router.Engine() is nil")
	}

	// BTCPay webhook — public, HMAC-verified inside the handler
	// (BTCPay has no session; CSRF skips /api/*).
	engine.POST("/api/btcpay/webhook", p.handlers.BTCPayWebhook)

	pub := engine.Group("/")
	pub.Use(c.Auth.Authenticate()...)
	pub.GET("/help/donate", p.handlers.DonatePage)
	pub.POST("/donate/claim-package/:id", p.handlers.ClaimPackage)

	// Mod-editable surface: the unified page, cost/points/package/
	// tip-jar management. These shape the public page but touch no
	// credentials and can't move money.
	adm := engine.Group("/admin/donate")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminDonatePage)
	adm.GET("/costs", p.handlers.DonateCostsPage)
	adm.POST("/costs", p.handlers.SaveDonateCost)
	adm.POST("/costs/:id/del", p.handlers.DeleteDonateCost)
	adm.GET("/points", p.handlers.DonatePointsPage)
	adm.POST("/points", p.handlers.SaveDonatePoints)
	adm.GET("/log", p.handlers.DonateLogPage)
	adm.POST("/tipjar", p.handlers.SaveDonateTipJar)
	adm.POST("/packages", p.handlers.SaveDonatePackage)
	adm.POST("/packages/:id/del", p.handlers.DeleteDonatePackage)

	// The link, registered beside the routes it points at so the two stay in
	// step. These pages are a route GROUP rather than a SlotAdminPage view —
	// the slot mounts one GET and these have several — so without this they
	// are served and in no nav at all, findable only by knowing the URL.
	if err := pluginapi.RegisterAdminNav(c, "donations", func() []pluginapi.AdminNavEntry {
		return []pluginapi.AdminNavEntry{{Href: "/admin/donate", Label: "Donations", Group: "Community", Weight: 10}}
	}); err != nil {
		return fmt.Errorf("donations: register admin nav: %w", err)
	}

	// Admin-only writes: the three credential/money vectors. Saving a
	// BTCPay base URL + running the health check together would
	// exfiltrate the stored API key to an attacker-controlled host;
	// manual log entry credits lifetime donation totals + the Donator
	// flag. A mod must not be able to do either — the page above stays
	// mod-viewable (secrets render presence-only), only these writes
	// require admin.
	admHi := engine.Group("/admin/donate")
	admHi.Use(c.Auth.RequireUser(core.RoleAdmin)...)
	admHi.POST("/wallet", p.handlers.SaveDonateWallet)
	admHi.POST("/btcpay-health", p.handlers.BTCPayHealthCheck)
	admHi.POST("/log", p.handlers.SaveDonateManual)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
