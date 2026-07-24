package usenet

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Settings view: the setup wizard — providers, newsgroups, tuning knobs.

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
		{"retention_days", "Crawl depth (days)", cfg.RetentionDays, "how far back to fetch and backfill; raise it to pull more history. This does NOT delete anything"},
		{"nzb_retention_days", "Delete releases older than (days)", cfg.NZBRetentionDays, "0 = keep forever (default). Any other value DELETES assembled releases past that age on the nightly prune — set it only if you actually want a rolling window"},
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

func (p *Plugin) renderSettings(ctx context.Context, gq, msg, errMsg string) (template.HTML, error) {
	groups, _ := p.st.allGroups(ctx, gq, 300)
	total, _ := p.st.groupCount(ctx)
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/list-servers", err)
	}
	// The Crawlers dashboard and Filters blacklist render as embedded tab
	// fragments — one page per plugin. Their msg/err slots stay empty because
	// the flash renders once at the top of this page.
	crawlersTab, err := p.renderCrawlers(ctx, "", "")
	if err != nil {
		return "", err
	}
	filtersTab, err := p.renderFilters(ctx, "", "")
	if err != nil {
		return "", err
	}
	return p.frag("settings.html", map[string]any{
		"Servers": servers, "DefaultConns": p.effective(ctx).Connections,
		"Knobs": p.knobs(ctx), "SkipBackfill": p.effective(ctx).SkipBackfill,
		"Groups": groups, "GroupQuery": gq,
		"GroupTotal": total, "Shown": len(groups),
		"CrawlersTab": crawlersTab, "FiltersTab": filtersTab,
		"Msg": msg, "Err": errMsg,
	})
}

// actionTestProvider dials one provider row's credentials (or the add-row's)
// without saving anything. A blank password on an existing row means
// "unchanged" — the stored secret is used, or testing a saved provider would
// always fail auth since the list never sends passwords to the browser.
func (p *Plugin) actionTestProvider(gc *gin.Context) (template.HTML, error) {
	pr := formProvider(gc)
	// An id-only POST (the crawlers-tab provider panel) means "test the row as
	// stored" — load the full config, not just the password.
	if pr.Host == "" && pr.ID > 0 {
		if servers, err := p.st.listServers(gc.Request.Context()); err == nil {
			for _, sv := range servers {
				if sv.ID == pr.ID {
					pr.Host, pr.Port, pr.TLS, pr.Username = sv.Host, sv.Port, sv.TLS, sv.Username
					break
				}
			}
		}
	}
	if pr.Password == "" && pr.ID > 0 {
		if pw, err := p.st.serverPassword(gc.Request.Context(), pr.ID); err == nil {
			pr.Password = pw
		}
	}
	if pr.Port <= 0 {
		pr.Port = 119
	}
	srv := pluginapi.Server{
		Host: pr.Host, Port: pr.Port, TLS: pr.TLS,
		Username: pr.Username, Password: pr.Password,
	}
	if err := testConnect(srv); err != nil {
		return settingsRedirect(gc, "err", "connection failed: "+err.Error())
	}
	return settingsRedirect(gc, "msg", "connection ok — credentials verified, nothing saved")
}

// formProvider reads a provider row from the management form. A blank password
// means "unchanged" — the list never sends the stored one to the browser.
func formProvider(gc *gin.Context) provider {
	id, _ := strconv.Atoi(gc.PostForm("id"))
	port, _ := strconv.Atoi(gc.PostForm("port"))
	prio, _ := strconv.Atoi(gc.PostForm("priority"))
	conns, _ := strconv.Atoi(gc.PostForm("connections"))
	acctCap, _ := strconv.Atoi(gc.PostForm("account_cap"))
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
		AccountCap:  acctCap,
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

func (p *Plugin) actionTuneGroup(gc *gin.Context) (template.HTML, error) {
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" {
		return settingsRedirect(gc, "err", "no group selected")
	}
	ret, _ := strconv.Atoi(gc.PostForm("retention_days"))
	thr, _ := strconv.Atoi(gc.PostForm("throttle_ms"))
	low := gc.PostForm("low_priority") != ""
	if err := p.st.setGroupTuning(gc.Request.Context(), name, ret, thr, low); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", name+" updated")
}

func (p *Plugin) actionMoveGroup(gc *gin.Context) (template.HTML, error) {
	name := strings.TrimSpace(gc.PostForm("name"))
	delta := -1
	if gc.PostForm("dir") == "down" {
		delta = 1
	}
	if err := p.st.moveGroup(gc.Request.Context(), name, delta); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", "order updated")
}

func (p *Plugin) actionDeleteGroup(gc *gin.Context) (template.HTML, error) {
	name := strings.TrimSpace(gc.PostForm("name"))
	if err := p.st.deleteGroup(gc.Request.Context(), name); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", name+" removed (crawl history kept)")
}

func (p *Plugin) actionPurgeInactive(gc *gin.Context) (template.HTML, error) {
	n, err := p.st.deleteInactiveGroups(gc.Request.Context())
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", fmt.Sprintf("removed %d inactive group(s)", n))
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
		// 0 is allowed: it means "keep forever" / "no cap" where the knob
		// defines that, and "use the built-in default" everywhere else —
		// withOverrides only applies stored values > 0.
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return settingsRedirect(gc, "err", key+" must be a number ≥ 0")
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
	dest := usenetURL + "#newsgroups"
	if gq := gc.PostForm("gq"); gq != "" {
		dest = usenetURL + "?gq=" + url.QueryEscape(gq) + "#newsgroups" // keep the current group search
	}
	return redirect(gc, dest)
}
