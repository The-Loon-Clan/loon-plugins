package seedlock

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// The member's own page: which torrents are locked to which host, and the
// button that takes one back.
//
// This page is not a nicety. The refusal a client shows says "clear the lock on
// the site", and a message that names an action the site does not offer is
// worse than no message — it sends somebody looking for a page that is not
// there.

// Deps is what the host supplies.
type Deps struct {
	// RenderPage wraps a fragment in the site's layout. Without it the page is
	// not mounted, and the plugin says so at boot rather than leaving the
	// refusal message pointing at nothing.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// CSRFToken supplies the double-submit token for the clear form.
	CSRFToken func(c *gin.Context) string
}

var (
	depsMu   sync.RWMutex
	hostDeps Deps
)

func SetDeps(d Deps) {
	depsMu.Lock()
	defer depsMu.Unlock()
	hostDeps = d
}

func deps() Deps {
	depsMu.RLock()
	defer depsMu.RUnlock()
	return hostDeps
}

func pageReady() bool { return deps().RenderPage != nil }

// Handlers serves the claims page.
type Handlers struct {
	plugin *Plugin
	auth   core.AuthService
	// db reads torrent NAMES from the tracker's schema. This plugin owns no
	// tables — a claim is a Redis key — so it takes the raw pool and qualifies
	// the one table it reads.
	db   *sqlx.DB
	tmpl *template.Template
}

func NewHandlers(p *Plugin, auth core.AuthService, db *sqlx.DB) *Handlers {
	return &Handlers{plugin: p, auth: auth, db: db}
}

func (h *Handlers) SetTemplates(t *template.Template) { h.tmpl = t }

type claimsVM struct {
	Rows       []claimRowVM
	WindowText string
	CSRF       string
	Message    string
}

type claimRowVM struct {
	InfoHash string
	Name     string
	Host     string
}

// ClaimsPage lists a member's locked torrents.
func (h *Handlers) ClaimsPage(c *gin.Context) { h.render(c, "") }

func (h *Handlers) render(c *gin.Context, msg string) {
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	ctx := c.Request.Context()

	claims, err := h.plugin.Claims(ctx, u.ID)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not read your seeding locks. Try again shortly.")
		return
	}
	vm := claimsVM{WindowText: h.plugin.cfg.LockWindow().String(), Message: msg}
	if fn := deps().CSRFToken; fn != nil {
		vm.CSRF = fn(c)
	}
	names := h.names(ctx, claims)
	for hash, cl := range claims {
		name := names[hash]
		if name == "" {
			// The torrent was removed while the claim outlived it. Show the
			// hash rather than an empty cell, which reads as a broken page.
			name = hash[:min(12, len(hash))] + "…"
		}
		vm.Rows = append(vm.Rows, claimRowVM{
			InfoHash: hash,
			Name:     name,
			// Masked here for the same reason it is masked in the refusal: a
			// member needs to recognise their own other machine, not read its
			// address back in full.
			Host: masked(cl.Host),
		})
	}

	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, "claims.html", vm); err != nil {
		c.String(http.StatusInternalServerError, "Could not read your seeding locks. Try again shortly.")
		return
	}
	if fn := deps().RenderPage; fn != nil {
		fn(c, "Seeding locks", template.HTML(buf.String()))
		return
	}
	c.String(http.StatusInternalServerError, "seedlock: no page renderer wired")
}

// names resolves torrent names in ONE query rather than per row.
func (h *Handlers) names(ctx context.Context, claims map[string]Claim) map[string]string {
	out := map[string]string{}
	if h.db == nil || len(claims) == 0 {
		return out
	}
	hashes := make([]string, 0, len(claims))
	for k := range claims {
		hashes = append(hashes, k)
	}
	q, args, err := sqlx.In(`SELECT info_hash, name FROM tracker.torrents WHERE info_hash IN (?)`, hashes)
	if err != nil {
		return out
	}
	rows, err := h.db.QueryContext(ctx, h.db.Rebind(q), args...)
	if err != nil {
		// A missing name is cosmetic; the claim and its clear button still
		// work, so this degrades rather than failing the page.
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var hash, name string
		if err := rows.Scan(&hash, &name); err == nil {
			out[hash] = name
		}
	}
	return out
}

// ClearAction releases one of the caller's own claims.
func (h *Handlers) ClearAction(c *gin.Context) {
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	hash := c.PostForm("info_hash")
	if hash == "" {
		h.render(c, "")
		return
	}
	// Scoped to the caller by construction: the user id comes from the session,
	// never from the form, so a member cannot clear somebody else's lock by
	// editing the request.
	if err := h.plugin.ClearClaim(c.Request.Context(), u.ID, hash); err != nil {
		c.String(http.StatusInternalServerError, "Could not clear that lock. Try again shortly.")
		return
	}
	h.render(c, "Lock cleared — your other client can take this torrent on its next announce.")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
