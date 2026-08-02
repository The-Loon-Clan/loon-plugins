package tickets

import (
	"context"

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
	// BaseData merges the host's page chrome into a template data map.
	BaseData func(c *gin.Context, extra gin.H) gin.H

	// PageOffset and Pagination are the host's paging helpers. Passed rather
	// than copied: the value is consumed by the host's pagination PARTIAL, so
	// a lifted copy would render correctly until the partial changed.
	PageOffset func(page, pageSize int) int
	Pagination func(page, pageSize, totalItems int, baseURL string) any

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
	return d != nil && d.BaseData != nil && d.Viewer != nil &&
		d.PageOffset != nil && d.Pagination != nil
}
