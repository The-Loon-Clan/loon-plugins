package usenet

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The plugin owns its admin UI through loon's view slots:
//
//   - a SETTINGS SECTION on the host's aggregated /admin/settings page —
//     server credentials + the indexing knobs (retention, batch sizes, caps;
//     persisted in the plugin's settings table, applied next run) + the
//     newsgroup picker;
//   - the CRAWLERS status page (coverage bars, live job activity, controls);
//   - a JOBS WIDGET that overrides the default table for its "Usenet" job
//     group on the host jobs page with a richer card.
//
// Each Render returns an HTML fragment from the embedded templates; the HOST
// wraps it in its own layout/nav/theme.

//go:embed templates/*.html
var viewFS embed.FS

const (
	settingsURL = "/admin/settings"
	crawlersURL = "/admin/p/crawlers"
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
				_ = p.st.resetBackfill(gc.Request.Context(), name)
				return redirect(gc, crawlersURL+"?msg="+url.QueryEscape("backfill re-armed for "+name))
			},
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

// ── settings section ────────────────────────────────────────────────

// knob is one editable numeric setting row in the settings form.
type knob struct {
	Key   string
	Label string
	Value int
	Help  string
}

func (p *Plugin) knobs(ctx context.Context) []knob {
	cfg := p.effective(ctx)
	return []knob{
		{"retention_days", "Retention / backfill depth (days)", cfg.RetentionDays, "keep — and backfill — the last N days; raise it to pull more history, backfill stops once caught up to this horizon"},
		{"crawl_interval_min", "Crawl interval (min)", cfg.CrawlIntervalMin, "how often to crawl + build (applies next cycle)"},
		{"batch", "Overview batch size", cfg.Batch, "article-number span per NNTP OVER request"},
		{"max_groups", "Max groups per run", cfg.MaxGroups, "cap active groups crawled per pass"},
		{"max_articles_per_group", "First-pass article cap", cfg.MaxArticlesPerGroup, "cap a new group's initial volume"},
		{"backfill_interval_min", "Backfill interval (min)", cfg.BackfillIntervalMin, "how often to pull history (applies next cycle)"},
		{"backfill_batches_per_run", "Backfill batches per run", cfg.BackfillBatchesPerRun, "how much history each backfill pass pulls"},
		{"staging_prune_hours", "Staging prune horizon (hrs)", cfg.StagingPruneHours, "drop staged articles older than this that never completed into an NZB (default 6)"},
		{"staging_max_rows", "Staging soft cap (rows)", cfg.StagingMaxRows, "back-pressure denominator: backfill yields as staged rows approach this (default 2,000,000)"},
	}
}

func (p *Plugin) renderSettings(ctx context.Context, srv pluginapi.Server, gq, msg, errMsg string) (template.HTML, error) {
	groups, _ := p.st.allGroups(ctx, gq, 300)
	total, _ := p.st.groupCount(ctx)
	return p.frag("settings.html", map[string]any{
		"Server": srv, "Knobs": p.knobs(ctx), "SkipBackfill": p.effective(ctx).SkipBackfill,
		"Groups": groups, "GroupQuery": gq,
		"GroupTotal": total, "Shown": len(groups),
		"Msg": msg, "Err": errMsg,
	})
}

func formServer(gc *gin.Context) pluginapi.Server {
	port, _ := strconv.Atoi(gc.PostForm("port"))
	if port == 0 {
		port = 119
	}
	tls := gc.PostForm("tls")
	return pluginapi.Server{
		Host:     strings.TrimSpace(gc.PostForm("host")),
		Port:     port,
		TLS:      tls == "on" || tls == "true",
		Username: gc.PostForm("username"),
		Password: gc.PostForm("password"),
		Enabled:  true,
	}
}

func (p *Plugin) actionSaveServer(gc *gin.Context) (template.HTML, error) {
	if err := p.st.saveServer(gc.Request.Context(), formServer(gc)); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", "server saved")
}

// actionTestServer re-renders the fragment with the SUBMITTED values (not a
// redirect) so the form keeps everything typed, whatever the result.
func (p *Plugin) actionTestServer(gc *gin.Context) (template.HTML, error) {
	srv := formServer(gc)
	if err := testConnect(srv); err != nil {
		return p.renderSettings(gc.Request.Context(), srv, "", "", "connection failed: "+err.Error())
	}
	return p.renderSettings(gc.Request.Context(), srv, "", "connection ok — click Save to keep it", "")
}

