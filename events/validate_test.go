package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func findingFor(fs []Finding, subject, contains string) *Finding {
	for i := range fs {
		if fs[i].Subject == subject && strings.Contains(fs[i].Problem, contains) {
			return &fs[i]
		}
	}
	return nil
}

var vNow = at("2026-03-01T12:00:00Z")

// A healthy configuration must produce NOTHING.
//
// A validator that always finds something is one an operator learns to ignore,
// and then it stops working the day it matters. This is the assertion that keeps
// the other cases honest.
func TestValidateSilentOnHealthyEvents(t *testing.T) {
	evs := []pluginapi.ScheduledEvent{
		{Slug: "daily", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "happy-hour", Cron: "0 18 * * *", Duration: 2 * time.Hour, Timezone: "UTC", Enabled: true},
	}
	cov := map[string]Coverage{
		"daily":      {Slug: "daily", Windows: 45, LastEnd: vNow.Add(45 * 24 * time.Hour)},
		"happy-hour": {Slug: "happy-hour", Windows: 45, LastEnd: vNow.Add(45 * 24 * time.Hour), Gaps: 44},
	}
	if got := validateEvents(evs, cov, vNow); len(got) != 0 {
		t.Errorf("healthy events produced %d finding(s): %+v", len(got), got)
	}
}

