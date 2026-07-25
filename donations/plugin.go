// Package donations is the donation system — the public donate
// page, BTCPay invoice creation + webhook, points-package claims,
// and the admin cost/points/log/wallet/package management. Clean
// leaf (no other surface reads the donation tables).
package donations

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
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
	// BaseData merges the host's page chrome (user, nav, CSRF, ...)
	// into a template data map — every page render goes through it.
	BaseData func(c *gin.Context, extra gin.H) gin.H
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

// Handlers serves the donation surfaces.
type Handlers struct {
	deps  Deps
	store Store
	auth  core.AuthService
	errs  core.ErrorReporter
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "donations",
		Version:     "1.0.0",
		Description: "Donations — BTCPay invoices + webhook, points packages, admin cost/log management.",
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps == nil {
		return fmt.Errorf("donations: SetDeps was not called before core.Boot — wire it in main()")
	}
	if deps.BaseData == nil || deps.Settings == nil || deps.IsDonateEnabled == nil ||
		deps.SetDonateEnabled == nil || deps.LookupUsername == nil || deps.LookupUserID == nil {
		return fmt.Errorf("donations: Deps missing a required field")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("donations: Core.Storage.DB() is nil")
	}
	p.handlers = &Handlers{deps: *deps, store: NewPGStore(db), auth: c.Auth, errs: c.Errors}

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

	adm := engine.Group("/admin/donate")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.AdminDonatePage)
	adm.GET("/costs", p.handlers.DonateCostsPage)
	adm.POST("/costs", p.handlers.SaveDonateCost)
	adm.POST("/costs/:id/del", p.handlers.DeleteDonateCost)
	adm.GET("/points", p.handlers.DonatePointsPage)
	adm.POST("/points", p.handlers.SaveDonatePoints)
	adm.GET("/log", p.handlers.DonateLogPage)
	adm.POST("/log", p.handlers.SaveDonateManual)
	adm.POST("/wallet", p.handlers.SaveDonateWallet)
	adm.POST("/btcpay-health", p.handlers.BTCPayHealthCheck)
	adm.POST("/tipjar", p.handlers.SaveDonateTipJar)
	adm.POST("/packages", p.handlers.SaveDonatePackage)
	adm.POST("/packages/:id/del", p.handlers.DeleteDonatePackage)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
