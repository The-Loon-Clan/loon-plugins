// Package lists is the watchlist/collection system — personal NZB lists with
// public sharing, follows, copies, bulk download as a ZIP, and the
// /community/watchlists discovery grid.
//
// The list TABLES stay host-owned: the account Following tab and the
// release-page widgets read them too. This plugin owns the /lists surface
// over them, markup included.
package lists

import (
	"context"
	"errors"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// List is a watchlist as this plugin's pages display it.
//
// These are the plugin's OWN fields, not a window onto the host's record.
// That is the difference the template move bought: while the markup lived
// host-side it read the host's struct directly, so the plugin had to carry
// the row through opaquely and the two could never be told apart. Now the
// pages render from this, and anything the host adds to its own record is
// invisible here until someone asks for it.
type List struct {
	ID            int
	Name          string
	Description   string
	Public        bool
	Username      string // the owner's display name
	ItemCount     int
	CoverURL      string
	DownloadCount int
	FollowCount   int
	CreatedAt     time.Time

	// UserID is the owner, for the private-list check. Not rendered.
	UserID int
}

// Item is one release in a list.
//
// Card is the site's release card, already rendered: it is shared chrome used
// on every listing page, reads most of a release record, and belongs to the
// host. Rather than mirror that whole record here to redraw a card the host
// already knows how to draw, the host draws it and this carries the result.
// ID and Filename are the two fields the ZIP builder needs for itself.
type Item struct {
	ID       int64
	Filename string
	Card     template.HTML
}

// NzbRef is the release header on /release/:id/lists — the two fields that
// page prints, nothing more.
type NzbRef struct {
	ID    int64
	Title string
}

// Deps carries everything the plugin cannot do for itself.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome and writes it.
	//
	// This plugin keeps its own routes rather than becoming slot views: it is
	// four pages, one of them with a path parameter (/lists/:id), and the
	// slot model is one page per slug. So the host supplies chrome on demand
	// instead, and every URL stays exactly where members have it bookmarked.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// RenderError writes the site's error page. Used for the one refusal that
	// is a page rather than a redirect (bulk download from an unpinned IP).
	RenderError func(c *gin.Context, code int, msg string)

	// Core is the registry, for the seams this plugin resolves per request —
	// today the CSRF token, which its nine POST forms had no way to reach.
	Core *core.Core

	// Shared site chrome the plugin embeds but does not own. The release
	// card itself arrives pre-rendered on each Item — the host has the record
	// it needs at that point, so there is nothing to hand back here.
	NzbCardCSS  func() template.HTML
	ReportModal func(c *gin.Context) template.HTML

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

	UserLists   func(ctx context.Context, userID int) (owned, followed []List, err error)
	ByID        func(ctx context.Context, listID int) (*List, error)
	Items       func(ctx context.Context, listID int) ([]Item, error)
	IsFollowing func(ctx context.Context, userID, listID int) (bool, error)
	ListsForNzb func(ctx context.Context, nzbID int64, viewerID int) (*NzbRef, []List, error)

	// Discovery returns the three axes of the public grid. The plugin merges
	// and dedupes them; the host just fetches.
	Discovery func(ctx context.Context, perAxis int) (newest, top, grabbed []List, err error)

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

	// DownloadItem is the host's per-release gate for the bulk ZIP, called
	// for each item before it is written. It is where the host counts the
	// download against the member's daily limit and refuses releases the
	// member may not have (hidden, deleted). Return ErrSkipItem to leave one
	// item out and carry on; any other error ends the ZIP there. A refusal
	// on the FIRST item is answered as a page instead of an empty ZIP.
	//
	// Optional: nil serves every item, the behaviour before it existed. A
	// host with a download limit should set it — without it "Download all"
	// was a way round that limit, one ZIP of any size with no download
	// counted.
	DownloadItem func(c *gin.Context, nzbID int64) error
}

// ErrSkipItem tells DownloadAllList to leave one item out of the ZIP and
// continue with the next.
var ErrSkipItem = errors.New("lists: skip this item")

// ErrDownloadLimit is what a host's DownloadItem returns (wrapped or not)
// when the member's download allowance is spent. On the first item it is
// answered with a "limit" page rather than a generic one.
var ErrDownloadLimit = errors.New("lists: download limit reached")

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the
// composition root before core.Boot.
func SetDeps(d Deps) { deps = &d }

// ok reports whether every required dependency was supplied. NotifyFollow is
// deliberately excluded — it is the one genuinely optional entry.
func (d *Deps) ok() bool {
	return d != nil &&
		d.RenderPage != nil && d.RenderError != nil &&
		d.NzbCardCSS != nil && d.ReportModal != nil &&
		d.JSONOK != nil && d.JSONError != nil &&
		d.Viewer != nil && d.DownloadAllowed != nil && d.Gunzip != nil &&
		d.UserLists != nil && d.ByID != nil && d.Items != nil &&
		d.IsFollowing != nil && d.ListsForNzb != nil && d.Discovery != nil &&
		d.Create != nil && d.Delete != nil && d.SetPublic != nil && d.Copy != nil &&
		d.Follow != nil && d.Unfollow != nil &&
		d.AddItem != nil && d.RemoveItem != nil &&
		d.NzbData != nil && d.IncrementDownloads != nil
}
