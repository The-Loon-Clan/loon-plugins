package usenet

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// TestTemplatesParse: templates are parsed in Provision, so a syntax error would
// not surface until a process boots. Catch it here instead.
func TestTemplatesParse(t *testing.T) {
	if _, err := template.ParseFS(viewFS, "templates/*.html"); err != nil {
		t.Fatalf("templates do not parse: %v", err)
	}
}

// TestSettingsRendersProviders exercises the provider table against
// representative data — a bad field reference in a range is a runtime error that
// would otherwise only appear on the settings page.
func TestSettingsRendersProviders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Servers": []provider{
			{ID: 1, Name: "Primary", Host: "news.eweka.nl", Port: 563, TLS: true,
				Username: "u", Enabled: true, Role: roleActive, Priority: 10,
				Connections: 20, Backbone: "omicron"},
			{ID: 2, Name: "Standby", Host: "news.other.com", Port: 119,
				Enabled: false, Role: roleBackup, Priority: 50},
		},
		"DefaultConns": 10,
		"Knobs":        []knob{{Key: "connections", Label: "NNTP connections", Value: 10, Help: "h"}},
		"SkipBackfill": false,
		"Groups": []pluginapi.GroupInfo{
			{Name: "alt.binaries.anime", Active: true, NZBs: 4211, RetentionDays: 0, ThrottleMs: 0},
			{Name: "alt.binaries.hdtv", Active: true, NZBs: 900, RetentionDays: 30, ThrottleMs: 250, Tier: "low", ResetArticles: 10076933, ResetHistoryArticles: 793770354},
			{Name: "alt.binaries.misc", Active: false},
		},
		"GroupQuery": "", "Tiers": AllTiers,
		"GroupTotal":  3,
		"Shown":       3,
		"CrawlersTab": template.HTML("<div>crawlers-frag</div>"),
		"FiltersTab":  template.HTML("<div>filters-frag</div>"),
		"Msg":         "",
		"Err":         "",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "settings.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"news.eweka.nl", "news.other.com", "omicron", "backup",
		// the aligned provider table: per-row external forms + add row + test
		`form="prov-1"`, `form="prov-del-1"`, `id="prov-new"`,
		"/admin/p/usenet/provider-test",
		// per-group tuning controls
		"alt.binaries.anime", "group-tune", "retention_days", "throttle_ms",
		// the tier <select> replaced the low_priority checkbox (migration 019);
		// assert the selected option too, so a broken {{if eq}} is caught here
		// rather than by an operator finding every group showing "Critical".
		`name="tier"`, `value="low" selected`,
		"group-move", "group-del", "groups-purge",
		// Reset Watermarks: the type-to-confirm prompt AND the hidden field
		// it fills. Without the field the handler always rejects, and the
		// button would look functional while doing nothing.
		"group-reset", `name="confirm"`, "Type the group name to confirm",
		// The prompt must state the ACTUAL article count. "everything already
		// fetched" reads as "the whole newsgroup", which is 80x larger on an
		// adopted install and is exactly the misreading that prompted this.
		"Re-reads 10076933 article", "not the whole newsgroup",
		// A group with no fetched coverage (or too fragmented to target)
		// gets a disabled marker, not a button that always errors.
		"watermark reset unavailable",
		// The history re-walk is a SEPARATE button with its own number, because
		// it is two orders of magnitude larger. Bundling them behind one click
		// would hide which one the operator is buying.
		`value="history"`, "Reopens the backfill over 793770354 article",
		"history re-walk unavailable",
		// tabbed layout on its own admin page (SlotAdminPage), forms post to /admin/p/usenet
		`class="nav tabs"`, `data-bs-toggle="tab"`, `id="providers"`, `id="newsgroups"`,
		"/admin/p/usenet/provider", "/admin/p/usenet/group-tune",
		// one page per plugin: crawlers + filters embed as tabs, width is a
		// container tier (compound .container.page selector), sections are
		// host-native card-header boxes
		`id="crawlers"`, `id="filters"`, "crawlers-frag", "filters-frag",
		`class="container page"`, `class="card-header"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered settings page is missing %q", want)
		}
	}
	// The stored password must never reach the browser.
	if strings.Contains(out, "value=\"secret") {
		t.Error("a password value was rendered into the page")
	}
}

// TestSettingsRendersWithNoProviders: a fresh install must not blow up.
func TestSettingsRendersWithNoProviders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "settings.html", map[string]any{
		"Servers": []provider{}, "DefaultConns": 10,
		"Knobs": []knob{}, "SkipBackfill": false, "Groups": nil,
		"GroupQuery": "", "Tiers": AllTiers, "GroupTotal": 0, "Shown": 0, "Msg": "", "Err": "",
		"CrawlersTab": template.HTML(""), "FiltersTab": template.HTML(""),
	})
	if err != nil {
		t.Fatalf("render with no providers: %v", err)
	}
	if !strings.Contains(buf.String(), "No providers configured") {
		t.Error("empty state not shown")
	}
}

// TestCrawlersRendersFleetAndWorkers exercises the new cards. A bad field
// reference inside a range only fails when the page is actually served, so it
// gets caught here instead of on the admin page.
func TestCrawlersRendersFleetAndWorkers(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "crawlers.html", map[string]any{
		"Stats":   pluginapi.IndexStats{},
		"Groups":  []crawlerGroupVM{},
		"Jobs":    nil,
		"Builder": BuilderInfo{},
		"Fleet": []providerVM{
			{Name: "Primary", Host: "news.eweka.nl:563", Backbone: "omicron",
				Role: roleActive, Enabled: true, Dialled: true, Open: 18, Target: 20, Resets: 2},
			{Name: "Standby", Host: "news.other.com:119", Backbone: "srv:2",
				Role: roleBackup, Enabled: true, Down: true, Dialled: true},
			{Name: "Parked", Host: "news.old.com:119", Backbone: "srv:3", Role: roleActive},
		},
		"Workers": []workerVM{
			{ID: "hostA/1/abcd", Me: true, Groups: 14},
			{ID: "hostB/1/efgh", Groups: 13},
		},
		"Pass": passVM{Any: true, Running: true, Groups: 5, Batches: 40, Failed: 2,
			Articles: 120000, Staged: 9000, Wire: "42.0 MB", Duration: "1m30s",
			Rate: "1333 art/s", Through: "0.47 MB/s", Providers: 2},
		"Backfill": passVM{Any: true, Articles: 777, Staged: 700, Batches: 9,
			Duration: "44s", Rate: "17 art/s"},
		"Pending": []pendingSet{
			{Base: "Some.Forming.Release", Group: "alt.binaries.anime", Have: 10, Need: 14, Segments: 10},
		},
		"Errors": []errorVM{{When: "10:04:11", Op: "usenet/crawl-fetch", Msg: "430 no such article"}},
		"Health": healthVM{Healthy: 80, Broken: 15, Dead: 5, Unknown: 100, Total: 200, HealthyPct: 40, BrokenPct: 7, DeadPct: 2},
		"IndexStats": indexStatsVM{
			GroupsActive: 28, GroupsTotal: 30,
			HaveCatalog: true, CatalogCached: true,
			Releases: 851454, TotalSize: "23.0 GB",
			Staging: stagingInfo{Mode: "redis", Keys: 1200, ReadyGroups: 4, MemUsedBytes: 1 << 30, MemMaxBytes: 8 << 30},
			MemUsed: "1.0 GB", MemMax: "8.0 GB",
		},
		"HostSink": true,

		"Msg": "",
		"Err": "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		// slim provider strip: connected state + open/target + the edit pencil.
		// "resets" is the pool-health signal — it was computed and thrown away
		// until 2026-07-26, which is why "no usable connection in pool" had no
		// visible counterpart on this page.
		"Primary", "connected", "18 / 20 connections", "benched", "2 resets",
		"#providers", "&#9998;",
		"hostA/1/abcd", "(this host)", "Crawler hosts",
		"Crawl in progress", "1333 art/s", "0.47 MB/s", "2 failed",
		"Recent errors", "430 no such article",
		// Index stats card: host-cached catalog + redis staging rows
		"Index stats", "851454", "host cache, refreshed hourly",
		"4 sets ready to assemble", "1.0 GB",
		// backfill line + the run buttons that moved into the pass card header
		"Last backfill", "777", "Crawl now", "Backfill now",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("crawlers page missing %q", want)
		}
	}
	// The moved surfaces must be GONE — they live on the Jobs tab now.
	for _, gone := range []string{"provider-test", "NZB health", "Builder"} {
		if strings.Contains(out, gone) {
			t.Errorf("crawlers page still contains %q (moved to the Jobs tab)", gone)
		}
	}
}

// TestJobsRendersPanes exercises the Jobs tab: one pane per pipeline job with
// status, Run-now, the log tail, and the Builder/health panels that moved
// here from the dashboard.
func TestJobsRendersPanes(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	panes := []jobPaneVM{
		{crawlerJobVM: crawlerJobVM{Name: "Usenet Crawler", Status: "running", Running: true,
			Next: "14:30:00", Logs: []string{"[14:12:01] crawling 28 group(s)"}},
			Slug: "crawler", Short: "Crawler", Action: "run-crawl"},
		{crawlerJobVM: crawlerJobVM{Name: "Usenet Builder", Status: "idle"},
			Slug: "builder", Short: "Builder", Action: "run-build"},
		{crawlerJobVM: crawlerJobVM{Name: "Usenet Health Check", Status: "idle"},
			Slug: "health", Short: "Health Check", Action: "run-health"},
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "jobs.html", map[string]any{
		"Jobs": panes, "PGStaging": false, "ReadyGroups": int64(4), "Evicted": int64(17),
		"Pending": []pendingSet{
			{Base: "Some.Forming.Release", Group: "alt.binaries.anime", Have: 10, Need: 14, Segments: 10},
		},
		"Builder": BuilderInfo{},
		"Health":  healthVM{Healthy: 80, Broken: 15, Dead: 5, Unknown: 100, Total: 200, HealthyPct: 40, BrokenPct: 7, DeadPct: 2},
		"Pass":    passVM{Any: true, Running: true, Articles: 120000, Staged: 9000, Groups: 5, Batches: 40, Duration: "1m30s", Rate: "1333 art/s"},
		"Backfill": passVM{Any: true, Articles: 777, Staged: 700, Batches: 9,
			Duration: "44s", Rate: "17 art/s"},
	})
	if err != nil {
		t.Fatalf("render jobs: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		// pills + panes
		`data-bs-target="#job-pane-crawler"`, "Run now", "run-crawl", "run-build", "run-health",
		"[14:12:01] crawling 28 group(s)",
		// builder pane (redis body + incomplete sample)
		"4</strong> complete set(s) queued for assembly", "17</span> hopeless set(s) evicted",
		"Some.Forming.Release", "10 / 14",
		// health pane
		"80 healthy", "15 broken", "100 unchecked",
		// crawler pane pass summary
		"Pass in progress:", "1333 art/s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("jobs page missing %q", want)
		}
	}
}

// TestJobsRendersCensusAndLiveness executes the census block and the
// worker-staleness banner — template regions no earlier fixture reached.
// html/template fails at Execute on a missing struct field, and a streamed
// render that dies mid-page shows only its first rows (a real bug class
// here), so every field the census table dereferences must be exercised.
func TestJobsRendersCensusAndLiveness(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 14, 12, 3, 0, time.UTC)
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "jobs.html", map[string]any{
		"Jobs": []jobPaneVM{
			{crawlerJobVM: crawlerJobVM{Name: "Usenet Crawler", Status: "idle", Duty: 87.2},
				Slug: "crawler", Short: "Crawler", Action: "run-crawl"},
			// The census card renders inside the builder pane.
			{crawlerJobVM: crawlerJobVM{Name: "Usenet Builder", Status: "idle"},
				Slug: "builder", Short: "Builder", Action: "run-build"},
		},
		"PGStaging": false, "ReadyGroups": int64(0), "Evicted": int64(3),
		"Pending": []pendingSet{},
		"Builder": BuilderInfo{}, "Health": healthVM{},
		"Pass": passVM{}, "Backfill": passVM{},
		"Schema":      "024_staging_census.sql",
		"WorkerStale": true, "WorkerLastSeen": "14:09:00",
		"Census": []censusRow{
			// The healthy shape, plus deltas and the deliberate-shed column.
			{At: at, ReadyDepth: 120, Sampled: 120, LiveCandidates: 100, FossilDropped: 20,
				MemUsedBytes: 6 << 30, MemMaxBytes: 8 << 30, MaxMemoryPolicy: "allkeys-lru",
				EvictedKeys: 1000, EvictedDelta: 40, ExpiredKeys: 500, ExpiredDelta: 5,
				HopelessSeen: 30, HopelessDelta: 7, PendingSets: 12},
			// The two ambiguous states the census must render distinctly: an
			// unreadable INFO (sentinel policy) and a pass that died before
			// sampling pending (-1).
			{At: at, MaxMemoryPolicy: "(unavailable)", PendingSets: -1},
		},
	})
	if err != nil {
		t.Fatalf("render jobs with census: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"maxmemory-policy: allkeys-lru", "plugin schema 024_staging_census.sql",
		"75%",        // 6/8 GB memory
		"1000 (+40)", // evicted with delta
		"30 (+7)",    // shed with delta
		"?",          // unreadable INFO renders as unknown, never "unbounded"
		"—",          // died-before-sampling pending marker
		"87% busy (1h)",
		"has not published telemetry since <strong>14:09:00</strong>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("census render missing %q", want)
		}
	}
}

// TestCrawlersRendersEmpty: a fresh install with no providers, no workers and no
// health data must still render.
func TestCrawlersRendersEmpty(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "crawlers.html", map[string]any{
		"Stats": pluginapi.IndexStats{}, "Groups": []crawlerGroupVM{}, "Jobs": nil,
		"Builder": BuilderInfo{}, "Fleet": []providerVM{}, "Workers": []workerVM{},
		"Health": healthVM{}, "Pass": passVM{}, "Errors": []errorVM{},
		"PGStaging": true, "RecentNzbs": nil,
		"Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	// The liveness card must degrade to an explanation, not a blank panel — an
	// empty crawler is the exact moment someone is looking at this page.
	if !strings.Contains(buf.String(), "Nothing built since the worker started") {
		t.Error("empty recently-built state not explained")
	}
	if !strings.Contains(buf.String(), "No providers configured") {
		t.Error("empty provider state not shown")
	}
}

// TestCrawlersRendersCoverage exercises the coverage table: the sparkline cells,
// the per-backbone grouping, and the backfill ETA. The empty-state test above
// renders none of that markup, so without this the whole block is uncovered.
func TestCrawlersRendersCoverage(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	// Two backbones: the page must key coverage per backbone, since article
	// numbers from one say nothing about the other.
	bbs := []backboneVM{
		{Name: "omicron", Groups: []crawlerGroupVM{{
			Name: "alt.binaries.anime", NZBs: 12, Staged: 3,
			Cells:     cellLevels(coverageCells([]articleRange{{Start: 0, End: 40}, {Start: 60, End: 99}}, 0, 99, 8)),
			Fragments: 2, RemainingFmt: "5,000",
			FwdAt: "2026-07-22 10:31", BackAt: "2026-01-01 12:11", NewFmt: "409,395,881",
		}}},
		{Name: "srv:2", Groups: []crawlerGroupVM{{
			Name: "alt.binaries.tv", NoCoverage: true,
		}}},
	}
	var groups []crawlerGroupVM
	for _, b := range bbs {
		groups = append(groups, b.Groups...)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "crawlers.html", map[string]any{
		"Stats":  pluginapi.IndexStats{TotalBackfillRemaining: 5000},
		"Groups": groups, "Backbones": bbs,
		"Backfill": passVM{Rate: "120 art/s"}, "BackfillETA": "3 hours",
		"Jobs": nil, "Builder": BuilderInfo{}, "Fleet": []providerVM{}, "Workers": []workerVM{},
		"Health": healthVM{}, "Pass": passVM{}, "Errors": []errorVM{},
		"PGStaging": true,
		"RecentNzbs": []recentNZBVM{
			{Title: "Some.Release", Group: "alt.binaries.anime", Size: "1.2 GB", Created: "12:05"},
		},
		"Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render coverage: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"cov-cells", "cc3", "alt.binaries.anime", "alt.binaries.tv",
		"3 hours", "120 art/s", "2 runs",
		// the legacy-format watermark line: ↩ back · ↑ fwd (+N new)
		"↩ 2026-01-01 12:11", "↑ 2026-07-22 10:31", "(+409,395,881 new)",
		"5,000 left",
		"Some.Release", "1.2 GB", // recently-built (telemetry ring)
		"omicron", "srv:2", // both backbones labelled when there is more than one
	} {
		if !strings.Contains(out, want) {
			t.Errorf("coverage render missing %q", want)
		}
	}
}

// TestFmtComma pins the thousands separator the coverage line depends on.
func TestFmtComma(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 999: "999", 1000: "1,000", 409395881: "409,395,881", -12345: "-12,345",
	} {
		if got := fmtComma(in); got != want {
			t.Errorf("fmtComma(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestFiltersRenders covers the blacklist + hit-counter page, including the
// invalid-rule warning — a rule edited straight into SQL is the one case where
// nothing else would tell the operator their rule is inert.
func TestFiltersRenders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "filters.html", map[string]any{
		"JunkRules": []junkOrderRow{},
		"Rules": []blacklistVM{
			{ID: 1, Pattern: "(?i)spam", Field: "poster", Enabled: true},
			{ID: 2, Pattern: "([unclosed", Field: "title", Invalid: "missing closing )"},
		},
		"Fields": blacklistFields,
		"Hits": []filterHitVM{
			{Kind: "junk", Rule: "bare-token", Count: 900, Pct: 90, Sample: "AbC123xyz", LastSeen: "12:00"},
			{Kind: "blacklist", Rule: "(?i)spam", Count: 100, Pct: 10, Sample: "Some.Release", LastSeen: "12:01"},
		},
		"TotalHits": 1000, "Msg": "", "Err": "",
		"Watched": []string{"tsukihime"},
		"PosterHits": []posterHitRow{
			{Poster: "tsukihime", Stage: "ingest", Reason: "staged", Count: 4210,
				Sample: "[Judas] Liar Game - S01E17.mkv", LastAt: "2026-07-28 04:10"},
			{Poster: "tsukihime", Stage: "build", Reason: "under_1mib", Count: 96,
				Sample: "[Judas] Liar Game - S01E17", LastAt: "2026-07-28 04:11"},
			{Poster: "tsukihime", Stage: "build", Reason: "built", Count: 4,
				Sample: "[Erai-raws] Neko to Ryuu - 04", LastAt: "2026-07-28 04:12"},
		},
	})
	if err != nil {
		t.Fatalf("render filters: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		// Watched posters: the card exists to answer "why am I missing this
		// poster", so the OUTCOME names and the counts must render, not just
		// the form. Successes too — a watch that only ever shows drops reads
		// as broken even when it is working.
		"Watched posters", "poster-watch", "tsukihime",
		"under_1mib", "staged", "built", "4210",

		"(?i)spam", "poster", "bare-token", "this rule is inert",
		// forms post under the unified /admin/p/usenet page (filters tab)
		"usenet/filter-add", "usenet/filter-toggle", "usenet/filter-del", "usenet/filter-reset",
		"90.0%", "1000 release(s) dropped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("filters page missing %q", want)
		}
	}
	// Every field must be offered, or a rule type becomes unreachable from the UI.
	for _, f := range blacklistFields {
		if !strings.Contains(out, ">"+f+"</option>") {
			t.Errorf("field %q not offered in the form", f)
		}
	}
}

// TestJobsWidgetRenders executes the one template nothing else exercised.
// Parsing catches syntax; only execution catches a field missing from the VM,
// and html/template aborts mid-stream when that happens.
func TestJobsWidgetRenders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "jobswidget.html", map[string]any{
		"Jobs": []crawlerJobVM{
			{Name: "Usenet Crawler", Status: "idle", Activity: "last pass: 12 staged", Running: false},
			{Name: "Usenet Backfill", Status: "running", Running: true},
		},
		"Stats": pluginapi.IndexStats{TotalNZBs: 42, TotalStaged: 7, TotalBackfillRemaining: 1000},
	})
	if err != nil {
		t.Fatalf("render jobswidget: %v", err)
	}
	for _, want := range []string{"Usenet Crawler", "42", "last pass: 12 staged"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("jobswidget missing %q", want)
		}
	}
}

// Inline handlers must be syntactically valid JavaScript, and the way they stop
// being valid is a RAW newline inside a string literal — JS has no multi-line
// string literals, so one unterminates the string and the whole handler throws.
//
// This shipped. The Reset Watermarks prompt was written with real line breaks
// instead of \n escapes, so onsubmit threw, the hidden confirm field was never
// populated, and every reset came back "the typed name did not match" no matter
// what the operator typed. The existing test asserted the prompt TEXT was
// present, which it was — in a handler that could not run.
func TestInlineHandlersHaveNoRawNewlines(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, name := range []string{"settings.html", "crawlers.html"} {
		var buf bytes.Buffer
		// Errors are ignored: several templates need data this test does not
		// supply. Whatever rendered before the error is still worth scanning,
		// and the dedicated render tests cover completeness.
		_ = tmpl.ExecuteTemplate(&buf, name, map[string]any{
			"Servers": []provider{}, "DefaultConns": 10, "Knobs": []knob{},
			"SkipBackfill": false, "Tiers": AllTiers,
			"Groups": []pluginapi.GroupInfo{{
				Name: "alt.binaries.example", Active: true,
				ResetArticles: 10076933, ResetHistoryArticles: 793770354,
			}},
			"GroupQuery": "", "GroupTotal": 1, "Shown": 1, "Msg": "", "Err": "",
			"CrawlersTab": template.HTML(""), "FiltersTab": template.HTML(""),
			"Stats": pluginapi.IndexStats{}, "Fleet": nil, "Workers": nil,
			"Pass": passVM{}, "Backfill": passVM{}, "Health": healthVM{},
			"IndexStats": indexStatsVM{}, "Backbones": nil,
		})
		if buf.Len() == 0 {
			t.Errorf("%s rendered nothing — this test would pass vacuously", name)
			continue
		}
		for _, attr := range []string{"onsubmit=\"", "onclick=\"", "onchange=\""} {
			rest := buf.String()
			for {
				i := strings.Index(rest, attr)
				if i < 0 {
					break
				}
				rest = rest[i+len(attr):]
				end := strings.Index(rest, "\"")
				if end < 0 {
					break
				}
				if body := rest[:end]; strings.ContainsAny(body, "\n\r") {
					t.Errorf("%s: %s handler contains a raw newline — JS string literals "+
						"cannot span lines, so this handler throws and never runs:\n%.160s",
						name, strings.TrimSuffix(attr, "=\""), body)
				}
				found++
				rest = rest[end:]
			}
		}
	}
	// Anti-vacuous. Not every template has inline handlers, but if NONE of them
	// do, the fixture has stopped rendering the rows that carry them and this
	// test silently stops testing anything. It did exactly that on the first
	// attempt: it passed against a template with the bug deliberately
	// reintroduced, because settings.html was not rendering far enough to emit
	// the handler at all.
	if found == 0 {
		t.Error("no inline handlers found in any template — the fixture no longer " +
			"renders them, so this test is not checking anything")
	}
}

// The junk-rule ORDER card. Two page-render failures this month came from
// templates that compiled fine and blew up at runtime, so the card gets its
// own execution test over a populated model — including the states that only
// appear on real data: a disabled rule, a sized rule, a never-fired rule, and
// both directions of drift.
func TestJunkOrderCardRenders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	rows := rankJunkRules([]junkRuleStat{
		{Position: 10, Name: "long_alnum_run", Kind: "regex", Enabled: true,
			Hits: 6_153_008_122, LastSample: "473e11675bdc5e4e3bc594b66b5deaa2"},
		{Position: 40, Name: "software_warez", Kind: "regex", Enabled: true, Hits: 20_952_828,
			LastSample: "Adobe Dreamweaver 2024 cracked license"},
		{Position: 130, Name: "single_token_20", Kind: "regex", Enabled: true,
			Hits: 3_564_164_538, LastSample: "5ef86c80883d3e835814.bin"},
		{Position: 190, Name: "short_lowercase_token", Kind: "heuristic", Enabled: false, Hits: 0},
		{Position: 923, Name: "under_1mib", Kind: "regex", Enabled: true, Hits: 4_165},
	}, map[string]bool{"under_1mib": true})

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "filters.html", map[string]any{
		"JunkRules": rows,
		"Rules":     []blacklistVM{}, "Fields": blacklistFields,
		"Hits": []filterHitVM{}, "TotalHits": 0,
		"Watched": []string{}, "PosterHits": []posterHitRow{},
		"Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"evaluation order",
		"long_alnum_run", "single_token_20", "software_warez",
		"6,153,008,122", // formatted in Go, not by a template helper
		"junk-move", "junk-order", "junk-toggle",
		"type reorder", // the confirm gate on the bulk apply
		"sized",        // the build-path-only marker
	} {
		if !strings.Contains(out, want) {
			t.Errorf("junk-order card is missing %q", want)
		}
	}
	// single_token_20 runs 3rd among ingest rules but earns 2nd → +1, and the
	// card must show that as the expensive direction.
	if !strings.Contains(out, "+1") {
		t.Error("positive drift is not rendered — that is the whole signal of the card")
	}
}

// Instrument counters must not render as rules.
//
// filter_hits carries both, and listing them together buried 26 real rules
// under 2,257 ungrouped stems — an operator reading the page concluded the
// install had grown 100k rules. The split is the fix; this pins it.
func TestDiagnosticsRenderSeparatelyFromRules(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "filters.html", map[string]any{
		"JunkRules": []junkOrderRow{},
		"Rules":     []blacklistVM{}, "Fields": blacklistFields,
		"Hits": []filterHitVM{
			{Kind: "junk", Rule: "long_alnum_run", Count: 6_290_546_735, Pct: 99.9,
				Sample: "473e11675bdc5e4e3bc594b66b5deaa2", LastSeen: "01:20"},
		},
		"TotalHits": 6_290_546_735,
		"Diag": buildDiagVM(diagPage{
			Rows: []filterHitRow{
				{Kind: "ungrouped", Rule: "two-blue.vol#+#.par#", TotalCount: 48_261,
					LastSample: "two-blue.vol029+16.par2"},
				{Kind: "merge_suspect", Rule: "E01|E02", TotalCount: 3, LastSample: "Show E01 / E02"},
			},
			Kinds:     []diagKind{{Kind: "ungrouped", Rows: 2255}, {Kind: "merge_suspect", Rows: 2}},
			TotalRows: 2257, TotalHits: 187_172_945,
		}, "", 1),
		"Watched": []string{}, "PosterHits": []posterHitRow{},
		"Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"Grouping diagnostics",
		"Nothing here was dropped", // the sentence that prevents the misreading
		// html/template escapes '+' to &#43; in HTML text, so the stem is
		// asserted in its wire form — the browser shows the literal.
		"two-blue.vol#&#43;#.par#",
		"1-2 of 2,257 distinct",                           // says what was NOT shown, never silently truncates
		"ungrouped <span class=\"tab-count\">2255</span>", // per-instrument chips
		">next</a>", // a second page exists, so the pager is live
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnostics card missing %q", want)
		}
	}
	// The rules card must not carry the diagnostic rows.
	cut := strings.Index(out, "Grouping diagnostics")
	if cut < 0 {
		t.Fatal("no diagnostics card rendered at all")
	}
	if strings.Contains(out[:cut], "two-blue") {
		t.Error("an ungrouped stem rendered in the RULES table — that is the bug")
	}
}

// The per-group release column is the difference between "this group is
// quiet" and "this group has never produced anything" — and in sink=host
// mode the plugin's own table cannot answer it, so the column used to be
// hidden entirely. An operator watching a fossil group therefore saw a fresh
// last_crawl every pass and no way to tell it apart from a healthy one, which
// is exactly the question that reached the database instead of the page.
func TestCrawlersRendersPerGroupCountsInHostMode(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	groups := []crawlerGroupVM{
		{Name: "alt.binaries.anime", NZBs: 4211, CoveredFmt: "62%", Cells: []int{3, 2}},
		// The fossil: crawled every pass, nothing to show for it.
		{Name: "alt.binaries.multimedia.anime.dialup", NZBs: 0, EmptyOnServer: true, LastCrawl: "08:52:14"},
	}
	render := func(data map[string]any) string {
		base := map[string]any{
			"Stats": pluginapi.IndexStats{}, "Groups": groups,
			"Backbones": []backboneVM{{Name: "netnews", Groups: groups}},
			"Jobs":      nil, "Builder": BuilderInfo{}, "Fleet": []providerVM{}, "Workers": []workerVM{},
			"Health": healthVM{}, "Pass": passVM{}, "Backfill": passVM{}, "Errors": []errorVM{},
			"Msg": "", "Err": "",
		}
		for k, v := range data {
			base[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "crawlers.html", base); err != nil {
			t.Fatalf("render: %v", err)
		}
		return buf.String()
	}

	// Host sink WITH the capability answering: real counts, marked stale.
	out := render(map[string]any{
		"HostSink": true, "HaveGroupCounts": true, "GroupCountsCached": true,
	})
	for _, want := range []string{
		">NZBs", "4211",
		"~1h",                              // the count is an hourly cache, and says so
		"provider reports the group empty", // the fossil's real diagnosis
		"never produced a release",         // the zero is deliberate, not missing data
	} {
		if !strings.Contains(out, want) {
			t.Errorf("host-mode render missing %q", want)
		}
	}

	// Host sink with NO capability: the column hides rather than printing a
	// column of zeros, which reads as "every group is dead".
	out = render(map[string]any{"HostSink": true, "HaveGroupCounts": false})
	if strings.Contains(out, ">NZBs") {
		t.Error("unanswerable per-group counts still rendered a NZBs column")
	}
	if !strings.Contains(out, "provider reports the group empty") {
		t.Error("the empty-server state is independent of the count capability")
	}

	// Internal sink: the plugin's own join answers, with no staleness note.
	out = render(map[string]any{"HostSink": false, "HaveGroupCounts": true, "GroupCountsCached": false})
	if !strings.Contains(out, "4211") {
		t.Error("internal-mode render lost the per-group count")
	}
	if strings.Contains(out, "~1h") {
		t.Error("internal-mode counts are live; they must not claim an hourly cache")
	}
}

// The NFO tab renders, and the settings page carries it.
//
// Two failures this catches, both silent. A tab pane whose data key is never
// supplied renders as an empty div — the tab appears in the strip and clicking
// it shows nothing, which reads as a broken page rather than a missing wire.
// And the counters are the tab's whole purpose: "waiting" is what distinguishes
// a job that is not running from a job with nothing to do.
func TestNFOTabRenders(t *testing.T) {
	tmpl := parseViewTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "nfo.html", nfoTabVM{
		Enabled: true, BudgetMB: 64, BatchSize: 100,
		Stored: 12, Pending: 906, Unavailable: 3,
		Rows: []nfoRowVM{{ID: 99, Title: "Some.Release-GROUP",
			Group: "alt.binaries.anime", Bytes: 2048, Lines: 40, Preview: "scene notes"}},
	}); err != nil {
		t.Fatalf("render nfo.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"906", "Some.Release-GROUP", "alt.binaries.anime", "/release/99"} {
		if !strings.Contains(out, want) {
			t.Errorf("NFO tab missing %q", want)
		}
	}
	// Enabled, so no "off" warning.
	if strings.Contains(out, "nfo_enabled") {
		t.Error("an enabled job still showed the 'switch me on' notice")
	}
}

// Off is the default, and the tab has to say so — otherwise zero stored reads
// as a broken job rather than one nobody has turned on.
func TestNFOTabSaysWhenItIsOff(t *testing.T) {
	tmpl := parseViewTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "nfo.html", nfoTabVM{BudgetMB: 64, BatchSize: 100}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "nfo_enabled") {
		t.Error("a disabled job did not say how to enable it")
	}
}

// A host with no NFO store gets an explanation, not an empty table implying
// the job ran and found nothing.
func TestNFOTabSaysWhenTheHostHasNoStore(t *testing.T) {
	tmpl := parseViewTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "nfo.html", nfoTabVM{NoStore: true}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "not registered an NFO store") {
		t.Error("a host with no NFO store got no explanation")
	}
}

// The settings page must actually place the fragment, or the tab is a dead
// link to an empty pane.
func TestSettingsPageCarriesTheNFOTab(t *testing.T) {
	tmpl := parseViewTemplates(t)
	var buf bytes.Buffer
	err := tmpl.ExecuteTemplate(&buf, "settings.html", map[string]any{
		// Seeded rather than nil: settings.html applies len to these, and
		// len of a nil interface is a render error rather than zero.
		"Servers": []provider{}, "Knobs": nil, "Groups": []pluginapi.GroupInfo{},
		"Tiers":       AllTiers,
		"CrawlersTab": template.HTML(""), "JobsTab": template.HTML(""),
		"FiltersTab": template.HTML(""),
		"NFOTab":     template.HTML("<div>nfo-frag</div>"),
	})
	if err != nil {
		t.Fatalf("render settings: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `href="#nfo"`) {
		t.Error("no NFO entry in the tab strip")
	}
	if !strings.Contains(out, "nfo-frag") {
		t.Error("the NFO pane did not receive its fragment — the tab would open " +
			"on an empty pane, which reads as a broken page")
	}
}

// parseViewTemplates is the shared parse for the tab render tests below.
func parseViewTemplates(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return tmpl
}
