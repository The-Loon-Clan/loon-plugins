package hitrun

import (
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Handlers serves the member page. One small type rather than free functions,
// so the store, the auth service and the live policy arrive together — the page
// must quote the SAME numbers the sweep applies, and a second copy taken
// somewhere else is how those two start disagreeing.
type Handlers struct {
	store  Store
	auth   core.AuthService
	tmpl   *template.Template
	policy func() Policy
}

func NewHandlers(st Store, auth core.AuthService, policy func() Policy) *Handlers {
	return &Handlers{store: st, auth: auth, policy: policy}
}

func (h *Handlers) SetTemplates(t *template.Template) { h.tmpl = t }

// render hands a fragment to the host's layout.
func (h *Handlers) render(c *gin.Context, title string, body template.HTML) {
	if fn := deps().RenderPage; fn != nil {
		fn(c, title, body)
		return
	}
	// Unreachable in practice — Provision refuses to mount the page without
	// the seam — but a plugin that assumed and was wrong would serve a blank
	// 200, so say something instead.
	c.String(http.StatusInternalServerError, "hitrun: no page renderer wired")
}

// relative formats a timestamp the way the rest of the site does, falling back
// to a plain date rather than to an empty cell.
func (h *Handlers) relative(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if fn := deps().RelativeTime; fn != nil {
		return fn(t)
	}
	return t.Format("2 Jan 2006")
}

// renderError shows the member a page rather than a stack trace, and keeps the
// detail for the log.
func (h *Handlers) renderError(c *gin.Context, err error) {
	c.String(http.StatusInternalServerError, "Could not load your seeding standing. Try again shortly.")
	_ = err
}
