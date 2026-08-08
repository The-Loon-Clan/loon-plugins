package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Render the page against real data.
//
// This is the test class this repo learned to write the hard way: html/template
// STREAMS, so a field the view model does not have aborts execution mid-output
// and the page silently truncates at the first row. Executing to completion and
// asserting on the tail is what catches it — a test that only checks "no error"
// would not, because a truncated render can still return nil.
func renderFixture(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	m := NewMemStore()
	p := &Plugin{store: m}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return p, m
}

func TestAdminPageRendersEveryEventShape(t *testing.T) {
	ctx := context.Background()
	p, m := renderFixture(t)
	now := time.Now()

	// One of each shape, because Shape() has four branches and the template
	// reads fields that differ between them.
	perpetual := now.Add(-24 * time.Hour)
	bounded := now.Add(-time.Hour)
	for _, ev := range []pluginapi.ScheduledEvent{
		{Slug: "daily-reset", Name: "Daily reset", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "happy-hour", Name: "Happy hour", Cron: "0 18 * * *", Duration: 2 * time.Hour, Timezone: "UTC", Enabled: true},
		{Slug: "site-launch", Name: "Launch", StartsAt: &perpetual, Timezone: "UTC", Enabled: true},
		{Slug: "launch-week", Name: "Launch week", StartsAt: &bounded, Duration: 7 * 24 * time.Hour, Timezone: "UTC", Enabled: true},
		{Slug: "retired", Name: "Retired season", Cron: "0 0 1 * *", Timezone: "UTC", Enabled: false},
	} {
		if err := m.UpsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
		ws, err := GenerateWindows(ev, now.Add(-48*time.Hour), now.Add(48*time.Hour))
		if err != nil {
			t.Fatalf("%s: %v", ev.Slug, err)
		}
		if _, err := m.InsertWindows(ctx, ev.Slug, ws); err != nil {
			t.Fatal(err)
		}
	}

	out, err := p.renderCtx(ctx)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	// Every slug must appear. A missing one at the END is the streamed-abort
	// signature: the first rows render, then execution stops.
	for _, slug := range []string{"daily-reset", "happy-hour", "site-launch", "launch-week", "retired"} {
		if !strings.Contains(html, slug) {
			t.Errorf("%q is missing — if the earlier rows rendered, the template aborted mid-stream", slug)
		}
	}
	// The form is the last thing on the page, so its presence proves execution
	// reached the end rather than stopping after the table.
	if !strings.Contains(html, "event-save") {
		t.Error("the create form is absent; the render did not reach the end of the template")
	}

	// The shapes, spelled out. An operator should not have to re-derive "cron
	// plus nullable duration" from two columns.
	for _, want := range []string{
		"one-off, never closes",
		"recurring, contiguous",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("no row explains %q", want)
		}
	}

	// The perpetual sentinel must read as "never", never as a year-9999 date.
	if strings.Contains(html, "9999") {
		t.Error("the perpetual sentinel leaked to the page as a date")
	}
	if !strings.Contains(html, "never") {
		t.Error("the perpetual one-off does not say never anywhere")
	}

	// The launch event started yesterday and never closes, so it must be open.
	if !strings.Contains(html, "bg-success") {
		t.Error("nothing is shown as open, though a perpetual window opened yesterday")
	}
	// And the disabled one must be reported as disabled rather than merely closed.
	if !strings.Contains(html, "disabled") {
		t.Error("the disabled event is not marked disabled")
	}
}

// The windows panel is a separate template branch and renders only when a slug
// is picked, so it needs its own pass or it is never executed by a test.
func TestWindowsPanelRenders(t *testing.T) {
	ctx := context.Background()
	p, m := renderFixture(t)
	ev := pluginapi.ScheduledEvent{Slug: "season", Cron: "0 0 1 * *", Timezone: "UTC", Enabled: true}
	if err := m.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ws, _ := GenerateWindows(ev, now.Add(-60*24*time.Hour), now.Add(60*24*time.Hour))
	if _, err := m.InsertWindows(ctx, "season", ws); err != nil {
		t.Fatal(err)
	}

	out, err := p.renderPage(ctx, "season", "", "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, "Windows") {
		t.Fatal("the windows panel did not render for a picked event")
	}
	// Past / open / future are three template branches; at least one past and
	// one future window exist in the fixture.
	if !strings.Contains(html, "future") || !strings.Contains(html, "past") {
		t.Error("the window state column is not distinguishing past from future")
	}
}

// An empty install must render, and say what to do. A blank table with no
// message reads as broken.
func TestEmptyPageSaysWhatToDo(t *testing.T) {
	p, _ := renderFixture(t)
	out, err := p.renderCtx(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "No events yet") {
		t.Error("an empty install renders a bare table with no explanation")
	}
}

// The findings banner renders, with all three severity styles and its fixes.
//
// The banner is the only part of this page that says something is WRONG, so a
// render bug here is the failure mode the validator exists to prevent, wearing a
// different hat: a stalled generator that nobody is told about.
func TestFindingsBannerRenders(t *testing.T) {
	p, _ := renderFixture(t)
	vm := adminVM{Now: time.Now(), Findings: []Finding{
		{SeverityError, "event daily", "has a cron but no windows at all", "trigger the job"},
		{SeverityWarn, "event weekly", "windows run out in 72h", "the generator keeps 45 days ahead"},
		{SeverityInfo, "event next-year", "no window yet", ""},
	}}

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "events_admin.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()

	for _, want := range []string{
		"Window health",
		"no windows at all", "run out in 72h", "no window yet",
		// Severity must be VISIBLE, not merely worded — an operator scans colour.
		"bg-danger", "bg-warning", "bg-secondary",
		"trigger the job", // the fix, which is what makes it actionable
	} {
		if !strings.Contains(out, want) {
			t.Errorf("findings banner missing %q", want)
		}
	}
	// A finding with no fix must not render a dangling arrow.
	if strings.Contains(out, "&rarr; </span>") {
		t.Error("a fix-less finding rendered an empty arrow")
	}
	// And the table below must still render — the banner is prepended, not
	// substituted, and html/template streams.
	if !strings.Contains(out, "event-save") {
		t.Error("the page did not reach its form; the banner truncated the render")
	}
}

// No findings, no banner. An empty "Window health" card on a healthy site is
// noise that trains an operator to skip the one place that matters.
func TestNoBannerWhenHealthy(t *testing.T) {
	p, _ := renderFixture(t)
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "events_admin.html", adminVM{Now: time.Now()}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(sb.String(), "Window health") {
		t.Error("the health card rendered with nothing to report")
	}
}
