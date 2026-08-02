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
// Communities is a FULL-PAGE surface: it serves /c/* as ordinary site pages
// with the host's chrome, so unlike a self-contained admin plugin it cannot
// own its own templates. The templates stay host-side and the plugin fills
// them, which is the same arrangement the news plugin uses.
//
// Pagination is a seam rather than a lifted copy on purpose. The value is
// consumed by the host's pagination PARTIAL, which reads fields off it —
// duplicating the type here would compile and render right up until the host
// changed the partial, and then drift silently. Passing the host's own
// constructor through keeps one implementation.
type Deps struct {
	// BaseData merges the host's page chrome (user, nav, CSRF, flash) into a
	// template data map. Every page render goes through it.
	BaseData func(c *gin.Context, extra gin.H) gin.H

	// Markdown renders user-authored post bodies. The host owns the policy —
	// which extensions are enabled, what sanitisation runs — because the same
	// policy governs the forum, and two markdown dialects on one site is a
	// bug an operator reports as "why does this render differently here".
	Markdown func(src string) template.HTML

	// PageOffset and Pagination are the host's paging helpers. See the note
	// above for why these are passed rather than copied.
	PageOffset func(page, pageSize int) int
	Pagination func(page, pageSize, totalItems int, baseURL string) any

	// Files stores banner/icon uploads under the community/ namespace.
	Files blob.Store
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot;
// Provision fails loud if anything required is missing.
func SetDeps(d Deps) { deps = d }

// ready reports whether SetDeps supplied everything a render needs. Uploads
// are optional — a host with no blob store simply cannot take banner images,
// which degrades one feature rather than the whole surface.
func (d Deps) ready() bool {
	return d.BaseData != nil && d.Markdown != nil &&
		d.PageOffset != nil && d.Pagination != nil
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
