package chat

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The /chat page, owned by the plugin that owns chat.
//
// The plugin always served the route; the HOST owned the markup — 400+ lines
// of chat.html in web/templates, including this plugin's own CSS and its
// entire SSE client. So the plugin could not change its own page without
// editing the host. It is a loon SlotSitePage: the fragment is the whole page
// body, and the host supplies only the chrome (plugin_page.html — head,
// navbar, theme, bootstrap, footer).

//go:embed templates/chat.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/chat.html"))

// pageVM is a struct, not a map, on purpose: a map answers a missing key with
// the empty value and no error. IsAnon is the reason it matters here — the
// page has always read it and nothing has ever supplied it, so the anonymous
// branch (a "log in to chat" prompt instead of the composer) was unreachable.
// Harmless today because the host gates the page on a signed-in account, but
// it would have come back the moment the page was made public.
type pageVM struct {
	DiscordInviteURL string
	IsAnon           bool
}

// renderPage is the SlotSitePage Render.
//
// The host checks the view's MinRole before calling, so anyone reaching here is
// allowed to see the page — this does not re-derive the rule.
func (p *Plugin) renderPage(c *gin.Context) (template.HTML, error) {
	var sb strings.Builder
	if err := pageTmpl.Execute(&sb, pageVM{
		DiscordInviteURL: deps.InviteURL(c.Request.Context()),
		IsAnon:           deps.Viewer(c) == nil,
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}
