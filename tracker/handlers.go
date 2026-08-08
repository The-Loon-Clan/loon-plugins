package tracker

import (
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Deps is the render seam the host supplies.
//
// The member pages keep the URLs they had before the extraction (/tracker,
// /tracker/my) and render templates that live in the HOST's set — the same
// arrangement messages, wiki and donations use. Moving them to /p/tracker to make
// the plugin self-contained would be a cost paid by every member with a bookmark,
// so the plugin borrows the chrome instead.
type Deps struct {
	// RenderPage renders a host template with the site's own layout — navbar,
	// footer, session context. Required: without it these pages render as though
	// nobody is signed in, which reads as a broken session rather than a missing
	// seam.
	RenderPage func(c *gin.Context, tmpl string, data any)
}

var deps *Deps

// SetDeps is called from the host's main() before core.Boot.
func SetDeps(d Deps) { deps = &d }

// Handlers serves the tracker: the two BitTorrent endpoints a client talks to,
// and the member pages a browser does.
//
// One type rather than two, because they share the store, the gate and the
// passkey — the .torrent download is a browser request that bakes in the same
// passkey an announce arrives with, so a split would cut across that.
type Handlers struct {
	store   Store
	peers   *PeerStore
	gate    Gate
	auth    core.AuthService
	siteURL string
}

// NewHandlers builds the handler set. siteURL is the absolute base (scheme +
// host, no trailing slash) baked into every downloaded .torrent's announce URL —
// wrong here means torrents that point somewhere unable to answer.
func NewHandlers(store Store, peers *PeerStore, gate Gate, auth core.AuthService, siteURL string) *Handlers {
	return &Handlers{store: store, peers: peers, gate: gate, auth: auth, siteURL: trimRightSlash(siteURL)}
}

func (h *Handlers) currentUser(c *gin.Context) (*core.User, bool) {
	if h.auth == nil {
		return nil, false
	}
	return h.auth.CurrentUser(c)
}

// render hands a page to the host's layout. A nil seam is a wiring bug rather
// than a runtime condition, so it says so instead of writing a blank page.
func (h *Handlers) render(c *gin.Context, tmpl string, data any) {
	if deps == nil || deps.RenderPage == nil {
		c.String(500, "tracker: SetDeps was not called with a RenderPage — wire it in main() before core.Boot")
		return
	}
	deps.RenderPage(c, tmpl, data)
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
