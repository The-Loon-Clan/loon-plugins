// Package perks is the tracker economy: freeleech and double-upload tokens,
// bought with points and spent on one torrent at a time.
//
// It exists apart from the tracker on purpose. A private site's economy changes
// constantly — new perks, promotions, seasonal rates — and none of it is the
// announce protocol. The tracker exposes two numbers per (member, torrent) and
// this decides what they are; see tracker/multiplier.go.
//
// It also answers the hit-and-run framework: a site that told somebody a
// download was free has already said what that download owes, so a freeleech
// torrent is exempt from seeding requirements.
package perks

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/tracker"
	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("perks", func() core.Plugin { return &Plugin{} })
}

// Config is plugins.perks.*.
type Config struct {
	// TokenHours is how long a perk lasts once spent. Zero means forever.
	//
	// Default 168 — seven days, matching the hit-and-run seedtime requirement,
	// so a freeleech download is free for exactly as long as a member is
	// required to seed it. Two numbers that mean different things but should
	// move together; a site that changes one should look at the other.
	TokenHours int `json:"token_hours"`
}

// RefreshInterval is how often the in-memory perk table is reloaded.
//
// Thirty seconds. A member who spends a token expects it to work on their next
// announce, which is minutes away, so this is comfortably inside that — and
// Spend refreshes immediately anyway, so the timer only covers perks that
// expired and other processes' writes.
const RefreshInterval = 30 * time.Second

type Plugin struct {
	core  *core.Core
	cfg   Config
	st    Store
	table *Table
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "perks",
		Version:     "0.1.0",
		Description: "Tracker economy: freeleech and double-upload tokens, bought with points.",
		Migrations:  migrations,
		// web AND api: the announce endpoints are registered on both, and a
		// perk that applied on one process and not the other would make a
		// member's ratio depend on which one served them.
		Processes: []string{"web", "api"},
		// Freeleech and double-upload tokens. Both are discounts on TRANSFER,
		// and an indexer-only site has no transfer to discount — so on one
		// these are shop items that take points and grant nothing.
		Flavours: []string{core.FlavourTracker},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	p.cfg = Config{TokenHours: 168}
	if err := c.Config.PluginInto("perks", &p.cfg); err != nil {
		return fmt.Errorf("perks: reading config: %w", err)
	}
	p.st = NewPGStore(c.Storage.SchemaDB("perks"))
	p.table = NewTable()

	// The seam the tracker consults on every announce. Registered here rather
	// than by the host so that installing this plugin IS the wiring — a host
	// that compiles it in and forgets a line would otherwise sell tokens that
	// never take effect.
	// The placeable widget (widgets.go). It reads the same Table the
	// multiplier below consults, so a member is shown the perks actually being
	// applied to their traffic rather than rows that say they should be.
	p.registerWidgets(c)

	tracker.SetMultiplier(func(_ context.Context, userID int64, infoHash string) (float64, float64) {
		return p.table.Factors(userID, infoHash, time.Now())
	})
	// Publish the freeleech answer for anything that needs it — the
	// hit-and-run framework asks whether a snatch was free. Registered under a
	// plain name and satisfied structurally, so a consumer needs no import of
	// this package.
	if err := c.Register(ExtensionName, p); err != nil {
		return fmt.Errorf("perks: registering %q: %w", ExtensionName, err)
	}
	// And the granter, under the shared pluginapi name, so the store can sell
	// tokens without importing this package.
	if err := c.Register(pluginapi.PerkGranterName, p); err != nil {
		return fmt.Errorf("perks: registering %q: %w", pluginapi.PerkGranterName, err)
	}
	// The wallet page, when the host wired a renderer. Web only — the api
	// process serves torrent clients, which have no wallet.
	if c.Process != "api" && pageReady() {
		tmpl, err := template.ParseFS(tmplFS, "templates/*.html")
		if err != nil {
			return fmt.Errorf("perks: parsing templates: %w", err)
		}
		h := NewHandlers(p, c.Auth)
		h.SetTemplates(tmpl)
		if c.Router != nil {
			g := c.Router.Engine().Group("/perks")
			g.Use(c.Auth.RequireUser(core.RoleUser)...)
			g.GET("", h.WalletPage)
			g.POST("/spend", h.SpendAction)
		}
	} else if c.Process != "api" {
		// Tokens can still be bought and will still apply; there is simply
		// nowhere to spend one. Say so, because that is a store selling
		// something a member cannot use.
		log.Printf("perks: no RenderPage seam wired — the wallet at /perks is NOT mounted, so tokens cannot be spent")
	}

	log.Printf("perks: tracker credit seam installed (tokens last %s)", p.tokenDuration())
	return nil
}

// ExtensionName is the registry key this plugin publishes itself under.
// Exported because a consumer needs the same string, and a literal in two
// repositories is how a capability ends up looked up under a name nobody
// registers.
const ExtensionName = "perks"

