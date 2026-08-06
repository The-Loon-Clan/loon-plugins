package news

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The news pages, owned by the plugin that owns news.
//
// The HOST used to carry all four, so the plugin could not change its own
// pages and the site could not delete them. Embedded here, so a missing
// template is a build error rather than a 500 at runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/*.html"))

// render executes one fragment and hands it to the host for chrome.
func render(c *gin.Context, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}
