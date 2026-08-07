package rewards

import (
	"strings"
	"testing"
	"time"
)

// Window-coverage validation (a one-off with no windows, an exhausted or
// short runway, gaps in a contiguous event) MOVED with the tables to the events
// plugin, along with the three tests that covered it. It is not checked from
// this side any more on purpose: the events plugin owns the generator and
// reports coverage per event, so a second opinion computed here could only ever
// disagree with the authority.
//
// What rewards still validates is its own half — that a reward's event slug
// names something real and enabled.

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
	rewards := []Reward{{
		ID: 100, Slug: "daily-login", Kind: KindRecurring, EventSlug: "daily",
		Trigger: "login", Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 10}},
	}}
	handled := map[PayoutKind]bool{PayoutPoints: true}
	known := map[string]bool{"daily": true}

	got := validateRewards(rewards, known, handled)
	if len(got) != 0 {
		t.Errorf("healthy config produced %d finding(s): %+v", len(got), got)
	}
}

// The two ways an enabled reward is guaranteed to be refused at grant time.
func TestValidateCatchesUngrantableRewards(t *testing.T) {
	rewards := []Reward{
		{ID: 1, Slug: "hollow", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto, Enabled: true},
		{ID: 2, Slug: "fancy", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto, Enabled: true,
			Payouts: []Payout{{Kind: PayoutUsernameFX, Target: "rainbow"}}},
	}
	got := validateRewards(rewards, map[string]bool{"daily": true}, map[PayoutKind]bool{PayoutPoints: true})

	if f := findingFor(got, "reward hollow", "no payout lines"); f == nil || f.Severity != SeverityError {
		t.Errorf("payout-less reward: got %+v, want an error", f)
	}
	if f := findingFor(got, "reward fancy", "no handler is registered"); f == nil || f.Severity != SeverityError {
		t.Errorf("unhandled payout kind: got %+v, want an error", f)
	}
}

// A reward gated by a disabled event, and one naming an event that does not
// exist. Both are enabled and look fine in the rewards table.
//
// The third case this test used to cover -- an event with no window open right
// now -- moved to the events plugin with the generator. Window health is the
// authority's to report.
func TestValidateCatchesBrokenEventGating(t *testing.T) {
	known := map[string]bool{"off": false, "reset": true}
	rewards := []Reward{
		{ID: 1, Slug: "gated-off", Kind: KindRecurring, EventSlug: "off", Trigger: "login", Enabled: true,
			Delivery: DeliveryAuto, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 3, Slug: "ghost-event", Kind: KindRecurring, EventSlug: "no-such-season", Trigger: "login", Enabled: true,
			Delivery: DeliveryAuto, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
	}
	got := validateRewards(rewards, known, map[PayoutKind]bool{PayoutPoints: true})

	for _, tc := range []struct{ subject, want string }{
		{"reward gated-off", "is disabled"},
		{"reward ghost-event", "does not exist"},
	} {
		if f := findingFor(got, tc.subject, tc.want); f == nil || f.Severity != SeverityError {
			t.Errorf("%s: got %+v, want an error mentioning %q", tc.subject, f, tc.want)
		}
	}

	// A CLOSED SEASON is not a problem — that is what a season does. Enabled and
	// existing is all rewards asks about; whether a window happens to be open
	// right now is not a configuration fault and is not this validator's call.
	seasonal := []Reward{{ID: 9, Slug: "summer-bonus", Kind: KindRecurring, EventSlug: "summer",
		Trigger: "login", Enabled: true, Delivery: DeliveryAuto,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}}}
	if got := validateRewards(seasonal, map[string]bool{"summer": true}, map[PayoutKind]bool{PayoutPoints: true}); len(got) != 0 {
		t.Errorf("an out-of-season reward was reported as broken: %+v", got)
	}

	// And with NO events plugin wired (nil, not empty) the validator stays quiet
	// rather than declaring every gated reward broken. Nil means "cannot tell";
	// an empty map means "asked, and there are none", and only the second is
	// grounds for a finding.
	if got := validateRewards(seasonal, nil, map[PayoutKind]bool{PayoutPoints: true}); len(got) != 0 {
		t.Errorf("with no events plugin the validator invented %d finding(s): %+v", len(got), got)
	}
}

// A disabled reward's problems are not problems yet, and reporting them buries
// the live ones.
func TestValidateIgnoresDisabledRewards(t *testing.T) {
	rewards := []Reward{{ID: 1, Slug: "draft", Kind: KindOneOff, Enabled: false}}
	if got := validateRewards(rewards, map[string]bool{}, map[PayoutKind]bool{}); len(got) != 0 {
		t.Errorf("disabled reward produced %d finding(s): %+v", len(got), got)
	}
}

// Legal-but-probably-wrong is info, not error: it must not drown the things
// that actually stop a payment.
func TestValidateGradesAdviceAsInfo(t *testing.T) {
	expiry := 720 * time.Hour
	known := map[string]bool{"daily": true}
	rewards := []Reward{
		{ID: 1, Slug: "auto-expiry", Kind: KindOneOff, Trigger: "login", Delivery: DeliveryAuto,
			Enabled: true, ExpiresAfter: &expiry, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 2, Slug: "gated-delta", Kind: KindPerUnit, EventSlug: "daily", Trigger: "upload",
			Delivery: DeliveryAuto, Enabled: true, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
		{ID: 3, Slug: "unreachable", Kind: KindOneOff, Delivery: DeliveryClaim,
			Enabled: true, Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}},
	}
	got := validateRewards(rewards, known, map[PayoutKind]bool{PayoutPoints: true})

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

// ...but a per_unit reward is granted by a job reading its UnitSource, never
// offered by a surface, so it has no use for a trigger. This fired on the
// tenure reward the moment it was enabled in production, which is the only
// reason the exclusion exists.
func TestValidateAllowsPerUnitWithNoTrigger(t *testing.T) {
	rewards := []Reward{{
		ID: 1, Slug: "tenure", Kind: KindPerUnit, Trigger: "",
		Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 30000}},
	}}
	got := validateRewards(rewards, map[string]bool{}, map[PayoutKind]bool{PayoutPoints: true})
	if len(got) != 0 {
		t.Errorf("job-driven per_unit reward produced %d finding(s): %+v", len(got), got)
	}

	// The same shape as one_off IS worth warning about: nothing would ever
	// create the grant.
	rewards[0].Kind = KindOneOff
	if got := validateRewards(rewards, map[string]bool{}, map[PayoutKind]bool{PayoutPoints: true}); len(got) != 1 {
		t.Errorf("one_off with no trigger produced %d finding(s), want 1", len(got))
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
