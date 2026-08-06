// Package lists is the watchlist/collection system — personal NZB lists with
// public sharing, follows, copies, bulk download as a ZIP, and the
// /community/watchlists discovery grid.
//
// The list TABLES stay host-owned: the account Following tab and the
// release-page widgets read them too. This plugin owns the /lists surface
// over them.
package lists

import (
	"context"

	"github.com/gin-gonic/gin"
)

// ListRef is a list the plugin has to reason about: the four fields it
// actually checks, plus the host's own record for the host's template.
//
// Raw is the honest part of this seam. These pages render host-owned
// templates that read a dozen fields the plugin has no opinion about — cover
// art, item counts, grab totals. Rather than mirror a struct the plugin does
// not understand (and quietly break the page the first time the host adds a
// column), it carries the row through untouched and never looks inside. When
// the templates move here, Raw goes away.
type ListRef struct {
	ID     int
	Name   string
	Public bool
	UserID int
	Raw    any
}

// ItemRef is one release in a list: the two fields the ZIP builder needs,
// plus the host row for the detail template. Same Raw contract as ListRef.
type ItemRef struct {
	ID       int64
	Filename string
	Raw      any
}

// Deps carries everything the plugin cannot do for itself. Most entries are
// plain scalars in and out — only ListRef and ItemRef carry a host row, and
// only because the templates still live host-side.
type Deps struct {
	// BaseData merges the host's page chrome into a template data map.
	BaseData func(c *gin.Context, extra gin.H) gin.H
	// The host's JSON helpers, for the two AJAX endpoints.
	JSONOK    func(c *gin.Context, extras gin.H)
	JSONError func(c *gin.Context, code int, msg string)

	// Viewer identifies the requester; ok is false when anonymous. Username
	// is needed for a copied list's attribution and the follow notification.
	Viewer func(c *gin.Context) (id int, username string, ok bool)

	// DownloadAllowed gates the bulk-ZIP route. This is host POLICY, not the
	// plugin's: the site pins a member's browse IP and refuses downloads from
	// anywhere else, and what counts as a matching IP is the operator's rule.
	// A plugin that reimplemented it would be deciding something it does not
	// own — and would keep serving ZIPs after the host tightened the rule.
	DownloadAllowed func(c *gin.Context) bool

	// Gunzip inflates a stored NZB. Payloads are gzipped at rest by the host,
	// so the host owns the decoding.
	Gunzip func(compressed []byte) ([]byte, error)

	// Reads whose results only ever reach a host template are typed `any` —
	// the plugin hands them straight over and never inspects them.
	UserLists   func(ctx context.Context, userID int) (owned, followed any, err error)
	NzbAndLists func(ctx context.Context, nzbID int64) (nzb, lists any, err error)

	ByID        func(ctx context.Context, listID int) (*ListRef, error)
	Items       func(ctx context.Context, listID int) ([]ItemRef, error)
	IsFollowing func(ctx context.Context, userID, listID int) (bool, error)

	// Discovery returns the three axes of the public grid. The plugin merges
	// and dedupes them; the host just fetches.
	Discovery func(ctx context.Context, perAxis int) (newest, top, grabbed []ListRef, err error)

	Create    func(ctx context.Context, userID int, name, description string, public bool) error
	Delete    func(ctx context.Context, listID, userID int) error
	SetPublic func(ctx context.Context, listID, userID int, public bool) error
	// Copy duplicates a list and returns the new list's id to redirect to.
	Copy func(ctx context.Context, listID, userID int, username string) (newID int, err error)

	Follow   func(ctx context.Context, userID, listID int) error
	Unfollow func(ctx context.Context, userID, listID int) error
	// NotifyFollow pings the list owner. Optional: nil disables the ping and
	// following still works.
	NotifyFollow func(ctx context.Context, ownerID, actorID int, actorName, listName string, listID int64)

	AddItem            func(ctx context.Context, listID int, nzbID int64, userID int) error
	RemoveItem         func(ctx context.Context, listID int, nzbID int64, userID int) error
	NzbData            func(ctx context.Context, nzbID int64) ([]byte, error)
	IncrementDownloads func(ctx context.Context, listID int) error
}

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the
// composition root before core.Boot.
func SetDeps(d Deps) { deps = &d }

// ok reports whether every required dependency was supplied. NotifyFollow is
// deliberately excluded — it is the one genuinely optional entry.
func (d *Deps) ok() bool {
	return d != nil &&
		d.BaseData != nil && d.JSONOK != nil && d.JSONError != nil &&
		d.Viewer != nil && d.DownloadAllowed != nil && d.Gunzip != nil &&
		d.UserLists != nil && d.NzbAndLists != nil && d.ByID != nil &&
		d.Items != nil && d.IsFollowing != nil && d.Discovery != nil &&
		d.Create != nil && d.Delete != nil && d.SetPublic != nil && d.Copy != nil &&
		d.Follow != nil && d.Unfollow != nil &&
		d.AddItem != nil && d.RemoveItem != nil &&
		d.NzbData != nil && d.IncrementDownloads != nil
}