// The headline case, and the reason this file exists: the generator stalls, the
// windows run out, and everything gated on the event silently stops happening.
func TestValidateCatchesTheStalledGenerator(t *testing.T) {
	evs := []pluginapi.ScheduledEvent{
		{Slug: "none-at-all", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "exhausted", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "running-out", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
	}
	cov := map[string]Coverage{
		"none-at-all": {Slug: "none-at-all"},
		"exhausted":   {Slug: "exhausted", Windows: 10, LastEnd: vNow.Add(-time.Hour)},
		"running-out": {Slug: "running-out", Windows: 10, LastEnd: vNow.Add(72 * time.Hour)},
	}
	got := validateEvents(evs, cov, vNow)

	if f := findingFor(got, "event none-at-all", "no windows at all"); f == nil || f.Severity != SeverityError {
		t.Errorf("cron event with no windows: got %+v, want an error", f)
	}
	if f := findingFor(got, "event exhausted", "every window is in the past"); f == nil || f.Severity != SeverityError {
		t.Errorf("exhausted runway: got %+v, want an error", f)
	}
	if f := findingFor(got, "event running-out", "run out in 72h"); f == nil || f.Severity != SeverityWarn {
		t.Errorf("short runway: got %+v, want a warning", f)
	}
	// Every finding must carry a fix. A finding with none is a complaint.
	for _, f := range got {
		if f.Fix == "" {
			t.Errorf("%s: %q has no fix", f.Subject, f.Problem)
		}
	}
}

// Gaps matter only where windows are supposed to be contiguous. For a bounded
// event the gaps ARE the design: summer runs 64 days and then there is nothing
// until next July.
func TestValidateGapsOnlyMatterWhenContiguous(t *testing.T) {
	evs := []pluginapi.ScheduledEvent{
		{Slug: "reset", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "season", Cron: "0 0 1 7 *", Duration: 64 * 24 * time.Hour, Timezone: "UTC", Enabled: true},
	}
	cov := map[string]Coverage{
		"reset":  {Slug: "reset", Windows: 40, Gaps: 3, LastEnd: vNow.Add(45 * 24 * time.Hour)},
		"season": {Slug: "season", Windows: 5, Gaps: 4, LastEnd: vNow.Add(45 * 24 * time.Hour)},
	}
	got := validateEvents(evs, cov, vNow)

	if f := findingFor(got, "event reset", "3 gap(s)"); f == nil || f.Severity != SeverityWarn {
		t.Errorf("gaps in a contiguous event: got %+v, want a warning", f)
	}
	if f := findingFor(got, "event season", "gap(s)"); f != nil {
		t.Errorf("a bounded event was reported for having gaps, which is what a season IS: %+v", f)
	}
}

// ADAPTATION 1. Rewards never generated windows for a cron-less event, so "no
// windows" was unambiguous there. Here the generator does make them — but only
// once the date is inside the horizon, so a one-off booked for next year has
// none and is perfectly healthy.
func TestValidateOneOffBeyondTheHorizonIsNotBroken(t *testing.T) {
	future := vNow.Add(200 * 24 * time.Hour)
	past := vNow.Add(-48 * time.Hour)
	evs := []pluginapi.ScheduledEvent{
		{Slug: "next-year", StartsAt: &future, Timezone: "UTC", Enabled: true},
		{Slug: "should-exist", StartsAt: &past, Timezone: "UTC", Enabled: true},
	}
	cov := map[string]Coverage{"next-year": {Slug: "next-year"}, "should-exist": {Slug: "should-exist"}}
	got := validateEvents(evs, cov, vNow)

	f := findingFor(got, "event next-year", "beyond the")
	if f == nil {
		t.Fatal("a far-future one-off produced no finding at all; the operator should still be told why it has no window")
	}
	if f.Severity != SeverityInfo {
		t.Errorf("far-future one-off is %s, want info — it is not broken, it has not happened", f.Severity)
	}

	// But one whose date has PASSED with no window is a stalled generator.
	if f := findingFor(got, "event should-exist", "has no window"); f == nil || f.Severity != SeverityError {
		t.Errorf("past one-off with no window: got %+v, want an error", f)
	}
}

// A perpetual window's stored end is a year-9999 sentinel, so a runway measured
// against it would report eight thousand years of headroom — true, useless, and
// the sort of number that makes an operator distrust the page.
//
// This asserts the PROPERTY, not the mechanism. It began as a check on a
// dedicated perpetual guard; mutation-checking showed deleting that guard changed
// nothing, because the one-off exemption already covered it and only a one-off
// can be perpetual. The guard went, the test stayed — what matters is that no
// runway language ever reaches the page for an event that cannot run out.
func TestValidatePerpetualWindowNeedsNoRunway(t *testing.T) {
	start := vNow.Add(-24 * time.Hour)
	evs := []pluginapi.ScheduledEvent{{Slug: "site-launch", StartsAt: &start, Timezone: "UTC", Enabled: true}}
	cov := map[string]Coverage{"site-launch": {Slug: "site-launch", Windows: 1, LastEnd: PerpetualEnd}}

	got := validateEvents(evs, cov, vNow)
	if len(got) != 0 {
		t.Errorf("a perpetual one-off produced %d finding(s): %+v", len(got), got)
	}
	// And the sentinel must not leak into a message as a runway figure.
	for _, f := range got {
		if strings.Contains(f.Problem, "9999") || strings.Contains(f.Problem, "run out") {
			t.Errorf("perpetual window described as a runway: %q", f.Problem)
		}
	}
}

// ADAPTATION 2. A bounded one-off whose window has closed has not "run out" —
// it is over, which is what a one-off does. Rewards got this right by gating the
// runway check on having a cron, and the reason is worth keeping written down
// because the check reads like it should apply to everything.
func TestValidateFinishedOneOffIsNotAFailure(t *testing.T) {
	start := vNow.Add(-30 * 24 * time.Hour)
	evs := []pluginapi.ScheduledEvent{
		{Slug: "launch-week", StartsAt: &start, Duration: 7 * 24 * time.Hour, Timezone: "UTC", Enabled: true},
	}
	cov := map[string]Coverage{
		"launch-week": {Slug: "launch-week", Windows: 1, LastEnd: start.Add(7 * 24 * time.Hour)},
	}
	if got := validateEvents(evs, cov, vNow); len(got) != 0 {
		t.Errorf("a finished one-off was reported as broken: %+v", got)
	}
}

// A disabled event opening nothing is not a fault, it is the point. Reporting it
// would bury the live ones.
func TestValidateIgnoresDisabledEvents(t *testing.T) {
	evs := []pluginapi.ScheduledEvent{{Slug: "retired", Cron: "0 0 * * *", Timezone: "UTC"}}
	if got := validateEvents(evs, map[string]Coverage{"retired": {Slug: "retired"}}, vNow); len(got) != 0 {
		t.Errorf("disabled event produced %d finding(s): %+v", len(got), got)
	}
}

// Errors above warnings above info. An operator scanning the banner needs what
// is broken before what is untidy.
func TestValidateSortsBySeverity(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	future := vNow.Add(200 * 24 * time.Hour)
	for _, ev := range []pluginapi.ScheduledEvent{
		{Slug: "a-info", StartsAt: &future, Timezone: "UTC", Enabled: true},
		{Slug: "b-error", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
	} {
		if err := m.UpsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	p := &Plugin{store: m}
	got, err := p.Validate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("first finding is %s, want error — severity is not leading the list", got[0].Severity)
	}
}

// The MemStore's Coverage must agree with the SQL about two things a test double
// gets wrong by default: an event with NO windows still gets a row (the PG side
// LEFT JOINs for exactly this), and a "gap" means the same thing on both sides.
func TestMemCoverageMatchesTheSQLDefinition(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	day := at("2026-03-01T00:00:00Z")

	if err := m.UpsertEvent(ctx, pluginapi.ScheduledEvent{Slug: "empty", Cron: "0 0 * * *", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertEvent(ctx, pluginapi.ScheduledEvent{Slug: "holed", Cron: "0 0 * * *", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Day 1→2 contiguous, then a missing day, then day 4→5. One gap.
	if _, err := m.InsertWindows(ctx, "holed", []pluginapi.EventWindow{
		{Starts: day, Ends: day.Add(24 * time.Hour)},
		{Starts: day.Add(24 * time.Hour), Ends: day.Add(48 * time.Hour)},
		{Starts: day.Add(72 * time.Hour), Ends: day.Add(96 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}

	cov, err := m.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := cov["empty"]; !ok {
		t.Error("an event with no windows is absent from coverage; the validator would never report it")
	} else if c.Windows != 0 {
		t.Errorf("empty event reports %d windows", c.Windows)
	}
	c := cov["holed"]
	if c.Windows != 3 {
		t.Errorf("windows = %d, want 3", c.Windows)
	}
	if c.Gaps != 1 {
		t.Errorf("gaps = %d, want 1 — insertion order must not change the answer", c.Gaps)
	}
	if !c.LastEnd.Equal(day.Add(96 * time.Hour)) {
		t.Errorf("last end = %s, want day+96h", c.LastEnd)
	}
}
