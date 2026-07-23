package usenet

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var viewFS embed.FS

const (
	settingsURL = "/admin/settings"
	crawlersURL = "/admin/p/crawlers"
	filtersURL  = "/admin/p/filters"
)

func (p *Plugin) registerViews(c *core.Core) error {
	t, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t

	if err := c.RegisterView(core.View{
		Slug: "usenet", Title: "Usenet", Slot: core.SlotAdminSettings,
		Render: func(gc *gin.Context) (template.HTML, error) {
			srv, _, _ := p.st.getServer(gc.Request.Context())
			return p.renderSettings(gc.Request.Context(), srv, gc.Query("gq"), gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"server":       p.actionSaveServer,
			"test":         p.actionTestServer,
			"knobs":        p.actionSaveKnobs,
			"fetch-groups": p.actionFetchGroups,
			"group":        p.actionToggleGroup,
		},
	}); err != nil {
		return err
	}

	if err := c.RegisterView(core.View{
		Slug: "crawlers", Title: "Crawlers", Slot: core.SlotAdminPage,
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderCrawlers(gc.Request.Context(), gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"crawl": func(gc *gin.Context) (template.HTML, error) {
				p.svc.TriggerCrawl()
				return redirect(gc, crawlersURL+"?msg="+url.QueryEscape("crawl triggered"))
			},
			"backfill": func(gc *gin.Context) (template.HTML, error) {
				p.svc.TriggerBackfill()
				return redirect(gc, crawlersURL+"?msg="+url.QueryEscape("backfill triggered"))
			},
			"reset-backfill": func(gc *gin.Context) (template.HTML, error) {
				name := gc.PostForm("name")
				if err := p.st.resetBackfillForGroup(gc.Request.Context(), name); err != nil {
					return redirect(gc, crawlersURL+"?err="+url.QueryEscape(err.Error()))
				}
				return redirect(gc, crawlersURL+"?msg="+url.QueryEscape("backfill re-armed for "+name))
			},
		},
	}); err != nil {
		return err
	}

	// Filters: the blacklist an operator authors, and the hit counters that say
	// whether any of it — theirs or the shipped junk rules — is doing anything.
	if err := c.RegisterView(core.View{
		Slug: "filters", Title: "Filters", Slot: core.SlotAdminPage,
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderFilters(gc.Request.Context(), gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"add":    p.actionAddBlacklist,
			"toggle": p.actionToggleBlacklist,
			"delete": p.actionDeleteBlacklist,
			"reset":  p.actionResetHits,
		},
	}); err != nil {
		return err
	}

	// Jobs widget: a richer card for the "Usenet" job group (crawler +
	// backfill) on the host jobs page. The "NZB" group keeps the host default —
	// the two side by side demonstrate default vs override.
	return c.RegisterView(core.View{
		Slug: "usenet-jobs", Title: "Usenet jobs", Slot: core.SlotJobsWidget, Anchor: "Usenet",
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderJobsWidget(gc.Request.Context())
		},
	})
}

func (p *Plugin) frag(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// redirect answers the action with a 303; the empty fragment tells the host
// the response is already written.
func redirect(gc *gin.Context, to string) (template.HTML, error) {
	gc.Redirect(http.StatusSeeOther, to)
	return "", nil
}

// settingsRedirect lands back on the usenet section of the settings page.
func settingsRedirect(gc *gin.Context, key, msg string) (template.HTML, error) {
	return redirect(gc, settingsURL+"?"+key+"="+url.QueryEscape(msg)+"#s-usenet")
}

// coverCellCount is the resolution of the coverage sparkline. Enough slices to
// see where the holes are, few enough to stay legible and keep the markup a
// fixed size regardless of how fragmented backfill left a group.
const coverCellCount = 48

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}
