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

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", vm); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()

	// Every section must survive to the end. A truncated render is the exact
	// failure this test exists for, so assert on content from the LAST table.
	for _, want := range []string{
		"Events", "Rewards", "Recent grants",
		"daily", "summer", "launch",
		"until next firing", // the reset's nil duration
		"one-off",           // the event with no cron
		"daily-login", "hollow",
		"NONE — will not grant", // the payout-less reward
		// Both payout lines, asserted separately: html/template escapes the
		// joining "+" to &#43;, so matching the rendered string would be
		// testing the escaper rather than the page.
		"10 points", "medal founder",
		"Test on me",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page is missing %q — likely truncated mid-render", want)
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
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", adminVM{Now: time.Now(), Err: "bad cron"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"No events yet", "No rewards yet", "Nothing granted yet", "bad cron"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty page is missing %q", want)
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
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", vm); err != nil {
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
	if err := p.tmpl.ExecuteTemplate(&sb, "rewards_admin.html", vm); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	if !strings.Contains(sb.String(), "cannot be earned until there is one") {
		t.Error("empty window list does not explain the consequence")
	}
}
