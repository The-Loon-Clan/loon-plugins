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
