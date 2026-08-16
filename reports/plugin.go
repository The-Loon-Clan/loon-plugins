package reports

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("reports", func() core.Plugin { return &Plugin{} })
}

// pageSize matches what the host page used, so an operator's sense of how deep
// the queue is does not shift underneath them.
const pageSize = 50

//go:embed templates/reports.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/reports.html"))

// Plugin serves the member-report triage queue.
type Plugin struct{}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "reports",
		Version:     "1.0.0",
		Description: "Member report queue — triage releases flagged as broken, mislabeled, or malware.",
		Processes:   []string{"web"},
	}
}

// vm is a struct rather than a map so a field the markup reads and the handler
// forgets is a render error instead of a silently empty cell.
type vm struct {
	Reports        []Report
	Total          int
	Resolved       bool
	Page           int
	PaginationHTML template.HTML
	CSRFToken      string
}

func (p *Plugin) Provision(c *core.Core) error {
	if c.Process != "web" && c.Process != "all" {
		return nil
	}
	if !deps.ok() {
		return fmt.Errorf("reports: SetDeps was not called with a full Deps before core.Boot")
	}

	if err := c.RegisterView(core.View{
		Slug:        "reports",
		Title:       "Member Reports",
		Slot:        core.SlotAdminPage,
		MinRole:     core.RoleAdmin,
		Description: "Releases members flagged as broken, mislabeled, or malware.",
		Nav:         core.NavHint{Group: "Community"},
		Render:      p.render,
		// The id travels in the form body, not the path. The host mounts
		// actions as POST /admin/p/<slug>/<name>, so a name containing ":id"
		// would register a gin path parameter -- which works, but a wildcard
		// conflict anywhere under /admin/p is a panic at BOOT, and taking the
		// site down is a steep price for a prettier URL.
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"resolve": p.resolve,
		},
	}); err != nil {
		return fmt.Errorf("reports: register view: %w", err)
	}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("reports: Core.Router.Engine() is nil")
	}
	// /admin/reports was this queue's address for its whole life and is linked
	// from the admin hub and from the daily digest's "triage at" line. Redirect
	// rather than break either.
	adm := engine.Group("/admin/reports")
	adm.Use(c.Auth.RequireUser(core.RoleAdmin)...)
	adm.GET("", func(gc *gin.Context) {
		gc.Redirect(http.StatusMovedPermanently, "/admin/p/reports?"+gc.Request.URL.RawQuery)
	})
	return nil
}

// csrf reads the host seam, tolerating a host that has not wired it — the
// form then posts tokenless and the host's middleware answers, which is the
// pre-seam behaviour rather than a new failure.
func csrf(c *gin.Context) string {
	if deps == nil || deps.CSRFToken == nil {
		return ""
	}
	return deps.CSRFToken(c)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

var _ core.Plugin = (*Plugin)(nil)

// render draws the queue. The host gates /admin/p on admin, so this does not
// re-derive the rule.
func (p *Plugin) render(c *gin.Context) (template.HTML, error) {
	resolved := c.Query("resolved") == "1"
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := deps.List(c.Request.Context(), resolved, pageSize, (page-1)*pageSize)
	if err != nil {
		return "", err
	}
	base := "/admin/p/reports?"
	if resolved {
		base = "/admin/p/reports?resolved=1&"
	}
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, "reports.html", vm{
		Reports:        rows,
		Total:          total,
		Resolved:       resolved,
		Page:           page,
		PaginationHTML: deps.RenderPagination(page, pageSize, total, base),
		CSRFToken:      csrf(c),
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// resolve clears one report and returns to the queue.
//
// Returning an empty fragment tells the host this action answered for itself —
// the redirect preserves which tab the operator was on, so clearing the tenth
// report does not bounce them back to page one of the other list.
func (p *Plugin) resolve(c *gin.Context) (template.HTML, error) {
	id, err := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("reports: bad id %q", c.PostForm("id"))
	}
	if err := deps.Resolve(c.Request.Context(), id, deps.ActingAdmin(c)); err != nil {
		return "", err
	}
	back := "/admin/p/reports"
	if c.PostForm("resolved") == "1" {
		back += "?resolved=1"
	}
	if pg := c.PostForm("page"); pg != "" {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		back += sep + "page=" + url.QueryEscape(pg)
	}
	c.Redirect(http.StatusFound, back)
	return "", nil
}
