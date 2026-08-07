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
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var playlistMigrations embed.FS

func init() {
	core.RegisterPlugin("playlists", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams. SetDeps must be called before core.Boot; Provision
// fails loud otherwise, so a half-wired host cannot serve broken pages.
type Deps struct {
	// BaseData merges the host's page chrome (user, nav, CSRF, theme) into a
	// template data map. Required — these pages render inside the host layout.
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
	core     *core.Core
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "playlists",
		Version:     "0.1.0",
		Description: "User-curated collections of indexed releases.",
		Migrations:  playlistMigrations,
		Processes:   []string{"web"},
	}
}

const pageSize = 24

func (p *Plugin) Provision(c *core.Core) error {
	if deps.BaseData == nil || deps.PageOffset == nil || deps.Pagination == nil {
		return fmt.Errorf("playlists: SetDeps not called with BaseData, PageOffset and Pagination before core.Boot")
	}
	if deps.LookupReleases == nil {
		return fmt.Errorf("playlists: SetDeps needs LookupReleases — a playlist that cannot resolve its releases has nothing to show")
	}
	p.core = c

	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("playlists: no schema DB")
	}
	p.handlers = &Handlers{store: NewPGStore(db), auth: c.Auth, core: c}
	if err := declareEvents(c); err != nil {
		return err
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
		c.HTML(http.StatusInternalServerError, "playlists_index.html", deps.BaseData(c, gin.H{
			"Error": "Could not load playlists.",
		}))
		return
	}
	h.fillUsernames(ctx, rows)
	c.HTML(http.StatusOK, "playlists_index.html", deps.BaseData(c, gin.H{
		"Playlists":  rows,
		"Total":      total,
		"Pagination": deps.Pagination(page, pageSize, total, "/playlists"),
	}))
}

func (h *Handlers) Show(c *gin.Context) {
	ctx := c.Request.Context()
	p, err := h.store.BySlug(ctx, c.Param("slug"))
	if err != nil || p == nil {
		c.String(http.StatusNotFound, "playlist not found")
		return
	}
	me := h.viewer(c)
	// A private playlist is a 404, not a 403: a 403 confirms the slug exists,
	// which is the one thing "private" is meant to withhold.
	if !p.Public && p.UserID != me {
		c.String(http.StatusNotFound, "playlist not found")
		return
	}
	items, err := h.store.ListItems(ctx, p.ID)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load playlist")
		return
	}
	h.resolveItems(ctx, items)
	h.fillUsernames(ctx, []*Playlist{p})
	c.HTML(http.StatusOK, "playlist_view.html", deps.BaseData(c, gin.H{
		"Playlist": p,
		"Items":    items,
		"IsOwner":  me != 0 && me == p.UserID,
		"Saved":    c.Query("saved") == "1",
	}))
}

func (h *Handlers) New(c *gin.Context) {
	c.HTML(http.StatusOK, "playlist_form.html", deps.BaseData(c, gin.H{"Action": "Create"}))
}

func (h *Handlers) Create(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.HTML(http.StatusBadRequest, "playlist_form.html", deps.BaseData(c, gin.H{
			"Action": "Create", "Error": "A name is required.",
		}))
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
		c.HTML(http.StatusBadRequest, "playlist_form.html", deps.BaseData(c, gin.H{
			"Action": "Create", "Error": "That name is already taken. Try another.",
			"Name": name, "Description": p.Description, "CoverURL": p.CoverURL, "Public": p.Public,
		}))
	case err != nil:
		c.HTML(http.StatusInternalServerError, "playlist_form.html", deps.BaseData(c, gin.H{
			"Action": "Create", "Error": "Could not create the playlist.",
		}))
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
	c.HTML(http.StatusOK, "playlist_form.html", deps.BaseData(c, gin.H{
		"Action": "Save", "Playlist": p,
		"Name": p.Name, "Description": p.Description, "CoverURL": p.CoverURL, "Public": p.Public,
	}))
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
		c.String(http.StatusInternalServerError, "could not save")
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
		c.String(http.StatusInternalServerError, "could not delete")
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
		c.String(http.StatusInternalServerError, "could not add")
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
		c.String(http.StatusInternalServerError, "could not remove")
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
		c.String(http.StatusNotFound, "playlist not found")
		return nil, false
	}
	if p.UserID != h.viewer(c) {
		// 404 rather than 403, for the same reason Show uses one.
		c.String(http.StatusNotFound, "playlist not found")
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
