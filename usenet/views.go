package usenet

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
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
	// (Providers / Indexing / Newsgroups) plus the Crawlers dashboard and the
	// Filters blacklist, which render as embedded fragments. Their actions are
	// registered here so their forms post under the same page and land back on
	// their own tab.
	if err := c.RegisterView(core.View{
		Slug: "usenet", Title: "Usenet", Slot: core.SlotAdminPage,
		Description: "Providers, indexing, newsgroups, crawlers, jobs + filters.",
		// Hub-placement hint: hosts that have an "Operations" admin section
		// file the card there (badged as plugin-provided); hosts that don't
		// keep it in their generic Plugins section.
		Nav: core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderSettings(gc.Request.Context(), gc.Query("gq"), gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"knobs":          p.actionSaveKnobs,
			"fetch-groups":   p.actionFetchGroups,
			"group":          p.actionToggleGroup,
			"provider":       p.actionSaveProvider,
			"provider-del":   p.actionDeleteProvider,
			"provider-test":  p.actionTestProvider,
			"provider-probe": p.actionProbeProvider,
			"group-tune":     p.actionTuneGroup,
			"group-move":     p.actionMoveGroup,
			"group-del":      p.actionDeleteGroup,
			"groups-purge":   p.actionPurgeInactive,
			// Crawlers tab. On a split deployment these buttons run in the WEB
			// process, whose trigger func-vars are nil (the jobs live in the
			// worker) — TriggerCrawl() here used to be a silent no-op. Relay
			// through the shared settings table instead; the worker's telemetry
			// tick consumes it within ~5s (telemetry_publish.go).
			"crawl":    p.actionTrigger("crawl", "crawl"),
			"backfill": p.actionTrigger("backfill", "backfill"),
			// Jobs tab: one Run-now per job. Distinct run-* paths (rather than
			// reusing /crawl) so the post-action redirect returns to #jobs.
			"run-crawl":    p.actionTrigger("crawl", "crawl"),
			"run-backfill": p.actionTrigger("backfill", "backfill"),
			"run-build":    p.actionTrigger("build", "build"),
			"run-tagfill":  p.actionTrigger("tagfill", "tag fill"),
			"run-prune":    p.actionTrigger("prune", "prune"),
			"run-health":   p.actionTrigger("health", "health check"),
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

	// Jobs widget: a richer card for the "Usenet" job group (all six pipeline
	// jobs) on the host jobs page.
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

// actionTrigger builds the Crawl-now / Backfill-now handler. In the process
// that runs the jobs the trigger fires directly; anywhere else it is written
// to the settings table for the worker's telemetry tick to pick up — with a
// flash that says so, because "clicked and nothing visibly happened" is
// exactly the confusion the relay exists to fix.
func (p *Plugin) actionTrigger(kind, label string) func(*gin.Context) (template.HTML, error) {
	return func(gc *gin.Context) (template.HTML, error) {
		if p.runsJobs {
			if !p.fireTrigger(kind) {
				return settingsRedirect(gc, "err", "unknown job trigger: "+kind)
			}
			return settingsRedirect(gc, "msg", label+" started")
		}
		req := kind + ":" + strconv.FormatInt(time.Now().Unix(), 10)
		if err := p.st.setSetting(gc.Request.Context(), triggerRequestKey, req); err != nil {
			return settingsRedirect(gc, "err", err.Error())
		}
		return settingsRedirect(gc, "msg", label+" requested — the worker starts it within ~5s; watch its log on the Jobs tab")
	}
}

// redirect answers the action with a 303; the empty fragment tells the host
// the response is already written.
func redirect(gc *gin.Context, to string) (template.HTML, error) {
	gc.Redirect(http.StatusSeeOther, to)
	return "", nil
}

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
// it lives on, so the post-action redirect reopens it. Empty = the first
// (Providers) tab.
func tabForAction(path string) string {
	switch {
	case strings.HasSuffix(path, "/provider"), strings.HasSuffix(path, "/provider-del"),
		strings.HasSuffix(path, "/provider-test"), strings.HasSuffix(path, "/provider-probe"):
		return "providers"
	case strings.HasSuffix(path, "/knobs"):
		return "indexing"
	case strings.HasSuffix(path, "/group-tune"), strings.HasSuffix(path, "/group-move"),
		strings.HasSuffix(path, "/group-del"), strings.HasSuffix(path, "/groups-purge"),
		strings.HasSuffix(path, "/fetch-groups"), strings.HasSuffix(path, "/group"):
		return "newsgroups"
	case strings.HasSuffix(path, "/run-crawl"), strings.HasSuffix(path, "/run-backfill"),
		strings.HasSuffix(path, "/run-build"), strings.HasSuffix(path, "/run-tagfill"),
		strings.HasSuffix(path, "/run-prune"), strings.HasSuffix(path, "/run-health"):
		return "jobs"
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

// fmtDateTime is the coverage line's watermark stamp — date alone hides how
// fresh today's crawl is.
func fmtDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// fmtComma renders 409395881 as "409,395,881" — the legacy page's article
// counts, kept legible.
func fmtComma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}
