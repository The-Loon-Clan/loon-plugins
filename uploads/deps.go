// Package uploads owns the member-facing upload domain: getting bytes in, and
// managing the rows that come out.
//
// The largest single surface still living in the ameNZB host when the plugin
// extraction closed — roughly 3,400 handler lines across seven files, covering
// public and private NZB upload, private torrent upload, two batch-review
// flows, scripted bulk endpoints with their own tokens, and the owner's
// management page. One bounded job, split across four handler structs for
// historical reasons rather than by design.
//
// It is being lifted in slices rather than at once, smallest first, because
// each slice is independently useful and a half-finished 3,400-line lift is
// not. The order and the reasoning are in README.md. This file is the contract
// every slice shares.
//
// Host-data worker, not a schema owner: every table it touches (nzbs,
// nzb_requests, agent_tokens) is host-owned and read by host pages, so it
// takes narrow function seams the way feeds and curation do, rather than
// building its own store.
package uploads

import (
	"context"
	"html/template"

	"github.com/gin-gonic/gin"
)

// Upload is one row on the owner's management page: something this member put
// on the site, in whatever shape it landed.
//
// Deliberately not the host's models.Nzb. That row carries ~50 fields —
// health verdicts, AI-upscale provenance, moderation state, half a dozen
// external ids — and a page that lists a member's own uploads has business
// with almost none of them. Copying the whole row across would make this
// package's contract a mirror of the host's schema, which is the coupling the
// plugin boundary exists to remove.
type Upload struct {
	ID    int64
	Title string
	// Kind distinguishes what the member actually uploaded, because the
	// actions differ: an NZB can be made anonymous, a private torrent can be
	// kept private, and offering the wrong control is worse than offering none.
	Kind      string // "nzb" | "private-nzb" | "private-torrent"
	SizeBytes int64
	CreatedAt string // preformatted by the host: date rendering is site vocabulary
	// Anonymous hides the uploader publicly but keeps the row manageable here.
	// TrueAnonymous has severed the link entirely — such a row can never
	// appear on this page again, which is the member's explicit choice and the
	// reason the two flags are not one.
	Anonymous bool
	Deleted   bool
	// Queued reports that an agent has this upload's request in its queue, for
	// the sidebar link. False when nothing is dispatched.
	Queued bool
}

// OwnerActions are the owner-scoped mutations the management page performs.
// Every one is scoped to the calling member by the HOST, not by an id this
// package passes: a plugin asserting "trust me, this row is theirs" is one
// bug away from letting a member delete somebody else's upload.
type OwnerActions struct {
	// SoftDelete and Restore act on one row. Both no-op silently when the row
	// is not the member's, which is the correct answer to a forged id.
	SoftDelete func(ctx context.Context, userID int, uploadID int64) error
	Restore    func(ctx context.Context, userID int, uploadID int64) error
	// SetAnonymous hides the uploader publicly and is reversible.
	SetAnonymous func(ctx context.Context, userID int, uploadID int64, on bool) error
	// SetTrueAnonymous is NOT reversible: it clears the owner link, so the row
	// leaves this page permanently. The host confirms with the member before
	// calling it — this package must never offer it as a quiet toggle.
	SetTrueAnonymous func(ctx context.Context, userID int, uploadID int64) error
	// The bulk forms of the same four, applying to everything the member owns.
	SoftDeleteAll       func(ctx context.Context, userID int) (int, error)
	RestoreAll          func(ctx context.Context, userID int) (int, error)
	SetAllAnonymous     func(ctx context.Context, userID int, on bool) (int, error)
	SetAllTrueAnonymous func(ctx context.Context, userID int) (int, error)
	// KeepPrivate toggles whether a private request's artifact stays private.
	KeepPrivate func(ctx context.Context, userID int, requestID int64, on bool) error
}

// Viewer is the signed-in member, or nil. Mirrors the shape every other
// lifted plugin takes so an author learns one model.
type Viewer struct {
	ID       int
	Username string
	Mod      bool
}

// Deps are the host seams. Provision refuses to boot without the required
// ones: a half-wired upload page that renders an empty list looks exactly like
// a member who has never uploaded anything.
type Deps struct {
	// Viewer resolves the signed-in member from the request.
	Viewer func(c *gin.Context) *Viewer

	// ListUploads returns one page of the member's uploads, newest first,
	// with the total for pagination.
	ListUploads func(ctx context.Context, userID, limit, offset int) ([]Upload, int, error)

	// Actions are the owner-scoped mutations.
	Actions OwnerActions

	// RenderPage wraps a fragment in the site's chrome. The plugin owns its
	// markup and the host owns the page around it.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)
	// CSRFToken answers the host middleware's per-session token. Required, not
	// optional: every action on this page is a POST, and a form without it
	// 403s on submit — which is how the discord settings form spent its whole
	// life broken.
	CSRFToken func(c *gin.Context) string
	// RenderPagination returns the site's pager markup for a page of results.
	// Signature matched to the other lifted plugins (roadmap, messages, forum)
	// rather than chosen fresh: an author wiring their fifth plugin should not
	// have to check which shape this one wanted.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML
}

var deps *Deps

// SetDeps stages the host seams. Call before core.Boot.
func SetDeps(d Deps) { deps = &d }

func (d *Deps) ok() bool {
	if d == nil {
		return false
	}
	a := d.Actions
	return d.Viewer != nil && d.ListUploads != nil &&
		d.RenderPage != nil && d.CSRFToken != nil && d.RenderPagination != nil &&
		a.SoftDelete != nil && a.Restore != nil &&
		a.SetAnonymous != nil && a.SetTrueAnonymous != nil &&
		a.SoftDeleteAll != nil && a.RestoreAll != nil &&
		a.SetAllAnonymous != nil && a.SetAllTrueAnonymous != nil &&
		a.KeepPrivate != nil
}
