package usenet

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

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
	return knobList(p.effective(ctx))
}

// knobList is knobs() minus the settings read, so tests can assert every
// knobFields key has a rendered row without a store — the hidden-knob trap
// (settable via SQL, invisible in the UI) regresses one forgotten line at a
// time, and this is the line that catches it.
func knobList(cfg Config) []knob {
	return []knob{
		{"connections", "NNTP connections", cfg.Connections, "size of the shared connection pool — how many overview batches are fetched in parallel; keep at or below your provider's per-account limit"},
		{"keepalive_min", "Connection keepalive (min)", cfg.KeepaliveMin, "how often to probe idle pool connections with a DATE so the provider does not reap them between passes; 0 disables. Set it below your provider's idle timeout"},
		{"retention_days", "Crawl depth (days)", cfg.RetentionDays, "how far back to fetch and backfill; raise it to pull more history. This does NOT delete anything"},
		{"nzb_retention_days", "Delete releases older than (days)", cfg.NZBRetentionDays, "0 = keep forever (default). Any other value DELETES assembled releases past that age on the nightly prune — set it only if you actually want a rolling window"},
		{"crawl_interval_min", "Crawl interval (min)", cfg.CrawlIntervalMin, "how often to crawl + build (applies next cycle)"},
		{"batch", "Overview batch size", cfg.Batch, "article-number span per NNTP OVER request"},
		{"max_groups", "Max groups per run", cfg.MaxGroups, "cap active groups crawled per pass"},
		{"crawl_max_batches", "Crawl pass budget (batches)", cfg.CrawlMaxBatches, "cap OVER batches planned per forward pass — a huge backlog runs as bounded rounds (catch-up rolls the rest into the next round) instead of one hours-long pass"},
		{"max_articles_per_group", "First-pass article cap", cfg.MaxArticlesPerGroup, "cap a new group's initial volume"},
		{"crawl_pressure_high_pct", "Pause crawling at staging fullness (%)", cfg.CrawlPressureHighPct, "stop staging when the backend is this full — writing into a full Redis evicts sets that are still assembling, destroying completed releases"},
		{"ready_reap_per_pass", "Ready-queue sweep per build round", cfg.ReadyReapPerPass, "how many queued sets to check for expired data each build round (the sweep resumes where it left off) — the queue is drained by a random sample, so dead entries left in it waste draw slots"},
		{"diag_keep_days", "Diagnostic history kept (days)", cfg.DiagKeepDays, "rolling window for the observe-only series (staging census, subject corpus, set resolutions). Their volume follows the crawler, not the site: the walk-past sweep wrote 1,000+ resolution rows a minute while clearing a backlog. Lower this if those tables grow faster than they earn their keep"},
		{"walk_past_grace_min", "Walk-past grace (minutes)", cfg.WalkPastGraceMin, "how long a set must go without a new article before the walk-past sweep may judge it dead — covers retried batches and staging latency at the walk edge"},
		{"walk_past_sweep_per_round", "Walk-past sweep per build round", cfg.WalkPastSweepPerRound, "how many staged sets the dead-set sweep examines each build round (cursors persist, so this times the round rate is the sweep speed)"},
		{"backfill_interval_min", "Backfill interval (min)", cfg.BackfillIntervalMin, "how often to pull history (applies next cycle)"},
		{"backfill_batches_per_run", "Backfill batches per run", cfg.BackfillBatchesPerRun, "how much history each backfill pass pulls"},
		{"staging_prune_hours", "Staging prune horizon (hrs)", cfg.StagingPruneHours, "drop staged articles older than this that never completed into an NZB (default 6)"},
		{"staging_ttl_hours", "Redis staging TTL (hrs)", cfg.StagingTTLHours, "redis mode: staged keys expire after this — raise it if passes are far enough apart that one release's parts arrive hours apart (default 2)"},
		{"staging_max_rows", "Staging soft cap (rows)", cfg.StagingMaxRows, "pg back-pressure denominator: backfill yields as staged rows approach this (default 2,000,000)"},
		{"health_interval_min", "Health check interval (min)", cfg.HealthIntervalMin, "how often to sweep stored NZBs for expired articles (default 60)"},
		{"health_batch_size", "Health check batch (releases)", cfg.HealthBatchSize, "releases examined per sweep (default 50)"},
		{"health_recheck_days", "Health re-check after (days)", cfg.HealthRecheckDays, "how long a verdict stands before it is re-tested (default 30)"},
		{"health_min_age_hours", "Health min age (hrs)", cfg.HealthMinAgeHours, "skip releases newer than this — articles may still be propagating, and checking early wrongly marks new uploads dead (default 24)"},
		{"health_stat_chunk", "Health STATs per lease", cfg.HealthStatChunk, "segments checked per borrowed connection; smaller returns connections to the crawler more often (default 200)"},
		{"backfill_pressure_high_pct", "Backfill pause at (% pressure)", cfg.BackfillPressureHighPct, "backfill pauses when staging pressure reaches this percent (default 85), while the builder has a backlog to drain"},
		{"backfill_pressure_low_pct", "Backfill resume below (% pressure)", cfg.BackfillPressureLowPct, "paused backfill resumes once pressure drops below this percent (default 70) — must stay below the pause threshold or the gate flaps"},
		{"backfill_pressure_ceiling_pct", "Eviction ceiling (% pressure)", cfg.BackfillPressureCeilingPct, "the hard stop that applies even with nothing to drain (default 92) — past it Redis EVICTS forming sets to make room; also caps the crawl's effective pause threshold"},
		{"backfill_drain_wait_sec", "Backfill drain wait (sec)", cfg.BackfillDrainWaitSec, "how long a pressure-paused backfill waits for the builder to make room before ending its pass (default 180)"},
		{"build_drain_per_pass", "Builder sets per pass", cfg.BuildDrainPerPass, "completed sets assembled per build pass (default 500) — raise during backlog recovery"},
		{"tagfill_interval_min", "Tag fill interval (min)", cfg.TagFillIntervalMin, "tag-fill + recategorize cadence (default 360)"},
		{"prune_interval_min", "Prune interval (min)", cfg.PruneIntervalMin, "prune cadence (default 1440)"},
		{"dial_timeout_sec", "NNTP dial timeout (sec)", cfg.DialTimeoutSec, "connect + greeting bound per connection (default 30); one pool open is bounded at twice this, so a dead host costs seconds, not minutes"},
		{"op_timeout_sec", "NNTP operation timeout (sec)", cfg.OpTimeoutSec, "bound on one whole exchange — one GROUP+OVER round (default 60). Raise together with the batch size on a slow provider, or honest fetches time out and the pool churns reconnects"},
		{"provider_down_cooldown_min", "Provider bench cooldown (min)", cfg.ProviderDownCooldownMin, "how long a failed provider stays benched before retrying (default 10) — while benched, its backup is promoted"},
		{"lease_ttl_min", "Lease TTL (min)", cfg.LeaseTTLMin, "how long a claimed group/job lease survives without renewal (default 15): long enough that a slow pass never loses its own claim, short enough that a killed worker's work is retaken promptly"},
		{"assign_term_min", "Worker term (min)", cfg.AssignTermMin, "multi-worker: the group split is fixed for terms of this length (default 15), so a joiner waits for the boundary instead of reshuffling mid-pass"},
		{"worker_stale_sec", "Worker staleness (sec)", cfg.WorkerStaleSec, "multi-worker: a crawler missing heartbeats this long drops out of the split (default 90)"},
	}
}

