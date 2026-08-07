package store

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The store's three pages, owned by the plugin that owns the store.
//
// The HOST used to carry this markup, so the plugin could not change its own
// pages without editing the site — and the site could not drop them, which is
// why the store still looked present in prod after the Go moved out.
//
// Embedded, so a missing template is a build error here rather than a 500 at
// runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

// The one host helper this markup calls. Reimplemented rather than crossed
// the seam: it is pure and three lines, and unlike a shared hash there is
// nothing here that could drift into a different ANSWER.
var pageTmpl = template.Must(template.New("store").Funcs(template.FuncMap{
	"deref64": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
}).ParseFS(pageFS, "templates/*.html"))

// page renders one fragment and hands it to the host for chrome.
//
// CSRFToken is injected here rather than left to each caller: two of the three
// pages post forms, it comes from host middleware, and a page that forgot it
// would render a form that silently fails to submit.
// Named renderPage, not page: the history handler has a local `page`
// holding the page NUMBER, and a helper that shadows it compiles into
// something confusing rather than failing.
func renderPage(c *gin.Context, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CSRFToken"] = deps.CSRFToken(c)
	// Set here rather than in each handler: the tab strip is on every store
	// page, and a handler that forgot would drop the host's tabs on one page
	// only — the kind of gap you find by clicking, not by reading.
	data["ExtraTabs"] = extraTabs(c)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams, so a half-rendered page must not go out as a
		// 200 carrying its own top half.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}
