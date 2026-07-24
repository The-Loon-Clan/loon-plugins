package usenet

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

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
		"Server":       pluginapi.Server{Host: "news.eweka.nl", Port: 563, TLS: true, Username: "u"},
		"Knobs":        []knob{{Key: "connections", Label: "NNTP connections", Value: 10, Help: "h"}},
		"SkipBackfill": false,
		"Groups": []pluginapi.GroupInfo{
			{Name: "alt.binaries.anime", Active: true, NZBs: 4211, RetentionDays: 0, ThrottleMs: 0},
			{Name: "alt.binaries.hdtv", Active: true, NZBs: 900, RetentionDays: 30, ThrottleMs: 250, LowPriority: true},
			{Name: "alt.binaries.misc", Active: false},
		},
		"GroupQuery":  "",
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
		"news.eweka.nl", "news.other.com", "omicron", "backup", "Add a provider",
		// per-group tuning controls
		"alt.binaries.anime", "group-tune", "retention_days", "throttle_ms",
		"low_priority", "group-move", "group-del", "groups-purge",
		// tabbed layout on its own admin page (SlotAdminPage), forms post to /admin/p/usenet
		`class="nav tabs"`, `data-bs-toggle="tab"`, `id="providers"`, `id="newsgroups"`,
		"/admin/p/usenet/provider", "/admin/p/usenet/group-tune",
		// one page per plugin: crawlers + filters embed as tabs, width is a
		// container tier (compound .container.page-wide selector)
		`id="crawlers"`, `id="filters"`, "crawlers-frag", "filters-frag",
		`class="container page-wide"`,
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
		"Servers": []provider{}, "DefaultConns": 10, "Server": pluginapi.Server{},
		"Knobs": []knob{}, "SkipBackfill": false, "Groups": nil,
		"GroupQuery": "", "GroupTotal": 0, "Shown": 0, "Msg": "", "Err": "",
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
		"Errors":      []errorVM{{When: "10:04:11", Op: "usenet/crawl-fetch", Msg: "430 no such article"}},
		"Health":      healthVM{Healthy: 80, Broken: 15, Dead: 5, Unknown: 100, Total: 200, HealthyPct: 40, BrokenPct: 7, DeadPct: 2},
		"AutoRefresh": false,
		"Msg":         "",
		"Err":         "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"news.eweka.nl:563", "omicron", "18 / 20", "benched",
		"hostA/1/abcd", "(this host)", "healthy", "Crawler hosts",
		"Crawl in progress", "1333 art/s", "0.47 MB/s", "2 failed",
		"Recent errors", "430 no such article",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("crawlers page missing %q", want)
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
		"RecentArticles": nil, "RecentNzbs": nil,
		"AutoRefresh": false, "Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	// The liveness card must degrade to an explanation, not a blank panel — an
	// empty crawler is the exact moment someone is looking at this page.
	if !strings.Contains(buf.String(), "Nothing staged") {
		t.Error("empty recent-activity state not explained")
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
			Cover:     pluginapi.CoverageBar{BackPct: 20, HavePct: 70, NewPct: 10, Known: true},
			Cells:     cellLevels(coverageCells([]articleRange{{Start: 0, End: 40}, {Start: 60, End: 99}}, 0, 99, 8)),
			Fragments: 2, Remaining: 5000, FwdDate: "2026-07-22", BackDate: "2026-01-01",
		}}},
		{Name: "srv:2", Groups: []crawlerGroupVM{{
			Name: "alt.binaries.tv", Cover: pluginapi.CoverageBar{Known: false},
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
		"RecentArticles": []recentArticleVM{
			{Subject: "[01/12] Some.Release.mkv", Group: "alt.binaries.anime", Size: "42.0 MB", Posted: "12:00"},
		},
		"RecentNzbs": []recentNZBVM{
			{Title: "Some.Release", Group: "alt.binaries.anime", Size: "1.2 GB", Created: "12:05"},
		},
		"AutoRefresh": false, "Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render coverage: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"cov-cells", "cc3", "alt.binaries.anime", "alt.binaries.tv",
		"3 hours", "120 art/s", "2 runs",
		"Some.Release.mkv", "42.0 MB", "1.2 GB", // recent activity, both columns
		"omicron", "srv:2", // both backbones labelled when there is more than one
	} {
		if !strings.Contains(out, want) {
			t.Errorf("coverage render missing %q", want)
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
	})
	if err != nil {
		t.Fatalf("render filters: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
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