func (p *Plugin) tokenDuration() time.Duration {
	if p.cfg.TokenHours <= 0 {
		return 0 // forever
	}
	return time.Duration(p.cfg.TokenHours) * time.Hour
}

func (p *Plugin) Start(ctx context.Context) error {
	// Load once before serving, so the first announce after a restart is
	// credited correctly rather than at 1:1 for the first refresh interval.
	p.refresh(ctx)
	go func() {
		t := time.NewTicker(RefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.refresh(ctx)
			}
		}
	}()
	return nil
}

func (p *Plugin) Stop(context.Context) error {
	// Leave the tracker as it was found. A stopped plugin whose multiplier is
	// still installed would keep applying a table nothing refreshes.
	tracker.SetMultiplier(nil)
	return nil
}

func (p *Plugin) refresh(ctx context.Context) {
	all, err := p.st.ActivePerks(ctx, time.Now())
	if err != nil {
		// Keep the previous table rather than clearing it. A failed reload is
		// the database's problem, and dropping every perk on the site because
		// of one bad query would charge members for downloads they had paid to
		// make free.
		if p.core.Errors != nil {
			p.core.Errors.Report(ctx, "perks/refresh", err)
		}
		return
	}
	p.table.Replace(all)
	p.refreshSiteFreeleech(ctx)
}

// SiteFreeleechEvent is the scheduled-event slug that makes the whole site
// free while one of its windows is open.
//
// A slug and not a config toggle: the operator defines the event once — name,
// description, whether it is enabled — and anything may open a window on it.
// The fundraising goal is the intended trigger, but an operator throwing the
// switch by hand is the same mechanism, which is what makes this worth having
// as a window rather than as a boolean somebody has to remember to turn off.
const SiteFreeleechEvent = "site-freeleech"

// refreshSiteFreeleech reads the window on the REFRESH timer, never on the
// announce path.
//
// Announce is the hottest path the tracker has — every peer, every interval —
// and this answers a question whose answer changes about twice a year. A lookup
// there would be a cross-plugin query per peer to be told "no" almost every
// time. Thirty seconds of staleness at the edges of a week-long window is the
// trade, and it is the same one the perk table itself already makes.
//
// A host with no events plugin gets the zero time, which is exactly what "no
// window is open" is — so absence degrades to off rather than to an error.
func (p *Plugin) refreshSiteFreeleech(ctx context.Context) {
	v, ok := p.core.Lookup(pluginapi.ScheduledEventsName)
	if !ok {
		return
	}
	ev, ok := v.(pluginapi.ScheduledEvents)
	if !ok {
		return
	}
	open, err := ev.OpenWindows(ctx, []string{SiteFreeleechEvent})
	if err != nil {
		// Keep whatever was last read, for the reason Replace above keeps the
		// old table: a failed lookup is the events plugin's problem, and
		// switching the site off freeleech mid-window because one query
		// hiccuped would charge members for downloads the site had announced
		// as free.
		if p.core.Errors != nil {
			p.core.Errors.Report(ctx, "perks/site-freeleech", err)
		}
		return
	}
	// Absent from the map means no window is open, which must CLEAR the state
	// rather than leave it — otherwise a closed window stays in force until the
	// process restarts, and the site is permanently free.
	p.table.SetSiteFreeleech(open[SiteFreeleechEvent].Ends)
}

// ── The capability other plugins and the host use ───────────────────────────

// Grant mints a token. The store plugin calls this when one is bought.
func (p *Plugin) Grant(ctx context.Context, userID int64, kind Kind) error {
	return p.st.Grant(ctx, userID, kind)
}

// Spend applies a held token to a torrent and takes effect immediately.
func (p *Plugin) Spend(ctx context.Context, userID int64, kind Kind, infoHash string) error {
	if err := p.st.Spend(ctx, userID, kind, infoHash, p.tokenDuration()); err != nil {
		return err
	}
	// Refresh now rather than waiting for the ticker: a member who just spent a
	// token and watched their next announce still count against them would
	// reasonably conclude it did not work.
	p.refresh(ctx)
	return nil
}

// Unspent is a member's wallet.
func (p *Plugin) Unspent(ctx context.Context, userID int64) (map[Kind]int, error) {
	return p.st.Unspent(ctx, userID)
}

// GrantPerk implements pluginapi.PerkGranter: the store calls this when a
// member buys a token. Kind arrives as a plain string from an admin-editable
// store item, so it is validated rather than trusted.
func (p *Plugin) GrantPerk(ctx context.Context, userID int64, kind string) error {
	return p.st.Grant(ctx, userID, Kind(kind))
}

// HasFreeleech answers the hit-and-run framework.
func (p *Plugin) HasFreeleech(userID int64, infoHash string) bool {
	return p.table.HasFreeleech(userID, infoHash, time.Now())
}
