package messages

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The inbox and the admin message log, owned by the plugin that owns
// messaging. The HOST used to carry both, so the plugin could not change its
// own pages and the site could not delete them.

//go:embed templates/*.html
var pageFS embed.FS

// Editor options for the two composers, named so the difference between them
// is visible in one place.
var (
	composeEditor = map[string]any{
		"Name": "body", "Rows": 5, "Required": true,
		"Placeholder": "Write your message…",
	}
)

var pageTmpl *template.Template

// parseTemplates binds the seams the markup calls and parses the fragments.
// Done at Provision rather than package init because markdown and
// relativeTime are Deps functions and Deps does not exist until then — and a
// stubbed SANITISER that shipped would be worse than a missing one.
func parseTemplates() {
	if deps == nil || deps.Markdown == nil || deps.RelativeTime == nil {
		return
	}
	pageTmpl = template.Must(template.New("messages").Funcs(template.FuncMap{
		"markdown": func(s string) template.HTML { return deps.Markdown(s) },
		// preview is the sidebar's one-liner: the same pipeline as markdown,
		// flattened to plain text (see previewBody) — the row is a summary,
		// not a rendering surface.
		"preview":      func(s string) string { return previewBody(s, 0) },
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		// initial is rune-safe on purpose: `slice name 0 1` counts BYTES, so
		// a multi-byte username renders half a rune and an empty one is a
		// template ERROR — which aborts the streamed page mid-render.
		"initial": func(s string) string {
			for _, r := range s {
				return string(r)
			}
			return "?"
		},
		// dict is reimplemented: it builds a map, and unlike the two above
		// there is no answer here that could drift.
		"dict": func(values ...any) map[string]any {
			if len(values)%2 != 0 {
				return map[string]any{}
			}
			m := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return map[string]any{}
				}
				m[k] = values[i+1]
			}
			return m
		},
	}).ParseFS(pageFS, "templates/*.html"))
}

// render executes one fragment and hands it to the host for chrome. The keys
// every page here needs are injected rather than repeated per call site.
//
// ViewerID is one of them, and deliberately so. The markup decides which side
// of the conversation a message sits on with `eq $me .SenderID`; data is a
// map, so a call site that forgot the key would render every message as the
// other person's, with nothing logged. A method on Handlers can resolve the
// viewer itself, and then no call site can forget.
func (h *Handlers) render(c *gin.Context, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CSRFToken"] = deps.CSRFToken(c)
	if u := h.currentUser(c); u != nil {
		data["ViewerID"] = u.ID
	} else {
		data["ViewerID"] = 0
	}
	if _, ok := data["EditorHTML"]; !ok {
		data["EditorHTML"] = deps.RenderEditor(composeEditor)
	}

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}
