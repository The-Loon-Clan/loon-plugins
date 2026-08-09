package roadmap

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The seven pages, embedded.
//
//go:embed templates/*.html
var pageFS embed.FS

// pageTmpl is parsed in Provision — the FuncMap binds deps.RelativeTime.
var pageTmpl *template.Template

func parseTemplates() error {
	t, err := template.New("roadmap").Funcs(template.FuncMap{
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		// Exact copies of the host FuncMap entries these pages rendered
		// with — parity is the lift's contract.
		"initial": initialRune,
		"deref": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		"deref64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"int64": func(v uint64) int64 { return int64(v) },
		"inc":   func(n int) int { return n + 1 },
		"add":   func(a, b int) int { return a + b },
		"sub":   func(a, b int64) int64 { return a - b },
		"list":  func(items ...any) []any { return items },
		"dict": func(values ...interface{}) map[string]interface{} {
			if len(values)%2 != 0 {
				return map[string]interface{}{}
			}
			m := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return map[string]interface{}{}
				}
				m[k] = values[i+1]
			}
			return m
		},
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("roadmap: parse templates: %w", err)
	}
	pageTmpl = t
	return nil
}

// render draws one page: fragment from the plugin's set, chrome from the
// host. The viewer keys the fragments read (CSRFToken, Username, UserID,
// IsAdmin) are injected here, once. ActiveNav stays per-handler data — this
// surface spans three nav sections (support, community, admin).
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

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		c.String(500, "this page failed to render")
		return
	}
	h.deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// requireAuthed answers false (and a 401 the /flow JS understands) for an
// anonymous request.
func (h *Handlers) requireAuthed(c *gin.Context) bool {
	if h.deps.Viewer(c) == nil {
		jsonError(c, 401, "sign in required")
		return false
	}
	return true
}

// sanitizeForum is the seam alias the ported markdown pipeline calls.
func sanitizeForum(html string) string { return deps.SanitizeForum(html) }

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

// initialRune — the first RUNE of a name, never a byte slice.
func initialRune(name string) string {
	if name == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError && size <= 1 {
		return ""
	}
	return name[:size]
}