// validateKnobs rejects knob combinations no gate can operate under, BEFORE
// anything persists. `vals` is what the form posted; anything absent or 0
// keeps its current effective value, so the relationships are checked against
// what will actually be in force after the save.
//
// The percentages matter most: staging pressure caps at 100%, so a gate set
// past that is unreachable — one extra typed digit (850 for 85) silently
// disabled eviction protection, and the pause logs kept citing thresholds
// that could never fire. The ordering rules keep the hysteresis real
// (low < high) and the eviction ceiling outermost (high ≤ ceiling): with a
// drainable backlog only the high/low latch gates, so a high above the
// ceiling would stage straight through the band where Redis evicts.
func validateKnobs(cur Config, vals map[string]int) string {
	for _, k := range []string{
		"crawl_pressure_high_pct", "backfill_pressure_high_pct",
		"backfill_pressure_low_pct", "backfill_pressure_ceiling_pct",
	} {
		if n, ok := vals[k]; ok && n > 100 {
			return fmt.Sprintf("%s is a percentage: %d can never fire (staging pressure tops out at 100%%), "+
				"which silently disables that gate", k, n)
		}
	}
	pick := func(key string, cur int) int {
		if n, ok := vals[key]; ok && n > 0 {
			return n
		}
		return cur
	}
	low := pick("backfill_pressure_low_pct", cur.BackfillPressureLowPct)
	high := pick("backfill_pressure_high_pct", cur.BackfillPressureHighPct)
	ceiling := pick("backfill_pressure_ceiling_pct", cur.BackfillPressureCeilingPct)
	if low >= high {
		return fmt.Sprintf("backfill_pressure_low_pct (%d) must be below backfill_pressure_high_pct (%d) — "+
			"equal or inverted removes the hysteresis and the gate flaps at the threshold", low, high)
	}
	if high > ceiling {
		return fmt.Sprintf("backfill_pressure_high_pct (%d) cannot exceed backfill_pressure_ceiling_pct (%d) — "+
			"past the ceiling Redis evicts the sets still assembling", high, ceiling)
	}
	return ""
}

