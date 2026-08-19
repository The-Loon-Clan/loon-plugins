package downloads

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The two pages: a member's setup page, and the staff view of what has come
// back.
//
// The setup page is the feature. An endpoint nobody can configure is an
// endpoint nobody uses, and the request this plugin answers was explicitly for
// a PREBUILT script — so the page's job is to hand over a file that already
// works, not to document how to write one.

func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug:   "downloads",
		Title:  "Download reports",
		Slot:   core.SlotSitePage,
		Public: false, // a member's own key is on this page
		Nav:    core.NavHint{Group: "Account"},
		Render: p.memberPage,
	}); err != nil {
		return fmt.Errorf("downloads: register member view: %w", err)
	}
	if err := c.RegisterView(core.View{
		Slug:    "downloads",
		Title:   "Download reports",
		Slot:    core.SlotAdminPage,
		MinRole: core.RoleMod,
		Nav:     core.NavHint{Group: "Operations"},
		Render:  p.adminPage,
	}); err != nil {
		return fmt.Errorf("downloads: register admin view: %w", err)
	}
	return nil
}

// scriptRoute is where the generated script is served. A query parameter picks
// the member's key; it is not in the path, so the URL is not something a proxy
// log turns into a credential leak by accident.
const scriptRoute = "/p/downloads/script"

// memberPage renders the setup instructions and the download link.
func (p *Plugin) memberPage(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		return "", nil
	}
	data := map[string]any{
		"Endpoint":  ReportPath,
		"Script":    scriptRoute,
		"Available": p.keys != nil,
		"Recheck":   p.recheck != nil,
		"Matching":  p.grabs != nil,
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "downloads_member.html", data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// adminPage shows what members' clients have said.
func (p *Plugin) adminPage(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	rows, err := p.st.Recent(ctx, 100)
	if err != nil {
		return "", err
	}
	failed, okCount, err := p.st.Counts(ctx)
	if err != nil {
		return "", err
	}
	data := map[string]any{
		"Rows":      rows,
		"Failed":    failed,
		"OK":        okCount,
		"Total":     failed + okCount,
		"Endpoint":  ReportPath,
		"Available": p.keys != nil,
		"Recheck":   p.recheck != nil,
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "downloads_admin.html", data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// serveScript hands over report.py with this site's URL and the member's own
// key already substituted in.
//
// Generated rather than a static file with instructions to edit it, because
// "download this, then open it in a text editor and paste two values" is where
// most people stop. The placeholders in scripts/report.py are what make the
// file valid Python either way: it runs and explains itself if somebody grabs
// it from the repo instead.
func (p *Plugin) serveScript(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	key := strings.TrimSpace(c.Query("key"))
	if key == "" {
		// No key means no working script. Refused rather than served with a
		// placeholder, because a file that looks configured and is not is a
		// support question.
		c.String(http.StatusBadRequest, "no API key supplied")
		return
	}
	raw, err := scriptFS.ReadFile("scripts/report.py")
	if err != nil {
		c.String(http.StatusInternalServerError, "script unavailable")
		return
	}
	base := strings.TrimRight(p.siteURL(c), "/")
	out := strings.ReplaceAll(string(raw), "__SITE_URL__", base)
	out = strings.ReplaceAll(out, "__API_KEY__", key)

	c.Header("Content-Disposition", `attachment; filename="report.py"`)
	c.Data(http.StatusOK, "text/x-python; charset=utf-8", []byte(out))
}

// siteURL is the absolute base the script posts back to.
//
// Taken from the REQUEST rather than from config, because this plugin has no
// config of its own and the host that serves the page is by definition the
// host the member is talking to. Behind a reverse proxy the forwarded headers
// are what the host already trusts for every other absolute URL it builds.
func (p *Plugin) siteURL(c *gin.Context) string {
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if fp := c.GetHeader("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	}
	return scheme + "://" + c.Request.Host
}
