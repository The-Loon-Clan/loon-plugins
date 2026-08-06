package logs

import (
	"embed"
	"html/template"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// The /admin/logs page, owned by the plugin that owns log search.
//
// The HOST used to carry this markup — 235 lines in web/templates, including
// this plugin's own CSS and its entire live-tail client — so the plugin could
// not change its own page without editing the site. It is a loon SlotAdminPage
// now: the fragment below is the whole page body and the host supplies only
// the chrome (plugin_page.html — head, navbar, theme, bootstrap, footer).
//
// Embedded rather than added to the host's template glob: the point is that
// the host stops carrying this plugin's markup, so the fragment travels with
// the code that renders it. It also makes a missing template a build error
// here rather than a 500 at runtime in the host.

//go:embed templates/logs.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/logs.html"))

// renderPage is the SlotAdminPage Render. The host mounts it at
// /admin/p/logs behind the admin gate, so this does not re-derive the rule.
func (p *Plugin) renderPage(c *gin.Context) (template.HTML, error) {
	res, bucket, err := p.handlers.run(c)
	if err != nil {
		return "", err
	}
	rawQ := c.Query("q")
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, "logs.html", map[string]any{
		"Query":     rawQ,
		"Logs":      res.Rows,
		"Total":     res.Total,
		"OpFacets":  res.Ops,
		"SevFacets": res.Severities,
		"HistoBars": buildBars(res.Histogram, bucket),
		// The pager's links have to carry the query back, and the page now
		// lives under /admin/p — the JSON endpoints below it deliberately did
		// not move, so this is the one URL that changed.
		"PaginationHTML": deps.RenderPagination(res.Page, logsPageSize, res.Total,
			"/admin/p/logs?q="+url.QueryEscape(rawQ)+"&"),
		"Page":       res.Page,
		"TotalPages": res.TotalPages,
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}
