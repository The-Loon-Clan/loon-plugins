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
		"Groups":       nil,
		"GroupQuery":   "",
		"GroupTotal":   0,
		"Shown":        0,
		"Msg":          "",
		"Err":          "",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "settings.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"news.eweka.nl", "news.other.com", "omicron", "backup", "Add a provider"} {
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
		"Health": healthVM{}, "AutoRefresh": false, "Msg": "", "Err": "",
	})
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	if !strings.Contains(buf.String(), "No providers configured") {
		t.Error("empty provider state not shown")
	}
}
