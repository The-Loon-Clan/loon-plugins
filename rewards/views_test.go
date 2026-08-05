package rewards

import (
	"strings"
	"testing"
	"time"
)

// Rendering the admin page is worth a test because html/template streams: a
// field referenced by the template but absent from the view model aborts
// execution partway, so the page silently loses everything after the first
// bad row rather than failing loudly. Executing it against a fully-populated
// model catches that at build time.
func TestAdminTemplateRenders(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	day := now.Truncate(24 * time.Hour)
	cur := Window{ID: 10, EventID: 1, StartsAt: day, EndsAt: day.Add(24 * time.Hour)}
	next := Window{ID: 11, EventID: 1, StartsAt: day.Add(24 * time.Hour), EndsAt: day.Add(48 * time.Hour)}
	season := 1536 * time.Hour
	expiry := 720 * time.Hour
	eid := int64(1)
	settled := now

	vm := adminVM{
		Now: now, Msg: "event daily created",
		Events: []EventStats{
			// A reset: cron, no duration, both windows present.
			{Event: Event{ID: 1, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true},
				Windows: 45, Current: &cur, Next: &next},
			// A season, disabled, with no open window — every nil branch.
			{Event: Event{ID: 2, Slug: "summer", Cron: str("0 0 1 7 *"), Duration: &season, Timezone: "UTC"},
				Windows: 2},
			// A one-off: no cron at all.
			{Event: Event{ID: 3, Slug: "launch", Timezone: "UTC", Enabled: true}},
		},
		Rewards: []Reward{
			{ID: 100, Slug: "daily-login", Kind: KindRecurring, EventID: &eid, Trigger: "login",
				Delivery: DeliveryAuto, Enabled: true, ExpiresAfter: &expiry,
				Payouts: []Payout{{Kind: PayoutPoints, Amount: 10}, {Kind: PayoutMedal, Target: "founder"}}},
			// No payouts and no event: the row that must render a warning
			// rather than an empty cell.
			{ID: 101, Slug: "hollow", Kind: KindOneOff, Delivery: DeliveryClaim},
		},
		Grants: []GrantRow{
			{Grant: Grant{ID: 1, UserID: 7, Reference: 10, State: StateCredited, CreatedAt: now, SettledAt: &settled},
				RewardSlug: "daily-login", Payout: "10 points"},
			{Grant: Grant{ID: 2, UserID: 8, Reference: 0, State: StatePending, CreatedAt: now},
				RewardSlug: "hollow", Payout: ""},
		},
	}

	// Both pages, from the same model. A truncated render is the exact failure
	// this test exists for, so each assertion set includes content from the
	// LAST table on its page.
	for _, tc := range []struct {
		tmpl string
		want []string
	}{
		{"rewards_events.html", []string{
			"Events",
			"daily", "summer", "launch",
			"until next firing", // the reset's nil duration
			"one-off",           // the event with no cron
			"Create event",
		}},
		{"rewards_admin.html", []string{
			"Rewards", "Recent grants",
			"daily-login", "hollow",
			"NONE — will not grant", // the payout-less reward
			// Both payout lines, asserted separately: html/template escapes the
			// joining "+" to &#43;, so matching the rendered string would be
			// testing the escaper rather than the page.
			"10 points", "medal founder",
			"Test on me",
		}},
	} {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, tc.tmpl, vm); err != nil {
			t.Fatalf("execute %s: %v", tc.tmpl, err)
		}
		out := sb.String()
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s is missing %q — likely truncated mid-render", tc.tmpl, want)
			}
		}
	}
}

// The findings banner is shared by both pages, because the configuration that
// breaks a reward usually lives on the other one.
func TestFindingsBannerRendersOnBothPages(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	vm := adminVM{Now: time.Now(), Findings: []Finding{
		{SeverityError, "reward daily-login", "has no payout lines", "add one"},
		{SeverityWarn, "event daily", "windows run out in 72h", "run the job"},
		{SeverityInfo, "reward x", "expiry never applies", ""},
	}}
	for _, tmpl := range []string{"rewards_events.html", "rewards_admin.html"} {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, tmpl, vm); err != nil {
			t.Fatalf("execute %s: %v", tmpl, err)
		}
		out := sb.String()
		for _, want := range []string{
			"Configuration check",
			"has no payout lines", "windows run out in 72h",
			"bg-danger", "bg-warning", "bg-secondary", // severity is visible, not just worded
			"add one", // the fix, which is what makes it actionable
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s findings banner missing %q", tmpl, want)
			}
		}
		// A finding with no fix must not render an empty arrow.
		if strings.Contains(out, "&rarr; </span>") {
			t.Errorf("%s renders an empty fix arrow", tmpl)
		}
	}
}

// The empty state has to render too: a fresh install has no events, no
// rewards and no grants, and that is the first page an operator sees.
func TestAdminTemplateRendersEmpty(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	vm := adminVM{Now: time.Now(), Err: "bad cron"}
	for tmpl, wants := range map[string][]string{
		"rewards_events.html": {"No events yet", "bad cron"},
		"rewards_admin.html":  {"No rewards yet", "Nothing granted yet", "bad cron"},
	} {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, tmpl, vm); err != nil {
			t.Fatalf("execute %s: %v", tmpl, err)
		}
		for _, want := range wants {
			if !strings.Contains(sb.String(), want) {
				t.Errorf("empty %s is missing %q", tmpl, want)
			}
		}
	}
}

