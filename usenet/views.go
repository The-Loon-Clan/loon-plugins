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

// providerVM is one provider row on the crawlers page: what it is, and whether
// its connections are actually working. Prod has no equivalent — it only ever
// had a fixed primary/secondary pair.
type providerVM struct {
	Name, Host, Backbone, Role string
	Enabled, Down, Dialled     bool
	Open, Target               int
	Resets                     int64
}

func (p *Plugin) fleetVMs(ctx context.Context) []providerVM {
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/fleet-view", err)
		return nil
	}
	stats := map[int]providerStat{}
	if p.fleet != nil {
		stats = p.fleet.snapshotStats(time.Now())
	}
	out := make([]providerVM, len(servers))
	for i, sv := range servers {
		vm := providerVM{
			Name: sv.label(), Host: sv.addr(), Backbone: sv.backboneKey(),
			Role: sv.Role, Enabled: sv.Enabled,
		}
		if st, ok := stats[sv.ID]; ok {
			vm.Dialled = true
			vm.Open, vm.Target, vm.Resets, vm.Down = st.Open, st.Target, st.Resets, st.Down
		}
		out[i] = vm
	}
	return out
}

// workerVM is one crawler host. This is the view that makes a multi-host setup
// debuggable: who is alive, and how much of the work each one currently holds.
type workerVM struct {
	ID     string
	Me     bool
	Groups int
}

func (p *Plugin) workerVMs(ctx context.Context) []workerVM {
	cfg := p.effective(ctx)
	stale := time.Duration(cfg.WorkerStaleSec) * time.Second
	if stale <= 0 {
		stale = 90 * time.Second
	}
	term := time.Duration(cfg.AssignTermMin) * time.Minute
	// Everyone alive, not just this term's members — a worker waiting out the
	// term is exactly what an operator wants to see after adding a host.
	workers, err := p.st.eligibleWorkers(ctx, time.Now().Add(term), stale)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/worker-view", err)
		return nil
	}
	held, err := p.st.leaseHolders(ctx, leaseScopeGroup)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/lease-view", err)
	}
	counts := map[string]int{}
	for _, owner := range held {
		counts[owner]++
	}
	me := workerID()
	out := make([]workerVM, len(workers))
	for i, w := range workers {
		out[i] = workerVM{ID: w, Me: w == me, Groups: counts[w]}
	}
	return out
}

// healthVM is the healthy/broken/dead/unknown split, the one prod card worth
// copying wholesale — it answers "is the archive still downloadable" at a glance.
type healthVM struct {
	Healthy, Broken, Dead, Unknown, Total int
	HealthyPct, BrokenPct, DeadPct        int
}

func (p *Plugin) healthVM(ctx context.Context) healthVM {
	counts, err := p.st.healthBreakdown(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/health-view", err)
		return healthVM{}
	}
	vm := healthVM{
		Healthy: counts[healthHealthy], Broken: counts[healthBroken],
		Dead: counts[healthDead], Unknown: counts[healthUnknown],
	}
	vm.Total = vm.Healthy + vm.Broken + vm.Dead + vm.Unknown
	if vm.Total > 0 {
		vm.HealthyPct = vm.Healthy * 100 / vm.Total
		vm.BrokenPct = vm.Broken * 100 / vm.Total
		vm.DeadPct = vm.Dead * 100 / vm.Total
	}
	return vm
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
		{"connections", "NNTP connections", cfg.Connections, "size of the shared connection pool — how many overview batches are fetched in parallel; keep at or below your provider's per-account limit"},
		{"retention_days", "Retention / backfill depth (days)", cfg.RetentionDays, "keep — and backfill — the last N days; raise it to pull more history, backfill stops once caught up to this horizon"},
		{"crawl_interval_min", "Crawl interval (min)", cfg.CrawlIntervalMin, "how often to crawl + build (applies next cycle)"},
		{"batch", "Overview batch size", cfg.Batch, "article-number span per NNTP OVER request"},
		{"max_groups", "Max groups per run", cfg.MaxGroups, "cap active groups crawled per pass"},
		{"max_articles_per_group", "First-pass article cap", cfg.MaxArticlesPerGroup, "cap a new group's initial volume"},
		{"backfill_interval_min", "Backfill interval (min)", cfg.BackfillIntervalMin, "how often to pull history (applies next cycle)"},
		{"backfill_batches_per_run", "Backfill batches per run", cfg.BackfillBatchesPerRun, "how much history each backfill pass pulls"},
		{"staging_prune_hours", "Staging prune horizon (hrs)", cfg.StagingPruneHours, "drop staged articles older than this that never completed into an NZB (default 6)"},
		{"staging_max_rows", "Staging soft cap (rows)", cfg.StagingMaxRows, "pg back-pressure denominator: backfill yields as staged rows approach this (default 2,000,000)"},
		{"health_interval_min", "Health check interval (min)", cfg.HealthIntervalMin, "how often to sweep stored NZBs for expired articles (default 60)"},
		{"health_batch_size", "Health check batch (releases)", cfg.HealthBatchSize, "releases examined per sweep (default 50)"},
		{"health_recheck_days", "Health re-check after (days)", cfg.HealthRecheckDays, "how long a verdict stands before it is re-tested (default 30)"},
		{"health_min_age_hours", "Health min age (hrs)", cfg.HealthMinAgeHours, "skip releases newer than this — articles may still be propagating, and checking early wrongly marks new uploads dead (default 24)"},
		{"health_stat_chunk", "Health STATs per lease", cfg.HealthStatChunk, "segments checked per borrowed connection; smaller returns connections to the crawler more often (default 200)"},
		{"backfill_pressure_high_pct", "Backfill pause at (% pressure)", cfg.BackfillPressureHighPct, "backfill pauses when staging pressure reaches this percent (default 85); forward crawl never pauses"},
		{"backfill_pressure_low_pct", "Backfill resume below (% pressure)", cfg.BackfillPressureLowPct, "paused backfill resumes once pressure drops below this percent (default 70)"},
	}
}

