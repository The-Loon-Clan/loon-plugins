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
	expiry := 720 * time.Hour
	settled := now

	vm := adminVM{
		Now: now, Msg: "reward daily-login created",
		Rewards: []Reward{
			{ID: 100, Slug: "daily-login", Kind: KindRecurring, EventSlug: "daily", Trigger: "login",
				Delivery: DeliveryAuto, Enabled: true, ExpiresAfter: &expiry,
				Payouts: []Payout{{Kind: PayoutPoints, Amount: 10}, {Kind: PayoutMedal, Target: "founder"}}},
			// No payouts and no event: the row that must render a warning
			// rather than an empty cell.
			{ID: 101, Slug: "hollow", Kind: KindOneOff, Delivery: DeliveryClaim},
		},
		Grants: []GrantRow{
			// A recurring grant now carries the occurrence KEY as its reference,
			// which is the string an operator reads on this page while working
			// out which run of an event paid.
			{Grant: Grant{ID: 1, UserID: 7, Reference: "daily@2026-03-01T00:00:00Z",
				State: StateCredited, CreatedAt: now, SettledAt: &settled},
				RewardSlug: "daily-login", Payout: "10 points"},
			{Grant: Grant{ID: 2, UserID: 8, Reference: "", State: StatePending, CreatedAt: now},
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
		{"rewards_admin.html", []string{
			"Rewards", "Recent grants",
			"daily-login", "hollow",
			"None &mdash; will not grant", // the payout-less reward
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
		// A warn-severity finding so all three severity styles render. Window
		// runout belongs to the events plugin now, so this uses a reward-side one.
		{SeverityWarn, "reward stale", "no surface asks for it", "run the job"},
		{SeverityInfo, "reward x", "expiry never applies", ""},
	}}
	// One page now, so a loop of one. Left as a loop because the banner is a
	// shared define and the next page to include it belongs here rather than in
	// a test of its own.
	for _, tmpl := range []string{"rewards_admin.html"} {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, tmpl, vm); err != nil {
			t.Fatalf("execute %s: %v", tmpl, err)
		}
		out := sb.String()
		for _, want := range []string{
			"Configuration check",
			"has no payout lines", "no surface asks for it",
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
		"rewards_admin.html": {"No rewards yet", "Nothing granted yet", "bad cron"},
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
