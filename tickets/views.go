package tickets

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The support pages, owned by the plugin that owns support.
//
// The HOST used to carry all four, so the plugin could not change its own
// pages without editing the site — and the site could not drop them, which is
// why tickets still reads as present in prod after the Go moved out.

//go:embed templates/*.html
var pageFS embed.FS

// The markdown helper is bound at PROVISION rather than package init,
// because it is a Deps function and Deps is not set until then. A template
// parsed at init would have to stub it, and a stubbed sanitiser that shipped
// would be worse than a missing one.
var pageTmpl *template.Template

func parseTemplates() {
	// Only the modern path renders from here. A legacy host renders by
	// template name out of its own directory, where its own FuncMap already
	// supplies markdown.
	if deps == nil || deps.Markdown == nil {
		return
	}
	pageTmpl = template.Must(template.New("tickets").Funcs(template.FuncMap{
		"markdown": func(s string) template.HTML { return deps.Markdown(s) },
	}).ParseFS(pageFS, "templates/*.html"))
}

// Editor option sets, named so the two call sites cannot drift apart by
// accident and so the difference between them is visible in one place.
var (
	newTicketEditor = map[string]any{
		"Name": "body", "Rows": 6, "Required": true,
		"Placeholder": "Describe your issue in detail…",
	}
	replyEditor = map[string]any{
		"Name": "body", "Rows": 5, "Required": true,
		"Placeholder": "Type your reply…",
	}
)

// render executes one fragment and hands it to the host for chrome.
//
// status is explicit because three of these pages re-render themselves on a
// validation failure and must say so — a 200 carrying "your ticket was
// rejected" is a lie to anything reading the status line.
func render(c *gin.Context, status int, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	// Legacy contract: render by NAME from the host's template directory. See
	// Deps.BaseData for why this branch exists and when it goes.
	if deps.RenderPage == nil {
		c.HTML(status, name, deps.BaseData(c, data))
		return
	}
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams, so a partly-rendered page must not go out as
		// if it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, status, title, template.HTML(sb.String()))
}