// actionSaveKnobs persists the numeric settings; they apply on each job's next
// run (effective() overlays them onto the config defaults).
func (p *Plugin) actionSaveKnobs(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	var cfg Config
	for key := range cfg.knobFields() {
		raw := strings.TrimSpace(gc.PostForm(key))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return settingsRedirect(gc, "err", key+" must be a positive number")
		}
		if err := p.st.setSetting(ctx, key, raw); err != nil {
			return settingsRedirect(gc, "err", err.Error())
		}
	}
	for key := range cfg.boolFields() {
		val := "false"
		if v := gc.PostForm(key); v == "on" || v == "true" {
			val = "true"
		}
		if err := p.st.setSetting(ctx, key, val); err != nil {
			return settingsRedirect(gc, "err", err.Error())
		}
	}
	return settingsRedirect(gc, "msg", "settings saved — applied on each job's next run")
}

func (p *Plugin) actionFetchGroups(gc *gin.Context) (template.HTML, error) {
	n, err := p.svc.FetchGroups(gc.Request.Context())
	if err != nil {
		return settingsRedirect(gc, "err", "fetch failed: "+err.Error())
	}
	return settingsRedirect(gc, "msg", "fetched "+strconv.Itoa(n)+" new group(s)")
}

func (p *Plugin) actionToggleGroup(gc *gin.Context) (template.HTML, error) {
	_ = p.st.setGroupActive(gc.Request.Context(), gc.PostForm("name"), gc.PostForm("active") == "true")
	dest := settingsURL + "#s-usenet"
	if gq := gc.PostForm("gq"); gq != "" {
		dest = settingsURL + "?gq=" + url.QueryEscape(gq) + "#s-usenet" // keep the current group search
	}
	return redirect(gc, dest)
}

// ── crawlers (status) view ──────────────────────────────────────────

type crawlerGroupVM struct {
	Name         string
	NZBs, Staged int
	Cover        pluginapi.CoverageBar
	FwdDate      string
	BackDate     string
	BackfillDone bool
	Remaining    int64
	LastCrawl    string
}

type crawlerJobVM struct {
	Name     string
	Status   string
	Activity string
	Running  bool
}

func (p *Plugin) renderCrawlers(ctx context.Context, msg, errMsg string) (template.HTML, error) {
	stats, err := p.st.stats(ctx)
	if err != nil {
		return "", err
	}
	groups := make([]crawlerGroupVM, len(stats.Groups))
	for i, g := range stats.Groups {
		vm := crawlerGroupVM{
			Name: g.Name, NZBs: g.NZBs, Staged: g.Staged,
			Cover: g.Coverage(), BackfillDone: g.BackfillDone,
			FwdDate: fmtDate(g.HighWatermarkDate), BackDate: fmtDate(g.BackWatermarkDate),
			LastCrawl: fmtTime(g.LastCrawl),
		}
		if !g.BackfillDone && g.BackWatermark > g.ServerLow {
			vm.Remaining = g.BackWatermark - g.ServerLow
		}
		groups[i] = vm
	}
	jobs, running := p.jobVMs()
	builder, err := p.st.builderInfo(ctx, 15)
	if err != nil {
		return "", err
	}
	return p.frag("crawlers.html", map[string]any{
		"Stats": stats, "Groups": groups, "Jobs": jobs, "Builder": builder,
		"AutoRefresh": running, "Msg": msg, "Err": errMsg,
	})
}

// jobVMs snapshots this plugin's own jobs so the page shows what each is doing.
func (p *Plugin) jobVMs() (jobs []crawlerJobVM, anyRunning bool) {
	mine := map[string]bool{
		"Usenet Crawler": true, "Usenet Backfill": true,
		"NZB Builder": true, "NZB Tag Fill": true, "NZB Prune": true,
	}
	for _, s := range schedule.GetAllSnapshots() {
		if !mine[s.Name] {
			continue
		}
		j := crawlerJobVM{Name: s.Name, Status: s.Status}
		if s.LastError != "" {
			j.Activity = s.LastError
		} else if len(s.Logs) > 0 {
			j.Activity = s.Logs[len(s.Logs)-1]
		}
		if s.Status == "running" || s.ElapsedSecs > 0 {
			j.Running, anyRunning = true, true
		}
		jobs = append(jobs, j)
	}
	return jobs, anyRunning
}

// ── jobs widget (override for the "Usenet" job group) ───────────────

func (p *Plugin) renderJobsWidget(ctx context.Context) (template.HTML, error) {
	stats, err := p.st.stats(ctx)
	if err != nil {
		return "", err
	}
	jobs, _ := p.jobVMs()
	var crawlJobs []crawlerJobVM
	for _, j := range jobs {
		if strings.HasPrefix(j.Name, "Usenet") {
			crawlJobs = append(crawlJobs, j)
		}
	}
	return p.frag("jobswidget.html", map[string]any{
		"Jobs": crawlJobs, "Stats": stats,
	})
}

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
