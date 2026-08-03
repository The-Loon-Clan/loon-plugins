package rewards

import (
	"strings"
	"testing"
	"time"
)

func findingFor(fs []Finding, subject, contains string) *Finding {
	for i := range fs {
		if fs[i].Subject == subject && strings.Contains(fs[i].Problem, contains) {
			return &fs[i]
		}
	}
	return nil
}

// A healthy configuration must produce NOTHING. A validator that always finds
// something is one an operator learns to ignore, and then it stops working the
// day it matters.
func TestValidateSilentOnHealthyConfig(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	day := now.Truncate(24 * time.Hour)
	cur := Window{ID: 10, EventID: 1, StartsAt: day, EndsAt: day.Add(24 * time.Hour)}
	eid := int64(1)

	events := []EventStats{{
		Event:   Event{ID: 1, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true},
		Windows: 45, Current: &cur,
	}}
	cov := map[int64]Coverage{1: {EventID: 1, Windows: 45, LastEnd: now.Add(45 * 24 * time.Hour)}}
	rewards := []Reward{{
		ID: 100, Slug: "daily-login", Kind: KindRecurring, EventID: &eid,
		Trigger: "login", Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 10}},
	}}
	handled := map[PayoutKind]bool{PayoutPoints: true}

	got := append(validateEvents(events, cov, now), validateRewards(rewards, mapEvents(events), handled)...)
	if len(got) != 0 {
		t.Errorf("healthy config produced %d finding(s): %+v", len(got), got)
	}
}

func mapEvents(events []EventStats) map[int64]EventStats {
	m := make(map[int64]EventStats, len(events))
	for _, e := range events {
		m[e.ID] = e
	}
	return m
}

// The headline case: a one-off event created and never given windows. Valid
// rows, valid reward, pays nobody, and nothing else in the system says so.
func TestValidateCatchesOneOffWithNoWindows(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	events := []EventStats{{Event: Event{ID: 3, Slug: "launch", Timezone: "UTC", Enabled: true}}}

	got := validateEvents(events, map[int64]Coverage{}, now)
	f := findingFor(got, "event launch", "no windows")
	if f == nil {
		t.Fatalf("no finding for a one-off event with no windows: %+v", got)
	}
	if f.Severity != SeverityError {
		t.Errorf("severity = %s, want error — it can never be earned", f.Severity)
	}
	if !strings.Contains(f.Fix, "by hand") {
		t.Errorf("fix does not say to author windows: %q", f.Fix)
	}
}

// A cron event whose windows have run out reads as healthy from every other
// angle: the event is enabled, the reward is enabled, the counts are non-zero.
func TestValidateCatchesExhaustedAndShortRunway(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	events := []EventStats{
		{Event: Event{ID: 1, Slug: "expired", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true}, Windows: 10},
		{Event: Event{ID: 2, Slug: "shortly", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true}, Windows: 10},
	}
	cov := map[int64]Coverage{
		1: {EventID: 1, Windows: 10, LastEnd: now.Add(-time.Hour)},
		2: {EventID: 2, Windows: 10, LastEnd: now.Add(3 * 24 * time.Hour)},
	}
	got := validateEvents(events, cov, now)

	if f := findingFor(got, "event expired", "in the past"); f == nil || f.Severity != SeverityError {
		t.Errorf("exhausted event: got %+v, want an error finding", f)
	}
	if f := findingFor(got, "event shortly", "run out in"); f == nil || f.Severity != SeverityWarn {
		t.Errorf("short runway: got %+v, want a warn finding", f)
	}
}

// Gaps are the point of a season and a bug in a reset, so the same shape of
// data must produce a finding in one case and silence in the other.
func TestValidateGapsOnlyMatterForResets(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	season := 1536 * time.Hour
	events := []EventStats{
		{Event: Event{ID: 1, Slug: "reset", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true}},
		{Event: Event{ID: 2, Slug: "season", Cron: str("0 0 1 7 *"), Duration: &season, Timezone: "UTC", Enabled: true}},
	}
	cov := map[int64]Coverage{
		1: {EventID: 1, Windows: 10, Gaps: 3, LastEnd: now.Add(45 * 24 * time.Hour)},
		2: {EventID: 2, Windows: 10, Gaps: 9, LastEnd: now.Add(45 * 24 * time.Hour)},
	}
	got := validateEvents(events, cov, now)

	if findingFor(got, "event reset", "gap") == nil {
		t.Error("a contiguous reset with gaps produced no finding")
	}
	if f := findingFor(got, "event season", "gap"); f != nil {
		t.Errorf("a season's gaps were reported as a problem: %+v", f)
	}
}

// The two ways an enabled reward is guaranteed to be refused at grant time.
func TestValidateCatchesUngrantableRewards(t *testing.T) {
	events := []EventStats{{Event: Event{ID: 1, Slug: "daily", Enabled: true}}}
	rewards := []Reward{
		{ID: 1, Slug: "hollow", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto, Enabled: true},
		{ID: 2, Slug: "fancy", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto, Enabled: true,
			Payouts: []Payout{{Kind: PayoutUsernameFX, Target: "rainbow"}}},
	}
	got := validateRewards(rewards, mapEvents(events), map[PayoutKind]bool{PayoutPoints: true})

	if f := findingFor(got, "reward hollow", "no payout lines"); f == nil || f.Severity != SeverityError {
		t.Errorf("payout-less reward: got %+v, want an error", f)
	}
	if f := findingFor(got, "reward fancy", "no handler is registered"); f == nil || f.Severity != SeverityError {
		t.Errorf("unhandled payout kind: got %+v, want an error", f)
	}
}

