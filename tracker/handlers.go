package tracker

import (
	"html/template"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Deps is the render seam the host supplies.
//
// The member pages keep the URLs they had before the extraction (/tracker,
// /tracker/my) — moving them to /p/tracker would be a cost paid by every member
// with a bookmark — but the MARKUP belongs to the plugin. It renders its own
// fragment and the host wraps it in the site chrome, which is the arrangement
// messages, wiki, forum, lists, news, offers, store and tickets all use.
//
// The first version of this seam took (tmpl string, data any) and rendered a
// template out of the HOST's set. That looked like less work and was a trap: the
// host templates are whole pages, so they pull in navbar and footer and need the
// host's BaseData to render at all, and they referenced two fields
// (Totals.ActiveCount, Totals.TorrentCount) this plugin does not have. Neither
// problem shows up at compile time — html/template streams, so a missing field
// truncates the page mid-row. Owning the fragment removes the whole class.
type Deps struct {
	// RenderPage wraps a rendered fragment in the site's layout — navbar, footer,
	// session context. Required: without it these pages render as though nobody
	// is signed in, which reads as a broken session rather than a missing seam.
	RenderPage func(c *gin.Context, title string, body template.HTML)

	// CSRFToken supplies the double-submit token for the passkey-rotate form.
	// A separate seam rather than a field on the view model because the token is
	// the host's session concern, not the tracker's.
	CSRFToken func(c *gin.Context) string

	// RelativeTime formats a timestamp the way the rest of the site does
	// ("3 hours ago"). Borrowed rather than reimplemented so the tracker's
	// "last seen" column does not drift from every other one.
	RelativeTime func(t time.Time) string

	// ReleaseURL is the host's page for the release a torrent was made from.
	// Empty string when the host has no page for that id.
	//
	// OPTIONAL, and the only optional seam here — the three above are required
	// because without them a page is broken, while without this one a listing
	// renders the torrent's name as text instead of a link. depsReady does not
	// check it for that reason.
	//
	// A seam rather than the plugin building "/release/%d" itself. The torrent
	// row carries nzb_id, which is an ID IN THE HOST'S INDEX; where that id is
	// browsable is the host's fact, and a plugin that hardcodes another
	// component's URL scheme is one that breaks silently when the scheme moves.
	ReleaseURL func(nzbID int64) string
}

var deps *Deps

// SetDeps is called from the host's main() before core.Boot.
func SetDeps(d Deps) { deps = &d }

// depsReady reports whether every required seam is wired, for Provision to fail
// loud rather than at first request.
func depsReady() bool {
	return deps != nil && deps.RenderPage != nil && deps.CSRFToken != nil && deps.RelativeTime != nil
}

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
	// tmpl is the plugin's own fragment set, shared with the admin view.
	tmpl *template.Template
	// promotions answers what magic is cast on one torrent, for the torrent
	// page. A sibling plugin's read, so it is looked up in Start and may be
	// nil — a host without the magic plugin simply has no promotions panel,
	// which is the correct page for that host rather than a degraded one.
	promotions pluginapi.TorrentPromotionsFunc
}

// SetPromotions installs the sibling read. Called from Start, never Provision:
// registration order between two plugins is nobody's promise, and this one has
// been learned twice in this tree already.
func (h *Handlers) SetPromotions(fn pluginapi.TorrentPromotionsFunc) { h.promotions = fn }

// SetTemplates hands the handler set the parsed fragments. Separate from
// NewHandlers because Provision parses once and both the member pages and the
// admin view read the same set.
func (h *Handlers) SetTemplates(t *template.Template) { h.tmpl = t }

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

// render executes one of the plugin's own fragments and hands it to the host's
// layout. A nil seam is a wiring bug rather than a runtime condition, so it says
// so instead of writing a blank page.
func (h *Handlers) render(c *gin.Context, tmpl, title string, data any) {
	if !depsReady() {
		c.String(500, "tracker: SetDeps was not called with a full Deps — wire it in main() before core.Boot")
		return
	}
	if h.tmpl == nil {
		c.String(500, "tracker: templates were not parsed")
		return
	}
	var sb strings.Builder
	if err := h.tmpl.ExecuteTemplate(&sb, tmpl, data); err != nil {
		// A fragment that fails mid-execute has already written part of itself to
		// sb, which is exactly why it goes to a buffer first: the half-rendered
		// output is discarded instead of reaching the browser as a truncated page.
		//
		// The member gets the same sentence every other plugin's render failure
		// gives. It used to be "tracker: rendering %s: %v" — the template name
		// and the raw Go error, printed to whoever hit it, which is the defect
		// forum and communities each recorded fixing in their own handlers.
		log.Printf("tracker: rendering %s: %v", tmpl, err)
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
