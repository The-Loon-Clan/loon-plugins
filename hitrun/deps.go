package hitrun

import (
	"context"
	"html/template"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// The host seams.
//
// A package-level Deps set before core.Boot, matching every other plugin here
// that needs the host to do something it cannot do itself.
//
// The important one is LimitReached. This plugin detects hit-and-runs; it does
// not punish them, because the punishment is the host's to define — revoke an
// entitlement, drop a rank, refuse downloads at the edge, all three. Putting
// that choice here would bake one site's policy into a framework, and putting
// it in the tracker would mean editing a plugin that has no idea this one
// exists.

// Deps is what the host supplies.
type Deps struct {
	// RenderPage wraps a rendered fragment in the site's layout. Required for
	// the member page; without it the page is not mounted at all, which is
	// better than serving one that looks signed-out.
	RenderPage func(c *gin.Context, title string, body template.HTML)

	// RelativeTime formats a timestamp the way the rest of the site does, so
	// "last seen" here does not drift from every other one.
	RelativeTime func(t time.Time) string

	// Prewarn is the courtesy notice — the one message that can still change
	// the outcome, so a host should make it say what to do.
	Prewarn func(ctx context.Context, userID int64, torrentName, reason string)

	// Warn is the punishment notice.
	Warn func(ctx context.Context, userID int64, torrentName, reason string)

	// LimitReached fires when a member's active warnings hit the maximum. See
	// the note above: this plugin does not disable anything itself.
	LimitReached func(ctx context.Context, userID int64, activeWarnings int)

	// Exempt reports whether a snatch should be excused entirely.
	//
	// The site's answer to "does this one count", asked per (member, torrent).
	// A freeleech token is the obvious case: a site that told somebody a
	// download was free has already made its statement about what that
	// download owes, and then warning them for not seeding it would be the
	// site contradicting itself.
	//
	// A seam rather than a dependency on the perks plugin, because exemption
	// is a site's judgement — a host may exempt staff, a launch window, or a
	// torrent it knows is unseedable, and none of that is this plugin's
	// business. Unset means nothing is exempt.
	Exempt func(ctx context.Context, userID int64, infoHash string) bool
}

// Notifier is the message-sending subset, which is all the sweep needs.
type Notifier struct {
	Prewarn      func(ctx context.Context, userID int64, torrentName, reason string)
	Warn         func(ctx context.Context, userID int64, torrentName, reason string)
	LimitReached func(ctx context.Context, userID int64, activeWarnings int)
}

var (
	depsMu   sync.RWMutex
	hostDeps Deps
)

// SetDeps installs the host's seams. Called from main() before core.Boot.
//
// Every field is optional. A host that wires no notifiers still gets the
// accounting, just silently — worse for the member than a notice, and better
// than a boot failure for a site that has not decided yet. A host that wires no
// RenderPage simply has no member page.
func SetDeps(d Deps) {
	depsMu.Lock()
	defer depsMu.Unlock()
	hostDeps = d
}

// deps reads the installed seams.
//
// Under a lock because SetDeps is a package global and the sweep runs on a job
// goroutine — a host that re-wires at runtime (tests do) would otherwise race
// the thing reading it.
func deps() Deps {
	depsMu.RLock()
	defer depsMu.RUnlock()
	return hostDeps
}

// notifier is the sweep's view of the seams.
func notifier() Notifier {
	d := deps()
	return Notifier{Prewarn: d.Prewarn, Warn: d.Warn, LimitReached: d.LimitReached}
}

// pageReady reports whether the member page can be served, so Provision can
// decline to mount it rather than serving a signed-out-looking page.
func pageReady() bool {
	d := deps()
	return d.RenderPage != nil
}
