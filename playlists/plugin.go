// Package playlists is user-curated collections of indexed releases —
// UNIT3D's playlist area.
//
// Self-contained by design: it owns its schema, needs no points, no
// entitlements and no external service, and its only host seams are page chrome,
// paging, and two lookups it cannot do itself (a username, and resolving
// release ids to something renderable).
//
// Those two lookups are the whole portability story. A plugin cannot join a
// host's users table or query a host's release index — it does not know their
// shape, and assuming one is exactly what leaves a plugin unwirable on a host
// whose columns differ. So it stores ids and asks the host to resolve them.
package playlists

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var playlistMigrations embed.FS

func init() {
	core.RegisterPlugin("playlists", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams. SetDeps must be called before core.Boot; Provision
// fails loud otherwise, so a half-wired host cannot serve broken pages.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The plugin
	// owns its four pages now (templates/, embedded), so chrome crosses
	// rather than a data map.
	//
	// status crosses too: Create re-renders the form on a validation failure,
	// and a seam fixed at 200 reports success while showing an error.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)

	// CSRFToken feeds the create, edit, add and remove forms — minted by host
	// middleware, so only the host can answer it.
	CSRFToken func(c *gin.Context) string

	// RenderPagination is the site's pager as finished HTML. The index used
	// to build its own <nav> from Deps.Pagination's view-model, which is six
	// fields where one seam does and six chances to drift when the host's own
	// pager changes.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML

	// RenderUserTag is the site's own username chip — role colour, name
	// effects, profile link — as finished HTML.
	//
	// The markup used to call the host partial {{template "user-tag"}}
	// directly, which worked only because the host parsed its chrome and every
	// plugin template into one flat namespace. A plugin's own set has no such
	// reach, and should not: what a username LOOKS like is the host's.
	//
	// Optional. A host that leaves it nil gets a plain link to the profile —
	// the information without the decoration.
	RenderUserTag func(name string) template.HTML

	// RelativeTime is the site's time wording ("2 hours ago"). Passed rather
	// than copied for consistency of phrasing across the site.
	RelativeTime func(any) string

	// BaseData and Pagination are the PREVIOUS contract, where the HOST owned
	// these four templates and the plugin rendered them by name.
	//
	// Kept working so a host mid-migration keeps building. Remove both, and
	// the branches in render()/paginationHTML() that read them, once every
	// host has moved to RenderPage.
	BaseData func(c *gin.Context, extra gin.H) gin.H

	// PageOffset and Pagination are the host's paging helpers. Taken rather
	// than reimplemented: the view-model is consumed by the host's own
	// pagination partial, so a lifted copy renders correctly right up until
	// that partial changes.
	PageOffset func(page, pageSize int) int
	Pagination func(page, pageSize, totalItems int, baseURL string) any

	// LookupReleases resolves release ids to something renderable. Ids that no
	// longer exist must simply be ABSENT from the result — retention removes
	// releases, and a collection outliving its contents is normal. The plugin
	// renders a missing id as unavailable rather than hiding the row, so a
	// curator can see what they lost.
	LookupReleases func(ctx context.Context, ids []int64) (map[int64]Release, error)

	// LookupUsername resolves an owner id for display. Optional: without it the
	// owner shows as a bare id, which is ugly but not wrong.
	LookupUsername func(ctx context.Context, userID int64) (string, bool)
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

// Plugin is the core.Plugin lifecycle wrapper. There is no background work, so
// Start and Stop are no-ops.
type Plugin struct {
	// pg is the concrete store, held for the collection sink — which needs the
	// database rather than the Store interface's per-playlist operations.
	pg       *PGStore
	core     *core.Core
	handlers *Handlers
}

// renderContractOK accepts either contract and refuses half of one.
//
// A host that wired some of the render seams would serve some pages and blank
// others, which reads as a broken site rather than a missing call.
func (d Deps) renderContractOK() bool {
	modern := d.RenderPage != nil && d.CSRFToken != nil &&
		d.RenderPagination != nil && d.RelativeTime != nil
	legacy := d.BaseData != nil && d.Pagination != nil
	return modern || legacy
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "playlists",
		Version:     "0.1.0",
		Description: "User-curated collections of indexed releases.",
		Migrations:  playlistMigrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

const pageSize = 24

func (p *Plugin) Provision(c *core.Core) error {
	if !deps.renderContractOK() || deps.PageOffset == nil {
		return fmt.Errorf("playlists: SetDeps not called, or a render seam is missing — " +
			"PageOffset plus either the current contract (RenderPage, CSRFToken, " +
			"RenderPagination, RelativeTime) or the previous one (BaseData, Pagination); " +
			"wire it in main() before core.Boot")
	}
	// Parsed here, not at package init: RelativeTime is a Deps function.
	// Forgetting this leaves pageTmpl nil and panics on the first page view
	// rather than failing at boot.
	if err := parseTemplates(); err != nil {
		return fmt.Errorf("playlists: parse templates: %w", err)
	}
	if deps.LookupReleases == nil {
		return fmt.Errorf("playlists: SetDeps needs LookupReleases — a playlist that cannot resolve its releases has nothing to show")
	}
	p.core = c

	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("playlists: no schema DB")
	}
	pg := NewPGStore(db)
	p.pg = pg
	p.handlers = &Handlers{store: pg, auth: c.Auth, core: c}
	if err := declareEvents(c); err != nil {
		return err
	}
	// Where the host's cart empties (pluginapi.CollectionSink). The host lets a
	// member tick rows across a listing; one thing it can then do with them is
	// put them in a collection, without knowing collections are playlists or
	// that this plugin exists.
	if err := c.Register(pluginapi.CollectionSinkName, sink{p: p}); err != nil {
		return fmt.Errorf("playlists: register collection sink: %w", err)
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("playlists: Core.Router.Engine() is nil")
	}

	// Reading is public (a public playlist is public), writing needs an
	// account. Two groups rather than one with per-handler checks, so the
	// authentication boundary is visible in the route table.
	pub := engine.Group("/playlists")
	pub.Use(c.Auth.Authenticate()...)
	pub.GET("", p.handlers.Index)
	pub.GET("/:slug", p.handlers.Show)

	authed := engine.Group("/playlists")
	authed.Use(c.Auth.RequireUser(core.RoleUser)...)
	authed.GET("/new", p.handlers.New)
	authed.POST("", p.handlers.Create)
	authed.GET("/:slug/edit", p.handlers.Edit)
	authed.POST("/:slug/update", p.handlers.Update)
	authed.POST("/:slug/delete", p.handlers.Destroy)
	authed.POST("/:slug/items", p.handlers.AddItem)
	authed.POST("/:slug/items/:id/delete", p.handlers.RemoveItem)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

// Handlers holds the request handlers.
type Handlers struct {
	store Store
	// auth resolves the signed-in user. Taken from the Core rather than read
	// off a gin context key: the key is the host's private detail, and guessing
	// it yields a silent anonymous-for-everyone bug rather than a compile error.
	auth core.AuthService
	// core is the mediator, for announcing what members curate. Nil in tests.
	core *core.Core
}

// viewer returns the signed-in user id, or 0 for anonymous. 0 is a real value
// here — ListVisible treats it as "public only" — so it is not an error.
func (h *Handlers) viewer(c *gin.Context) int64 {
	if u, ok := h.auth.CurrentUser(c); ok && u != nil {
		return u.ID
	}
	return 0
}

func (h *Handlers) Index(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	ctx := c.Request.Context()
	rows, total, err := h.store.ListVisible(ctx, h.viewer(c), pageSize, deps.PageOffset(page, pageSize))
	if err != nil {
		h.render(c, http.StatusInternalServerError, "playlists_index.html", gin.H{
			"Error": "loadfailed",
		})
		return
	}
	h.fillUsernames(ctx, rows)
	h.render(c, http.StatusOK, "playlists_index.html", gin.H{
		"Playlists": rows,
		"Total":     total,
		// Both, for as long as both contracts are accepted: the legacy
		// markup in the host reads .Pagination's view-model, the plugin's own
		// reads .PaginationHTML. Each renders nothing on the other's path.
		"Pagination":     legacyPagination(page, pageSize, total, "/playlists"),
		"PaginationHTML": paginationHTML(page, pageSize, total, "/playlists"),
	})
}

// fail renders the site's error page with a CODE the template turns into
// words (CHECKLIST §10). playlist_error.html, in the HOST's plugin template
// set, holds the sentences — this plugin has no templates of its own, which is
// exactly why its messages stayed in Go longer than everyone else's.
//
// It replaces bare c.String responses, which answered a member with plain text
// on a blank page: no chrome, no nav, no way back. That is a UX bug as much as
// an i18n one, and it is why these are worth converting rather than merely
// counting.
func (h *Handlers) fail(c *gin.Context, status int, code string) {
	// No BaseData means no host chrome to render into — Provision refuses to
	// start without it, so this is the unit-test path rather than a live one.
	// The fallback writes the CODE, not a sentence: machine-readable, and
	// still identical for two callers who passed the same code, which is what
	// TestOwnedAnswersTheSameForMissingAndNotYours depends on. A response that
	// differs between "not yours" and "does not exist" is an oracle for
	// whether a private playlist exists.
	if deps.BaseData == nil {
		c.String(status, code)
		return
	}
	h.render(c, status, "playlist_error.html", gin.H{"Reason": code})
}

func (h *Handlers) Show(c *gin.Context) {
	ctx := c.Request.Context()
	p, err := h.store.BySlug(ctx, c.Param("slug"))
	if err != nil || p == nil {
		h.fail(c, http.StatusNotFound, "notfound")
		return
	}
	me := h.viewer(c)
	// A private playlist is a 404, not a 403: a 403 confirms the slug exists,
	// which is the one thing "private" is meant to withhold.
	//
	// pluginapi.OwnedBy rather than `p.UserID != me`, because viewer() returns
	// 0 for anonymous and this route is behind Authenticate() — which lets
	// anonymous through in the site's public access mode. The IsOwner line
	// below already got this right; this one did not, two lines apart.
	if !p.Public && !pluginapi.OwnedBy(p.UserID, me) {
		h.fail(c, http.StatusNotFound, "notfound")
		return
	}
	items, err := h.store.ListItems(ctx, p.ID)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "loadfailed")
		return
	}
	h.resolveItems(ctx, items)
	h.fillUsernames(ctx, []*Playlist{p})
	h.render(c, http.StatusOK, "playlist_view.html", gin.H{
		"Playlist": p,
		"Items":    items,
		"IsOwner":  pluginapi.OwnedBy(p.UserID, me),
		"Saved":    c.Query("saved") == "1",
	})
}

func (h *Handlers) New(c *gin.Context) {
	h.render(c, http.StatusOK, "playlist_form.html", gin.H{"Action": "Create"})
}

func (h *Handlers) Create(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		h.render(c, http.StatusBadRequest, "playlist_form.html", gin.H{
			"Action": "Create", "Error": "noname",
		})
		return
	}
	p := &Playlist{
		UserID:      h.viewer(c),
		Slug:        slugify(name),
		Name:        name,
		Description: strings.TrimSpace(c.PostForm("description")),
		CoverURL:    strings.TrimSpace(c.PostForm("cover_url")),
		Public:      c.PostForm("public") == "1",
	}
	switch err := h.store.Create(c.Request.Context(), p); {
	case err == ErrSlugTaken:
		h.render(c, http.StatusBadRequest, "playlist_form.html", gin.H{
			"Action": "Create", "Error": "nametaken",
			"Name": name, "Description": p.Description, "CoverURL": p.CoverURL, "Public": p.Public,
		})
	case err != nil:
		h.render(c, http.StatusInternalServerError, "playlist_form.html", gin.H{
			"Action": "Create", "Error": "createfailed",
		})
	default:
		h.emit(c.Request.Context(), EventPlaylistCreated, p.UserID,
			PlaylistCreated{PlaylistID: p.ID, Slug: p.Slug, Name: p.Name, Public: p.Public})
		c.Redirect(http.StatusFound, "/playlists/"+p.Slug)
	}
}

