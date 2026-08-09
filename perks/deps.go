package perks

import (
	"html/template"
	"sync"

	"github.com/gin-gonic/gin"
)

// The host seams for the member page.
//
// Only what this plugin cannot do for itself: wrap a fragment in the site's
// layout, and mint the CSRF token for the spend form. Everything else — what a
// perk is, what it costs, when it expires — is the plugin's own business.

type Deps struct {
	// RenderPage wraps a fragment in the site's layout. Without it the wallet
	// page is not mounted at all, which is better than serving one that looks
	// signed-out.
	RenderPage func(c *gin.Context, title string, body template.HTML)

	// CSRFToken supplies the double-submit token for the spend form. A seam
	// rather than a field on the view model because the token is the host's
	// session concern, not this plugin's.
	CSRFToken func(c *gin.Context) string
}

var (
	depsMu   sync.RWMutex
	hostDeps Deps
)

// SetDeps installs the host's seams. Called from main() before core.Boot.
func SetDeps(d Deps) {
	depsMu.Lock()
	defer depsMu.Unlock()
	hostDeps = d
}

func deps() Deps {
	depsMu.RLock()
	defer depsMu.RUnlock()
	return hostDeps
}

// pageReady reports whether the wallet can be served.
func pageReady() bool { return deps().RenderPage != nil }
