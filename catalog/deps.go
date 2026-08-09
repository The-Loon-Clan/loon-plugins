package catalog

import "github.com/gin-gonic/gin"

// Deps are the host seams the settings section renders with. Optional as a
// whole — catalog predates the SetDeps convention and loon-demo-site
// blank-imports it with no wiring, so Provision must not refuse — but a host
// with CSRF middleware MUST wire CSRFToken: without it the toggle form
// submits an empty token and every category toggle 403s, which is exactly
// how this section behaved between its lift and the 2026-08-09 consistency
// audit finding it.
type Deps struct {
	// CSRFToken answers the host middleware's per-session token, rendered
	// into the toggle form's _csrf field.
	CSRFToken func(c *gin.Context) string
}

var deps *Deps

// SetDeps stages the host seams. Call before core.Boot.
func SetDeps(d Deps) { deps = &d }

// csrfToken is what the render uses: the wired seam, or empty for a host
// that never called SetDeps — no worse than the pre-seam behavior.
func csrfToken(c *gin.Context) string {
	if deps == nil || deps.CSRFToken == nil {
		return ""
	}
	return deps.CSRFToken(c)
}