func (h *Handlers) Edit(c *gin.Context) {
	p, ok := h.owned(c)
	if !ok {
		return
	}
	h.render(c, http.StatusOK, "playlist_form.html", gin.H{
		"Action": "Save", "Playlist": p,
		"Name": p.Name, "Description": p.Description, "CoverURL": p.CoverURL, "Public": p.Public,
	})
}

func (h *Handlers) Update(c *gin.Context) {
	p, ok := h.owned(c)
	if !ok {
		return
	}
	p.Name = strings.TrimSpace(c.PostForm("name"))
	p.Description = strings.TrimSpace(c.PostForm("description"))
	p.CoverURL = strings.TrimSpace(c.PostForm("cover_url"))
	p.Public = c.PostForm("public") == "1"
	if p.Name == "" {
		c.Redirect(http.StatusFound, "/playlists/"+p.Slug+"/edit")
		return
	}
	if err := h.store.Update(c.Request.Context(), p); err != nil {
		h.fail(c, http.StatusInternalServerError, "savefailed")
		return
	}
	c.Redirect(http.StatusFound, "/playlists/"+p.Slug+"?saved=1")
}

func (h *Handlers) Destroy(c *gin.Context) {
	p, ok := h.owned(c)
	if !ok {
		return
	}
	if err := h.store.Delete(c.Request.Context(), p.ID, p.UserID); err != nil {
		h.fail(c, http.StatusInternalServerError, "deletefailed")
		return
	}
	c.Redirect(http.StatusFound, "/playlists")
}

