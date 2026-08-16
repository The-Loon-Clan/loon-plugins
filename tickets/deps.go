package tickets

import (
	"context"
	"html/template"

	"github.com/gin-gonic/gin"
)

// Deps are the host seams the support surface renders and reasons through.
//
// Tickets is a FULL-PAGE surface, so its templates stay host-side and the
// plugin fills them — the same arrangement communities and news use.
//
// The interesting seam is Viewer. In-tree this plugin read the session user
// directly and asked the host's role model whether they were an admin or a
// mod. Both are host POLICY: what counts as staff differs per site, and a
// plugin that hard-codes "role >= mod" has quietly decided something the
// operator owns. So the plugin asks one question — who is this, and may they
// act on other people's tickets — and the host answers in its own terms.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The four pages
	// are this plugin's markup now, so what it needs from the host is chrome
	// rather than a data map.
	//
	// status is a parameter because three of these pages re-render themselves
	// on a validation failure. Fixing it at 200 would mean a page saying "your
	// ticket was rejected" while telling every client it succeeded.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)

	// BaseData and Pagination are the PREVIOUS contract, kept working while
	// loon-demo-site migrates. A host that sets these instead of RenderPage
	// gets the old behaviour: the plugin renders by template NAME out of the
	// host's own directory rather than its embedded fragment.
	//
	// This exists because loon-demo-site is maintained separately and builds
	// against this working tree — shipping the new seam alone would have
	// broken a build in someone else's session, for code they did not write.
	// Delete both, and the legacy branch in views.go, once demo sets
	// RenderPage.
	BaseData   func(c *gin.Context, extra gin.H) gin.H
	Pagination func(page, pageSize, totalItems int, baseURL string) any
	// RenderEditor returns the site's shared markdown editor as ready HTML,
	// for the options given. Shared chrome used by seven pages across the
	// site, so it is rendered host-side rather than copied in here — but the
	// OPTIONS are per call site (rows, placeholder), hence a function.
	RenderEditor func(opts map[string]any) template.HTML
	// Markdown renders a ticket body or reply.
	//
	// Crosses the seam rather than being reimplemented, and this one is not a
	// convenience: it SANITISES. A second renderer in the plugin would be a
	// second allow-list, and two sanitisers that disagree is a stored-XSS bug
	// waiting for whichever one is laxer. Same reasoning as a shared hash —
	// the failure would be silent.
	Markdown func(string) template.HTML

	// CSRFToken supplies the double-submit token for the five POST forms — the
	// host's session concern. REQUIRED with the modern contract: this plugin
	// shipped without it, every form posted tokenless, and the host's CSRF
	// middleware refused each with 403 — a member could not open a ticket at
	// all. (The legacy BaseData contract injects the token host-side, which is
	// why it needs nothing here.)
	CSRFToken func(c *gin.Context) string

	// PageOffset and Pagination are the host's paging helpers. Passed rather
	// than copied: the value is consumed by the host's pagination PARTIAL, so
	// a lifted copy would render correctly until the partial changed.
	PageOffset func(page, pageSize int) int
	// RenderPagination is the site's pager as finished HTML. A fragment runs
	// in this plugin's own template set and cannot reach the host's partials.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML

	// Viewer identifies the requester and answers the two authority questions
	// this surface asks. nil means not signed in.
	Viewer func(c *gin.Context) *Viewer

	// OwnerRole resolves a ticket author's role NAME, for the role chrome on
	// the detail page. Absent or erroring, the page falls back to the default
	// role — which is why a deleted account does not blank the ticket.
	OwnerRole func(ctx context.Context, userID int) (string, error)

	// RoleBadge is the host's display data for a role name — colour, label —
	// rendered by the host's own template, so the plugin never inspects it.
	// Optional: without it, tickets render without role chrome.
	RoleBadge func(ctx context.Context, roleName string) any

	// Notifications. Optional as a pair: a host with no notification system
	// still has a working ticket surface, it just does not announce.
	NotifyNewTicket func(ctx context.Context, ticketID int, username, subject, body string, userID int)
	NotifyReply     func(ctx context.Context, ticketID, ownerID, recipientID, authorID int, username, subject string, staff bool)
}

// Viewer is the requester as this plugin needs them.
//
// Staff and Admin are separate because the surface uses them differently:
// Staff may reply on any ticket and their replies are marked as coming from
// the site, while Admin may read a ticket they do not own. Collapsing the two
// would silently widen one of them.
type Viewer struct {
	ID       int
	Username string
	Role     string
	Staff    bool
	Admin    bool
}

var deps *Deps

// SetDeps hands the plugin its host seams. Call from main() before core.Boot;
// Provision fails loud if the required ones are missing.
func SetDeps(d Deps) { deps = &d }

// ready reports whether the seams a render cannot proceed without are wired.
// Notifications and role chrome are deliberately excluded — each degrades one
// feature rather than the page.
func (d *Deps) ready() bool {
	if d == nil || d.Viewer == nil || d.PageOffset == nil {
		return false
	}
	// Either contract is acceptable: the new chrome seam, or the legacy
	// name-and-data-map one. Not a mixture — a host that set half of each
	// would render some pages and blank others.
	modern := d.RenderPage != nil && d.RenderPagination != nil &&
		d.RenderEditor != nil && d.Markdown != nil && d.CSRFToken != nil
	legacy := d.BaseData != nil && d.Pagination != nil
	return modern || legacy
}