func (p *Plugin) renderSettings(ctx context.Context, gq, msg, errMsg string, showBuilder bool) (template.HTML, error) {
	groups, err := p.st.allGroups(ctx, gq, 300)
	if err != nil {
		return "", err
	}
	total, err := p.st.groupCount(ctx)
	if err != nil {
		return "", err
	}
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/list-servers", err)
	}
	// The Crawlers dashboard and Filters blacklist render as embedded tab
	// fragments — one page per plugin. Their msg/err slots stay empty because
	// the flash renders once at the top of this page.
	//
	// A failing tab degrades to an inline card; it does NOT fail the page.
	// Every fragment error used to propagate, so one slow query (builderInfo
	// is a 30s+ disk-spilling aggregation at the 33M-row scale prod hit) took
	// down the whole plugin admin page — including the Providers tab an
	// operator needs to fix the cause. Worst exactly when staging backs up.
	tab := func(name string, render func() (template.HTML, error)) template.HTML {
		frag, err := render()
		if err != nil {
			p.reportErr(ctx, "usenet/render-"+name, err)
			return template.HTML(`<div class="alert alert-warning">The ` +
				template.HTMLEscapeString(name) + ` view failed to render: ` +
				template.HTMLEscapeString(err.Error()) +
				`. The rest of this page is unaffected.</div>`)
		}
		return frag
	}
	crawlersTab := tab("crawlers", func() (template.HTML, error) { return p.renderCrawlers(ctx, "", "") })
	jobsTab := tab("jobs", func() (template.HTML, error) { return p.renderJobs(ctx, showBuilder) })
	filtersTab := tab("filters", func() (template.HTML, error) { return p.renderFilters(ctx, "", "") })
	return p.frag("settings.html", map[string]any{
		"Servers": servers, "DefaultConns": p.effective(ctx).Connections,
		"Knobs": p.knobs(ctx), "SkipBackfill": p.effective(ctx).SkipBackfill,
		"CrawlNoCatchup":         p.effective(ctx).CrawlNoCatchup,
		"BackfillNoCatchup":      p.effective(ctx).BackfillNoCatchup,
		"BuildNoCatchup":         p.effective(ctx).BuildNoCatchup,
		"HoldLowUntilBackfilled": p.effective(ctx).HoldLowUntilBackfilled,
		"Groups":                 groups, "GroupQuery": gq, "Tiers": AllTiers,
		"GroupTotal": total, "Shown": len(groups),
		"CrawlersTab": crawlersTab, "JobsTab": jobsTab, "FiltersTab": filtersTab,
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

// actionProbeProvider runs the backbone fingerprint probe (probe.go): does
// this provider assign the same article numbers as the reference provider?
// The reference is the first OTHER enabled provider — with one provider there
// is nothing to compare against.
func (p *Plugin) actionProbeProvider(gc *gin.Context) (template.HTML, error) {
	ctx, cancel := context.WithTimeout(gc.Request.Context(), 60*time.Second)
	defer cancel()
	cand := formProvider(gc)
	servers, err := p.st.listServers(ctx)
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	var ref *provider
	for i := range servers {
		if servers[i].ID != cand.ID && servers[i].Enabled {
			ref = &servers[i]
			break
		}
	}
	if ref == nil {
		return settingsRedirect(gc, "err", "backbone probe needs a second enabled provider to compare against")
	}
	// Stored secrets for both sides — the form never carries them.
	refSrv := pluginapi.Server{Host: ref.Host, Port: ref.Port, TLS: ref.TLS, Username: ref.Username}
	if pw, err := p.st.serverPassword(ctx, ref.ID); err == nil {
		refSrv.Password = pw
	}
	if cand.Password == "" && cand.ID > 0 {
		if pw, err := p.st.serverPassword(ctx, cand.ID); err == nil {
			cand.Password = pw
		}
	}
	if cand.Host == "" && cand.ID > 0 {
		for _, sv := range servers {
			if sv.ID == cand.ID {
				cand.Host, cand.Port, cand.TLS, cand.Username = sv.Host, sv.Port, sv.TLS, sv.Username
				break
			}
		}
	}
	if cand.Port <= 0 {
		cand.Port = 119
	}
	candSrv := pluginapi.Server{Host: cand.Host, Port: cand.Port, TLS: cand.TLS,
		Username: cand.Username, Password: cand.Password}

	v, err := p.probeBackbone(ctx, refSrv, candSrv)
	if err != nil {
		return settingsRedirect(gc, "err", "backbone probe: "+err.Error())
	}
	if v.Same {
		return settingsRedirect(gc, "msg", fmt.Sprintf(
			"same numbering as %s (%d/%d articles matched in %s) — use backbone %q on this provider",
			ref.label(), v.Matched, v.Compared, v.Group, ref.backboneKey()))
	}
	return settingsRedirect(gc, "err", fmt.Sprintf(
		"DISTINCT numbering vs %s (only %d/%d matched in %s) — keep a separate backbone label; sharing %q would corrupt its watermarks",
		ref.label(), v.Matched, v.Compared, v.Group, ref.backboneKey()))
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

func (p *Plugin) actionTuneGroup(gc *gin.Context) (template.HTML, error) {
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" {
		return settingsRedirect(gc, "err", "no group selected")
	}
	ret, _ := strconv.Atoi(gc.PostForm("retention_days"))
	thr, _ := strconv.Atoi(gc.PostForm("throttle_ms"))
	// normalizeTier is the gate: the form is a <select>, but a hand-rolled
	// POST could carry anything, and the column has a CHECK constraint that
	// would turn that into a 500 rather than a quietly-defaulted row.
	tier := normalizeTier(gc.PostForm("tier"))
	err := p.st.setGroupTuning(gc.Request.Context(), name, ret, thr, tier)
	// Same in-place contract as actionToggleGroup: the tuning inputs (the
	// tier select especially) auto-save via fetch, so a bare status is
	// the whole answer — a redirect-and-re-render per change would re-run
	// every dashboard query and jump the scroll. No-JS still gets the
	// redirect below.
	if gc.GetHeader("X-Requested-With") == "fetch" {
		if err != nil {
			gc.String(http.StatusInternalServerError, "save failed")
		} else {
			gc.Status(http.StatusNoContent)
		}
		return "", nil
	}
	if err != nil {
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

	// Parse and validate EVERYTHING before persisting anything. The old loop
	// committed each key in Go-map iteration order and bailed on the first bad
	// field, leaving a nondeterministic prefix of the form saved underneath an
	// error flash.
	ints := map[string]string{}
	vals := map[string]int{}
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
		ints[key], vals[key] = raw, n
	}
	eff := p.effective(ctx)
	if msg := validateKnobs(eff, vals); msg != "" {
		return settingsRedirect(gc, "err", msg)
	}
	bools := postedBools(gc.PostForm, cfg.boolFields())

	for key, raw := range ints {
		if err := p.st.setSetting(ctx, key, raw); err != nil {
			return settingsRedirect(gc, "err", err.Error())
		}
	}
	for key, val := range bools {
		if err := p.st.setSetting(ctx, key, val); err != nil {
			return settingsRedirect(gc, "err", err.Error())
		}
	}
	// Knob changes alter crawler behaviour on the next run with no other
	// trace — when a job suddenly behaves differently, "did a setting change
	// just before this?" must be answerable from the log. old->new, against
	// the effective config read BEFORE the writes above.
	effInts, effBools := eff.knobFields(), eff.boolFields()
	var changed []string
	for key, val := range vals {
		if old := effInts[key]; old != nil && *old != val {
			changed = append(changed, fmt.Sprintf("%s: %d -> %d", key, *old, val))
		}
	}
	for key, val := range bools {
		if old := effBools[key]; old != nil && strconv.FormatBool(*old) != val {
			changed = append(changed, fmt.Sprintf("%s: %t -> %s", key, *old, val))
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		p.logAction("settings saved: %s", strings.Join(changed, ", "))
	}
	return settingsRedirect(gc, "msg", "settings saved — applied on each job's next run")
}

// postedBools collects checkbox states, but only for keys the form DECLARED
// via a has_<key> marker. An unchecked checkbox posts nothing, so writing
// "false" for every known bool force-reset the ones the form did not render —
// which is how every knob save silently flipped backfill_no_catchup (the
// catch-up loop's own emergency brake) back off while it wasn't in the UI.
func postedBools(get func(string) string, fields map[string]*bool) map[string]string {
	out := map[string]string{}
	for key := range fields {
		if get("has_"+key) == "" {
			continue
		}
		val := "false"
		if v := get(key); v == "on" || v == "true" {
			val = "true"
		}
		out[key] = val
	}
	return out
}

func (p *Plugin) actionFetchGroups(gc *gin.Context) (template.HTML, error) {
	n, err := p.svc.FetchGroups(gc.Request.Context())
	if err != nil {
		return settingsRedirect(gc, "err", "fetch failed: "+err.Error())
	}
	return settingsRedirect(gc, "msg", "fetched "+strconv.Itoa(n)+" new group(s)")
}

func (p *Plugin) actionToggleGroup(gc *gin.Context) (template.HTML, error) {
	err := p.st.setGroupActive(gc.Request.Context(), gc.PostForm("name"), gc.PostForm("active") == "true")
	// The page toggles groups in place (fetch + DOM flip): a full
	// redirect-and-re-render per click re-runs every dashboard query and
	// jumps the scroll position — painful when enabling twenty groups in a
	// row. The fetch path answers with a bare status; the form still works
	// without JS via the redirect below.
	if gc.GetHeader("X-Requested-With") == "fetch" {
		if err != nil {
			gc.String(http.StatusInternalServerError, "toggle failed")
		} else {
			gc.Status(http.StatusNoContent)
		}
		return "", nil
	}
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	dest := usenetURL + "#newsgroups"
	if gq := gc.PostForm("gq"); gq != "" {
		dest = usenetURL + "?gq=" + url.QueryEscape(gq) + "#newsgroups" // keep the current group search
	}
	return redirect(gc, dest)
}

// actionResetWatermark rewinds one group's forward watermark so the crawler
// re-reads the span it already fetched. Needed after a parsing fix: the
// articles were read, mis-assembled, discarded, and left behind the mark where
// nothing would look at them again.
//
// Type-to-confirm. The browser prompt is UX; THIS check is the control. A
// mis-click here costs a re-crawl of millions of articles against a metered
// provider, so the operator names the group they mean.
func (p *Plugin) actionResetWatermark(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" {
		return settingsRedirect(gc, "err", "no group selected")
	}
	if strings.TrimSpace(gc.PostForm("confirm")) != name {
		return settingsRedirect(gc, "err",
			"reset cancelled — the typed name did not match "+name)
	}

	// State is keyed by BACKBONE, and the group's is whichever backbone its
	// crawl state was filed under. Resolve it from the primary provider, the
	// same way a crawl pass does.
	servers, err := p.st.listServers(ctx)
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	backbone := ""
	for _, sv := range servers {
		if sv.Enabled {
			backbone = sv.backboneKey()
			break
		}
	}
	if backbone == "" {
		return settingsRedirect(gc, "err", "no enabled provider — cannot tell which backbone's state to reset")
	}

	// Two independent scopes: the plugin-era span (cheap, repairs a parser bug)
	// and the history below it (expensive, repairs a blind spot inherited from
	// a previous crawler). Anything unrecognised is the cheap one.
	scope := resetForward
	if gc.PostForm("scope") == string(resetHistory) {
		scope = resetHistory
	}

	res, err := p.st.resetWatermark(ctx, backbone, name, scope)
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	if res.Scope == resetHistory {
		p.logAction("%s: backfill reopened down to %d (%s article(s) of gaps to re-walk)",
			res.Group, res.NewMark, fmtComma(res.Articles))
		return settingsRedirect(gc, "msg", fmt.Sprintf(
			"%s: backfill reopened — %s article(s) of gaps below article %d will be re-walked, "+
				"bounded per pass so the forward crawl keeps running. Already-stored releases "+
				"dedup on content hash, so nothing duplicates.",
			res.Group, fmtComma(res.Articles), res.OldMark))
	}
	p.logAction("%s: watermark reset %d -> %d (%s article(s) queued for re-read)",
		res.Group, res.OldMark, res.NewMark, fmtComma(res.Articles))
	return settingsRedirect(gc, "msg", fmt.Sprintf(
		"%s: watermark rewound to %d — %s article(s) will be re-read on the next pass. "+
			"Already-stored releases dedup on content hash, so nothing duplicates.",
		res.Group, res.NewMark, fmtComma(res.Articles)))
}

// logAction records an operator action from an admin handler.
//
// Admin handlers run in the WEB process, where the job pointers are nil — the
// jobs are registered only in worker/all (plugin.go). Calling crawlJob.Log()
// from here panicked the settings page on a split deployment: the reset itself
// had already committed, so the operator got a 500 for work that had actually
// succeeded, which is the worst of both.
//
// The host logger is the right sink because it exists in every process. The job
// log is a bonus when this process happens to own it, so a single-process
// install still sees the line in the place it expects.
func (p *Plugin) logAction(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.core != nil && p.core.Logger != nil {
		p.core.Logger.Info("usenet: " + msg)
	}
	if p.crawlJob != nil {
		p.crawlJob.Log("%s", msg)
	}
}
