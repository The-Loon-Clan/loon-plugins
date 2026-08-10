package seedlock

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/tracker"
	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("seedlock", func() core.Plugin { return &Plugin{} })
}

// ExtensionName is the registry key this plugin publishes itself under, so the
// host can offer a member the clear action.
const ExtensionName = "seedlock"

type Plugin struct {
	core *core.Core
	cfg  Policy
	st   Store
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "seedlock",
		Version:     "0.1.0",
		Description: "One host per torrent per member: claims, a lock window, and a clear action.",
		// No migrations: a claim lives in Redis with a TTL, so there is no
		// table and nothing to sweep.
		//
		// web AND api, because announce is served by both — a rule installed on
		// one process only is a rule a member can walk around by pointing their
		// second client at the other.
		Processes: []string{"web", "api"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	p.cfg = DefaultPolicy()
	if err := c.Config.PluginInto("seedlock", &p.cfg); err != nil {
		return err
	}
	p.cfg = p.cfg.normalise()

	// Redis is the ONE optional core service, and this plugin cannot work
	// without it: an in-memory claim would be per-process, and announce runs on
	// two. Refusing to arm is the honest failure — a lock that only half
	// applies is worse than none, because it punishes the members whose
	// announces happen to land on the process that has the claim.
	if !p.cfg.Enabled {
		log.Printf("seedlock: disabled (plugins.seedlock.enabled) — every announce is admitted")
		return nil
	}
	if c.Redis == nil {
		log.Printf("seedlock: WARNING enabled but core.Redis is absent — the lock is NOT armed. " +
			"A claim must be shared across the web and api processes; set REDIS_ADDR.")
		return nil
	}
	p.st = NewRedisStore(c.Redis.Client())

	if err := c.Register(ExtensionName, p); err != nil {
		return err
	}
	// The member page the refusal message points at. Mounted only where a
	// human can see it, and only when the host wired a renderer — a rule that
	// tells somebody to "clear the lock on the site" while offering no such
	// page sends them looking for something that is not there.
	if c.Process != "api" {
		if pageReady() {
			tmpl, err := template.ParseFS(tmplFS, "templates/*.html")
			if err != nil {
				return fmt.Errorf("seedlock: parsing templates: %w", err)
			}
			var db *sqlx.DB
			if c.Storage != nil {
				db = c.Storage.DB()
			}
			h := NewHandlers(p, c.Auth, db)
			h.SetTemplates(tmpl)
			if c.Router != nil {
				g := c.Router.Engine().Group("/seedlock")
				g.Use(c.Auth.RequireUser(core.RoleUser)...)
				g.GET("", h.ClaimsPage)
				g.POST("/clear", h.ClearAction)
			}
		} else {
			log.Printf("seedlock: WARNING no RenderPage seam wired — /seedlock is NOT mounted, " +
				"but the refusal a client shows still tells members to clear the lock there")
		}
	}

	// The placeable widget (widgets.go). Registered whatever Enabled says: a
	// claim is a Redis TTL and outlives the switch, so a member has to be able
	// to see and clear what is already held.
	p.registerWidgets(c)

	tracker.SetAnnounceGuard(p.admit)
	log.Printf("seedlock: ARMED — one host per torrent, claim lapses %s after the last announce, identified by %s",
		p.cfg.LockWindow(), p.cfg.IdentifyBy)
	return nil
}

func (p *Plugin) Start(context.Context) error { return nil }

func (p *Plugin) Stop(context.Context) error {
	// Leave the tracker as it was found, or a stopped plugin keeps refusing
	// announces against claims nothing maintains.
	tracker.SetAnnounceGuard(nil)
	return nil
}

// admit is the tracker's admission seam.
//
// Ordered so the common case is cheapest: an announce from the host that
// already holds the claim does one Redis round trip and returns.
func (p *Plugin) admit(ctx context.Context, in tracker.GuardRequest) (bool, string) {
	if p.st == nil {
		return true, ""
	}
	host := p.cfg.Host(in.IP, in.PeerID)
	if host == "" {
		// Nothing to identify them by. Allowing is the safe direction: a member
		// behind something that hides their address should not be locked out of
		// their own torrent by a rule that cannot see them.
		return true, ""
	}

	// Stopping releases, whoever it came from. A member who stops on one host
	// must be able to start on another, and the losing host's "stopped" is not
	// another attempt to seed.
	if in.Event == "stopped" {
		if held, err := p.st.Held(ctx, in.UserID, in.InfoHash); err == nil && held.Host == host {
			_ = p.st.Release(ctx, in.UserID, in.InfoHash)
		}
		return true, ""
	}

	held, err := p.st.Acquire(ctx, in.UserID, in.InfoHash, host, p.cfg.LockWindow())
	if err != nil {
		// Redis is unreachable. Allow: a tracker that refuses every announce
		// because its cache is down has turned an outage into a site-wide ban.
		if p.core.Errors != nil {
			p.core.Errors.Report(ctx, "seedlock/acquire", err)
		}
		return true, ""
	}
	if held.Host == "" {
		return true, "" // just claimed it
	}
	verdict, reason := Decide(p.cfg, held, host, in.Event, time.Now())
	switch verdict {
	case Allow:
		// This host holds it: push the window out so it survives while they
		// keep seeding.
		_ = p.st.Refresh(ctx, in.UserID, in.InfoHash, p.cfg.LockWindow())
		return true, ""
	case Release:
		_ = p.st.Release(ctx, in.UserID, in.InfoHash)
		return true, ""
	default:
		return false, reason
	}
}

// ── The capability the host uses ────────────────────────────────────────────

// Claims lists a member's live locks, for their own page.
func (p *Plugin) Claims(ctx context.Context, userID int64) (map[string]Claim, error) {
	if p.st == nil {
		return map[string]Claim{}, nil
	}
	return p.st.HeldBy(ctx, userID)
}

// ClearClaim releases a member's lock on one torrent.
//
// The escape hatch that makes the rule safe to switch on. A member who
// genuinely moved machines, or whose address changed under them, can take their
// own torrent back without waiting out the window or asking staff — and because
// it only ever clears their OWN claim, it cannot be used to take somebody
// else's.
func (p *Plugin) ClearClaim(ctx context.Context, userID int64, infoHash string) error {
	if p.st == nil {
		return nil
	}
	return p.st.Release(ctx, userID, infoHash)
}