func (h *Handlers) AddItem(c *gin.Context) {
	p, ok := h.owned(c)
	if !ok {
		return
	}
	releaseID, err := strconv.ParseInt(c.PostForm("release_id"), 10, 64)
	if err != nil || releaseID <= 0 {
		c.Redirect(http.StatusFound, "/playlists/"+p.Slug)
		return
	}
	if err := h.store.AddItem(c.Request.Context(), p.ID, releaseID, strings.TrimSpace(c.PostForm("note"))); err != nil {
		h.fail(c, http.StatusInternalServerError, "addfailed")
		return
	}
	h.emit(c.Request.Context(), EventItemAdded, p.UserID, ItemAdded{PlaylistID: p.ID, ReleaseID: releaseID})
	c.Redirect(http.StatusFound, "/playlists/"+p.Slug)
}

func (h *Handlers) RemoveItem(c *gin.Context) {
	p, ok := h.owned(c)
	if !ok {
		return
	}
	itemID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.store.RemoveItem(c.Request.Context(), p.ID, itemID); err != nil {
		h.fail(c, http.StatusInternalServerError, "removefailed")
		return
	}
	c.Redirect(http.StatusFound, "/playlists/"+p.Slug)
}

// owned loads the playlist named in the route and confirms the caller owns it,
// writing the response itself when it does not. One helper rather than the same
// four lines in six handlers — an ownership check that is easy to omit is one
// that eventually gets omitted.
func (h *Handlers) owned(c *gin.Context) (*Playlist, bool) {
	p, err := h.store.BySlug(c.Request.Context(), c.Param("slug"))
	if err != nil || p == nil {
		h.fail(c, http.StatusNotFound, "notfound")
		return nil, false
	}
	// viewer() returns 0 for anonymous, and 0 is a REAL value in the comparison
	// below — a row with user_id 0 would come out owned by everybody who is not
	// signed in. Nothing creates such a row today and no ownership column in
	// the live database held one when this was checked, but the schema only
	// says NOT NULL — it does not rule 0 out — so the rule lives in
	// pluginapi.OwnedBy rather than in each caller's memory.
	if !pluginapi.OwnedBy(p.UserID, h.viewer(c)) {
		// 404 rather than 403, for the same reason Show uses one.
		h.fail(c, http.StatusNotFound, "notfound")
		return nil, false
	}
	return p, true
}

// resolveItems fills each item's Release in ONE host lookup. Ids the host does
// not return stay nil and render as unavailable.
func (h *Handlers) resolveItems(ctx context.Context, items []*Item) {
	if len(items) == 0 || deps.LookupReleases == nil {
		return
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ReleaseID)
	}
	found, err := deps.LookupReleases(ctx, ids)
	if err != nil {
		return
	}
	for _, it := range items {
		if r, ok := found[it.ReleaseID]; ok {
			rr := r
			it.Release = &rr
		}
	}
}

func (h *Handlers) fillUsernames(ctx context.Context, rows []*Playlist) {
	if deps.LookupUsername == nil {
		return
	}
	// Small N (one page), and the seam is per-user, so a cache keeps a page of
	// one owner's playlists to a single lookup.
	seen := map[int64]string{}
	for _, p := range rows {
		if name, ok := seen[p.UserID]; ok {
			p.Username = name
			continue
		}
		if name, ok := deps.LookupUsername(ctx, p.UserID); ok {
			seen[p.UserID] = name
			p.Username = name
		}
	}
}
