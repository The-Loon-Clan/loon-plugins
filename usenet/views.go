package usenet

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var viewFS embed.FS

const usenetURL = "/admin/p/usenet"

func (p *Plugin) registerViews(c *core.Core) error {
	t, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t

	// One view = one page = one card on the host's admin hub. Everything the
	// plugin can configure or show lives on /admin/p/usenet as a tab: config
	// (NNTP / Providers / Indexing / Newsgroups) plus the Crawlers dashboard and
	// the Filters blacklist, which render as embedded fragments. Their actions
	// are registered here so their forms post under the same page and land back
	// on their own tab.
	if err := c.RegisterView(core.View{
		Slug: "usenet", Title: "Usenet", Slot: core.SlotAdminPage,
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
			"provider":     p.actionSaveProvider,
			"provider-del": p.actionDeleteProvider,
			"group-tune":   p.actionTuneGroup,
			"group-move":   p.actionMoveGroup,
			"group-del":    p.actionDeleteGroup,
			"groups-purge": p.actionPurgeInactive,
			// Crawlers tab
			"crawl": func(gc *gin.Context) (template.HTML, error) {
				p.svc.TriggerCrawl()
				return settingsRedirect(gc, "msg", "crawl triggered")
			},
			"backfill": func(gc *gin.Context) (template.HTML, error) {
				p.svc.TriggerBackfill()
				return settingsRedirect(gc, "msg", "backfill triggered")
			},
			"reset-backfill": func(gc *gin.Context) (template.HTML, error) {
				name := gc.PostForm("name")
				if err := p.st.resetBackfillForGroup(gc.Request.Context(), name); err != nil {
					return settingsRedirect(gc, "err", err.Error())
				}
				return settingsRedirect(gc, "msg", "backfill re-armed for "+name)
			},
			// Filters tab
			"filter-add":    p.actionAddBlacklist,
			"filter-toggle": p.actionToggleBlacklist,
			"filter-del":    p.actionDeleteBlacklist,
			"filter-reset":  p.actionResetHits,
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
// settingsRedirect flashes a message and returns to the usenet admin page,
// reopening the tab the action belongs to (derived from the POST path) so a
// save doesn't bounce the operator back to the first tab.
func settingsRedirect(gc *gin.Context, key, msg string) (template.HTML, error) {
	dest := usenetURL + "?" + key + "=" + url.QueryEscape(msg)
	if tab := tabForAction(gc.Request.URL.Path); tab != "" {
		dest += "#" + tab
	}
	return redirect(gc, dest)
}

// tabForAction maps an action's POST path (/admin/p/usenet/<action>) to the tab
// it lives on, so the post-action redirect reopens it. Empty = the first (NNTP)
// tab, which is where the server/test actions belong.
func tabForAction(path string) string {
	switch {
	case strings.HasSuffix(path, "/provider"), strings.HasSuffix(path, "/provider-del"):
		return "providers"
	case strings.HasSuffix(path, "/knobs"):
		return "indexing"
	case strings.HasSuffix(path, "/group-tune"), strings.HasSuffix(path, "/group-move"),
		strings.HasSuffix(path, "/group-del"), strings.HasSuffix(path, "/groups-purge"),
		strings.HasSuffix(path, "/fetch-groups"), strings.HasSuffix(path, "/group"):
		return "newsgroups"
	case strings.HasSuffix(path, "/crawl"), strings.HasSuffix(path, "/backfill"),
		strings.HasSuffix(path, "/reset-backfill"):
		return "crawlers"
	case strings.HasSuffix(path, "/filter-add"), strings.HasSuffix(path, "/filter-toggle"),
		strings.HasSuffix(path, "/filter-del"), strings.HasSuffix(path, "/filter-reset"):
		return "filters"
	}
	return ""
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