// The window-authoring panel is the path a one-off event depends on entirely:
// nothing generates windows for it, so if this panel does not render, the
// event can never become earnable. Rendered here with rows and empty.
func TestAdminTemplateRendersWindowPanel(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	vm := adminVM{
		Now: now, PickedEvent: 3, PickedSlug: "launch",
		DefaultStart: "2026-03-01T00:00", DefaultEnd: "2026-03-02T00:00",
		Events: []EventStats{{Event: Event{ID: 3, Slug: "launch", Timezone: "UTC", Enabled: true}}},
		Windows: []Window{
			{ID: 90, EventID: 3, StartsAt: now, EndsAt: now.Add(48 * time.Hour)},
		},
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_events.html", vm); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"Windows for", "launch",
		`type="datetime-local"`,    // the calendar picker, not a text box
		`value="2026-03-01T00:00"`, // and it opens on today, not 1970
		"Add window", "delete",
		// $.PickedEvent inside the range — a scope slip here would silently
		// post event_id="" and collapse the panel on every delete.
		`name="event_id" value="3"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("window panel missing %q", want)
		}
	}

	// Empty: the state a freshly-created one-off event is in, and the one
	// where the operator most needs telling what to do next.
	vm.Windows = nil
	sb.Reset()
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_events.html", vm); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	if !strings.Contains(sb.String(), "cannot be earned until there is one") {
		t.Error("empty window list does not explain the consequence")
	}
}

// The event field is a SELECT built from real events, and the trigger field a
// datalist of names already in use.
//
// Both were free-text boxes. Event asked the operator to recall a numeric id
// and silently accepted a wrong one — a reward pointing at a nonexistent event
// only shows up as an ERROR after saving. Trigger is worse, because a typo
// there is silent forever: the reward saves, no surface ever asks for that
// string, and nobody is offered it, which is indistinguishable from a reward
// nobody wants.
//
// Tested because a dropdown that stops listing its options degrades to an
// empty picker — strictly worse than the text box it replaced, and invisible
// unless something asserts the options are there.
func TestRewardFormOffersEventsAndTriggers(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cron := "0 0 * * *"
	vm := adminVM{
		Now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Events: []EventStats{
			{Event: Event{ID: 7, Slug: "daily-reset", Cron: &cron, Enabled: true}},
			{Event: Event{ID: 8, Slug: "summer-2026", Enabled: false}},
		},
		Rewards:  []Reward{{Slug: "daily-login", Trigger: "login"}},
		Triggers: knownTriggers([]Reward{{Slug: "daily-login", Trigger: "login"}}),
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()

	// The event picker must be a select carrying real ids, not a text box.
	for _, want := range []string{
		`<select name="event_id"`,
		`value="7"`, `daily-reset`,
		`value="8"`, `summer-2026`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("event picker missing %q — operators are back to guessing ids", want)
		}
	}
	// The cron is shown INLINE, so "is a schedule behind this?" is answerable
	// without opening the Events page.
	if !strings.Contains(out, cron) {
		t.Error("the event's cron is not shown in the picker; the whole point is " +
			"seeing that a job drives it")
	}
	// A disabled event must be visibly disabled rather than quietly selectable.
	if !strings.Contains(out, "DISABLED") {
		t.Error("a disabled event is offered with no warning — a reward gated by " +
			"one can never be earned")
	}
	// "no event" has to be reachable: per_unit rewards want exactly that.
	if !strings.Contains(out, `<option value="">`) {
		t.Error("no way to choose NO event, which is what every per_unit reward needs")
	}
	// Trigger: a datalist (free text preserved — the host owns the vocabulary).
	if !strings.Contains(out, `list="trigger-options"`) || !strings.Contains(out, `<datalist id="trigger-options">`) {
		t.Error("trigger is not wired to its datalist")
	}
	if !strings.Contains(out, `<option value="login">`) {
		t.Error("the trigger list does not offer login, the one a stock host fires")
	}
	// And the operator is told what actually drives each kind.
	if !strings.Contains(out, "Reward Windows") {
		t.Error("the form does not mention the job that grants per_unit rewards " +
			"and materialises windows")
	}
}

// knownTriggers seeds "login" so the picker is useful when it matters most:
// configuring the FIRST trigger-driven reward, when there is nothing to derive.
func TestKnownTriggersSeedsAndDedupes(t *testing.T) {
	got := knownTriggers(nil)
	if len(got) != 1 || got[0] != "login" {
		t.Errorf("empty config gave %v, want [login] — an empty picker is useless "+
			"exactly when the first reward is being made", got)
	}
	got = knownTriggers([]Reward{
		{Trigger: "login"}, {Trigger: "profile"}, {Trigger: ""}, {Trigger: "profile"},
	})
	want := []string{"login", "profile"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v (deduped, blank skipped, sorted)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}
