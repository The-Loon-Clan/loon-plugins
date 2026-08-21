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
// fail renders a refusal in the site's chrome instead of a bare string.
//
// No status parameter, because this plugin's render has none — its pages all
// answer 200 and the host's RenderPage signature here carries no status
// either. So a failure renders the words and returns 200, which is worse than
// a 500 and better than plain text; fixing it means widening the seam.
func (h *Handlers) fail(c *gin.Context, reason string) {
	render(c, "News", "news_error.html", gin.H{"Reason": reason})
}

func render(c *gin.Context, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	// One place, all four pages: whether the fragment draws its own <h1>
	// (chrome supplies none on this host) and what it says. See
	// Deps.HeadingInFragment for why hosts differ.
	data["ShowHeading"] = deps.HeadingInFragment
	data["PageHeading"] = title
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}
