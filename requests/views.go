package requests

import (
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The board's two pages, embedded so a missing template is a build error in
// this repo rather than a runtime 500 in a host.
//
//go:embed templates/*.html
var pageFS embed.FS

// pageTmpl is parsed in Provision, not init, because the FuncMap binds
// deps.Markdown — the host's sanitising renderer. A stubbed sanitiser that
// shipped would be worse than a missing one.
var pageTmpl *template.Template

func parseTemplates() error {
	t, err := template.New("requests").Funcs(template.FuncMap{
		// The host's sanitising markdown renderer, via the seam.
		"markdown": func(s string) template.HTML { return deps.Markdown(s) },
		// Pure helpers, reimplemented locally: a copy can only disagree
		// loudly (wrong number on the page), unlike a sanitiser.
		"safeHTML":   func(s string) template.HTML { return template.HTML(s) },
		"deref":      derefInt,
		"deref64":    derefInt64,
		"derefBool":  func(p *bool) bool { return p != nil && *p },
		"int64":      func(v uint64) int64 { return int64(v) },
		"add":        func(a, b int) int { return a + b },
		"shortNum":   shortNum,
		"truncate":   truncateStr,
		"contains":   strings.Contains,
		"formatSize": formatSize,
		"splitComma": splitComma,
		// Resolves a stored backlog slug to its label, hint and colour. A
		// function rather than three nested {{if eq}} blocks in the
		// template, so the wording lives next to the rule that sets it.
		"backlogLabel": BacklogLabel,
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("requests: parse templates: %w", err)
	}
	pageTmpl = t
	return nil
}

// render draws one page: fragment from the plugin's own set, chrome from the
// host. The viewer keys every fragment needs (CSRFToken, Username, UserID,
// IsAdmin) are injected here, once, so no call site can forget them — the
// lifted templates read the same keys they read under the host's BaseData.
func (h *Handlers) render(c *gin.Context, status int, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CSRFToken"] = h.deps.CSRFToken(c)
	if v := h.deps.Viewer(c); v != nil {
		data["Username"] = v.Username
		data["UserID"] = v.ID
		data["IsAdmin"] = v.Mod
	} else {
		data["Username"] = ""
		data["UserID"] = 0
		data["IsAdmin"] = false
	}
	data["NzbCardCSS"] = h.deps.NzbCardCSS()

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// Never stream a partial page: report and fail whole.
		c.String(500, "this page failed to render")
		return
	}
	h.deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// ── Pure template helpers (local copies of host FuncMap entries) ────────────

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// shortNum, formatSize and splitComma are EXACT copies of the host FuncMap
// entries the original templates rendered with — the lift's promise is that
// a score pill or size cell shows the same characters it showed before.
// (The first cut here "improved" them and the adversarial review caught
// three visible divergences; parity is the contract, preference is not.)

func shortNum(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// truncateStr is the ONE deliberate divergence from the host helper: the
// host cuts at n BYTES (s[:n]), which lands mid-rune on CJK notes and
// renders U+FFFD at the cut. Cutting at n runes shows a few more characters
// for multi-byte text and can never manufacture mojibake — the same reason
// the cohort's `initial` helper is rune-safe.
func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func formatSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(1<<10))
	case b > 0:
		return fmt.Sprintf("%d B", b)
	default:
		return "—"
	}
}

func splitComma(s string) []string { return strings.Split(s, ",") }

// pageOffset converts a 1-based page to a query offset, clamping hostile
// input.
func pageOffset(page, pageSize int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * pageSize
}

// jsonOK / jsonError — the site-wide JSON envelope, copied rather than
// imported: six lines each, and the shapes must match the origin site byte
// for byte because the same JavaScript reads them.
func jsonOK(c *gin.Context, extras gin.H) {
	out := gin.H{"ok": true}
	for k, v := range extras {
		out[k] = v
	}
	c.JSON(200, out)
}

func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"ok": false, "error": msg})
}