// A reward gated by a disabled event, and one gated by an event whose reset
// window has vanished. Both are enabled and look fine in the rewards table.
func TestValidateCatchesBrokenEventGating(t *testing.T) {
	e1, e2, e3 := int64(1), int64(2), int64(99)
	season := 1536 * time.Hour
	events := []EventStats{
		{Event: Event{ID: 1, Slug: "off", Enabled: false}},
		{Event: Event{ID: 2, Slug: "reset", Timezone: "UTC", Enabled: true}}, // no Current
	}
	rewards := []Reward{
		{ID: 1, Slug: "gated-off", Kind: KindRecurring, EventID: &e1, Trigger: "login", Enabled: true,
			Delivery: DeliveryAuto, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 2, Slug: "no-window", Kind: KindRecurring, EventID: &e2, Trigger: "login", Enabled: true,
			Delivery: DeliveryAuto, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 3, Slug: "ghost-event", Kind: KindRecurring, EventID: &e3, Trigger: "login", Enabled: true,
			Delivery: DeliveryAuto, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
	}
	got := validateRewards(rewards, mapEvents(events), map[PayoutKind]bool{PayoutPoints: true})

	for _, tc := range []struct{ subject, want string }{
		{"reward gated-off", "is disabled"},
		{"reward no-window", "no window open right now"},
		{"reward ghost-event", "does not exist"},
	} {
		if f := findingFor(got, tc.subject, tc.want); f == nil || f.Severity != SeverityError {
			t.Errorf("%s: got %+v, want an error mentioning %q", tc.subject, f, tc.want)
		}
	}

	// A CLOSED SEASON is not a problem — that is what a season does.
	seasonEvents := []EventStats{{Event: Event{ID: 5, Slug: "summer", Duration: &season, Enabled: true}}}
	eid := int64(5)
	seasonal := []Reward{{ID: 9, Slug: "summer-bonus", Kind: KindRecurring, EventID: &eid,
		Trigger: "login", Enabled: true, Delivery: DeliveryAuto,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}}}
	if got := validateRewards(seasonal, mapEvents(seasonEvents), map[PayoutKind]bool{PayoutPoints: true}); len(got) != 0 {
		t.Errorf("an out-of-season reward was reported as broken: %+v", got)
	}
}

// A disabled reward's problems are not problems yet, and reporting them buries
// the live ones.
func TestValidateIgnoresDisabledRewards(t *testing.T) {
	rewards := []Reward{{ID: 1, Slug: "draft", Kind: KindOneOff, Enabled: false}}
	if got := validateRewards(rewards, map[int64]EventStats{}, map[PayoutKind]bool{}); len(got) != 0 {
		t.Errorf("disabled reward produced %d finding(s): %+v", len(got), got)
	}
}

// Legal-but-probably-wrong is info, not error: it must not drown the things
// that actually stop a payment.
func TestValidateGradesAdviceAsInfo(t *testing.T) {
	expiry := 720 * time.Hour
	eid := int64(1)
	events := []EventStats{{Event: Event{ID: 1, Slug: "daily", Enabled: true, Duration: &expiry}}}
	rewards := []Reward{
		{ID: 1, Slug: "auto-expiry", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto,
			Enabled: true, ExpiresAfter: &expiry, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 2, Slug: "gated-delta", Kind: KindPerUnit, EventID: &eid, Trigger: "upload",
			Delivery: DeliveryAuto, Enabled: true, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 3, Slug: "unreachable", Kind: KindOneOff, Delivery: DeliveryClaim,
			Enabled: true, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
	}
	got := validateRewards(rewards, mapEvents(events), map[PayoutKind]bool{PayoutPoints: true})

	if f := findingFor(got, "reward auto-expiry", "expiry never applies"); f == nil || f.Severity != SeverityInfo {
		t.Errorf("auto+expiry: got %+v, want info", f)
	}
	if f := findingFor(got, "reward gated-delta", "per_unit AND gated"); f == nil || f.Severity != SeverityInfo {
		t.Errorf("per_unit with an event: got %+v, want info", f)
	}
	// No trigger and claim delivery means nothing will ever offer it — that is
	// a real problem, so it is a warning rather than advice.
	if f := findingFor(got, "reward unreachable", "no surface asks for it"); f == nil || f.Severity != SeverityWarn {
		t.Errorf("claim with no trigger: got %+v, want warn", f)
	}
}

// Errors must sort above warnings above info: an operator scanning this needs
// what is broken now at the top.
func TestValidateSortsBySeverity(t *testing.T) {
	fs := []Finding{
		{Severity: SeverityInfo, Subject: "c"},
		{Severity: SeverityWarn, Subject: "b"},
		{Severity: SeverityError, Subject: "a"},
	}
	// Same comparator Validate uses.
	for i := 0; i < len(fs); i++ {
		for j := i + 1; j < len(fs); j++ {
			if severityRank(fs[j].Severity) < severityRank(fs[i].Severity) {
				fs[i], fs[j] = fs[j], fs[i]
			}
		}
	}
	if fs[0].Subject != "a" || fs[1].Subject != "b" || fs[2].Subject != "c" {
		t.Errorf("order = %s/%s/%s, want a/b/c", fs[0].Subject, fs[1].Subject, fs[2].Subject)
	}
}