func (p *Plugin) renderSettings(ctx context.Context, srv pluginapi.Server, gq, msg, errMsg string) (template.HTML, error) {
	groups, _ := p.st.allGroups(ctx, gq, 300)
	total, _ := p.st.groupCount(ctx)
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/list-servers", err)
	}
	return p.frag("settings.html", map[string]any{
		"Servers": servers, "DefaultConns": p.effective(ctx).Connections,
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
	srv := pluginapi.Server{
		Host:     strings.TrimSpace(gc.PostForm("host")),
		Port:     port,
		TLS:      tls == "on" || tls == "true",
		Username: gc.PostForm("username"),
		Password: gc.PostForm("password"),
		Enabled:  true,
		Backbone: strings.TrimSpace(gc.PostForm("backbone")),
	}
	// Only ever FILL A BLANK. An operator's explicit value always wins: the
	// lookup table is a convenience that can go stale, and quietly rewriting a
	// deliberate answer is how two providers end up wrongly sharing crawl state.
	if srv.Backbone == "" {
		srv.Backbone = backboneForHost(srv.Host)
	}
	return srv
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

// formProvider reads a provider row from the management form. A blank password
// means "unchanged" — the list never sends the stored one to the browser.
func formProvider(gc *gin.Context) provider {
	id, _ := strconv.Atoi(gc.PostForm("id"))
	port, _ := strconv.Atoi(gc.PostForm("port"))
	prio, _ := strconv.Atoi(gc.PostForm("priority"))
	conns, _ := strconv.Atoi(gc.PostForm("connections"))
	tls := gc.PostForm("tls")
	pr := provider{
		ID:          id,
		Name:        strings.TrimSpace(gc.PostForm("name")),
		Host:        strings.TrimSpace(gc.PostForm("host")),
		Port:        port,
		TLS:         tls == "on" || tls == "true",
		Username:    gc.PostForm("username"),
		Password:    gc.PostForm("password"),
		Enabled:     gc.PostForm("enabled") != "",
		Role:        gc.PostForm("role"),
		Priority:    prio,
		Connections: conns,
		Backbone:    strings.TrimSpace(gc.PostForm("backbone")),
	}
	// Same rule as the wizard: only ever fill a blank, never overwrite a
	// deliberate answer with a lookup that may be stale.
	if pr.Backbone == "" {
		pr.Backbone = backboneForHost(pr.Host)
	}
	return pr
}

func (p *Plugin) actionSaveProvider(gc *gin.Context) (template.HTML, error) {
	pr := formProvider(gc)
	if pr.ID == 0 {
		// A brand-new provider defaults to enabled; an edit keeps whatever the
		// checkbox says.
		pr.Enabled = true
	}
	if err := p.st.upsertServer(gc.Request.Context(), pr); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	msg := "provider saved"
	if pr.ID == 0 {
		msg = "provider added — it joins the fleet on the next crawl"
	}
	return settingsRedirect(gc, "msg", msg)
}

func (p *Plugin) actionDeleteProvider(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.Atoi(gc.PostForm("id"))
	if id <= 0 {
		return settingsRedirect(gc, "err", "no provider selected")
	}
	if err := p.st.deleteServer(gc.Request.Context(), id); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	// Crawl state survives on purpose: it is keyed by backbone, so it may belong
	// to another account, and discarding it would re-crawl that history.
	return settingsRedirect(gc, "msg", "provider removed (its crawl history is kept)")
}

func (p *Plugin) actionToggleProvider(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.Atoi(gc.PostForm("id"))
	if id <= 0 {
		return settingsRedirect(gc, "err", "no provider selected")
	}
	if err := p.st.toggleServer(gc.Request.Context(), id); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", "provider updated")
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
		"Fleet": p.fleetVMs(ctx), "Workers": p.workerVMs(ctx),
		"Health":      p.healthVM(ctx),
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
