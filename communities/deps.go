package communities

import (
	"context"
	"database/sql"
	"errors"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/blob"
)

// Deps are the host seams this plugin renders through.
//
// The plugin owns all seven /c/* pages' markup now (templates/, embedded), so
// what crosses on the current contract is chrome and shared widgets rather
// than a data map. Markdown is the one that crosses for SAFETY: it sanitises,
// and a second allow-list inside the plugin would be a stored-XSS bug waiting
// on whichever copy is laxer. CSRFToken can only be answered by the host
// middleware that mints it; the pager and markdown editor are shared widgets
// whose plugin-local copies would drift from every other page using them;
// RelativeTime crosses for wording alone.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. status
	// crosses too: the create-community form re-renders on a validation
	// failure, and a 200 saying "rejected" would lie to anything reading the
	// status line.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)

	// CSRFToken feeds every POST form's hidden _csrf input — minted by host
	// middleware, so only the host can answer it.
	CSRFToken func(c *gin.Context) string

	// RenderPagination is the site's pager as finished HTML.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML

	// RenderEditor is the site's shared markdown editor as ready HTML, for
	// the options given — the new-thread form uses it, and pages across the
	// site share the same widget.
	RenderEditor func(opts map[string]any) template.HTML

	// RelativeTime is the site's time wording ("2 hours ago").
	RelativeTime func(v any) string

	// Markdown renders user-authored post bodies. The host owns the policy —
	// which extensions are enabled, what sanitisation runs — because the same
	// policy governs the forum, and two markdown dialects on one site is a
	// bug an operator reports as "why does this render differently here".
	// Required on BOTH contracts.
	Markdown func(src string) template.HTML

	// PageOffset is the host's paging arithmetic, needed on both contracts —
	// it shapes the query, not the markup.
	PageOffset func(page, pageSize int) int

	// BaseData and Pagination are the PREVIOUS contract, where the HOST
	// owned these seven templates and the plugin rendered them by name.
	//
	// Kept working because loon-demo-site still wires it and compiles against
	// this working tree — breaking it would surface as a build error in
	// someone else's session about code they did not write. Remove both, and
	// the branches in render()/legacyPager() that read them, once the demo
	// has moved to RenderPage. See TestLegacyContractIsStillAccepted.
	BaseData   func(c *gin.Context, extra gin.H) gin.H
	Pagination func(page, pageSize, totalItems int, baseURL string) any

	// Files stores banner/icon uploads under the community/ namespace.
	// Optional on either contract.
	Files blob.Store
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot;
// Provision fails loud if anything required is missing.
func SetDeps(d Deps) { deps = d }

// ready reports whether SetDeps supplied a complete contract — the current
// one (RenderPage + widgets) or the previous one (BaseData + Pagination).
// Markdown and PageOffset are required on both; uploads are optional — a
// host with no blob store simply cannot take banner images, which degrades
// one feature rather than the whole surface.
func (d Deps) ready() bool {
	if d.Markdown == nil || d.PageOffset == nil {
		return false
	}
	modern := d.RenderPage != nil && d.CSRFToken != nil && d.RenderPagination != nil &&
		d.RenderEditor != nil && d.RelativeTime != nil
	legacy := d.BaseData != nil && d.Pagination != nil
	return modern || legacy
}

// noRowsAsNil collapses sql.ErrNoRows to a nil error, so "row not found" is
// handled as absence rather than as a failure.
//
// Lifted from the host's storage package rather than seamed: it is four lines
// with no policy in it, and a seam for that would be ceremony.
func noRowsAsNil(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// FollowedName is the extension key for the account page's "Following" tab.
//
// The host Looks it up after core.Boot and renders whatever it returns —
// duck-typed in the template, so the plugin owes the host no shared type.
const FollowedName = "communities.followed"

// FollowedFunc is the extension's type: the communities a user subscribes to,
// or nil for none.
type FollowedFunc func(ctx context.Context, userID int) any
