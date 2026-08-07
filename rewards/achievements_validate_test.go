package rewards

import (
	"strings"
	"testing"
)

// The guard that keeps repeatability from having two sources of truth.
//
// An achievement declares no repeatability of its own — rewards.kind does, and
// the engine enforces it through the reference it computes. These checks are
// what stop an achievement being pointed at a reward whose kind makes no sense
// for a criterion that latches.
func TestValidateAchievements(t *testing.T) {
	rewards := map[int64]Reward{
		1: {ID: 1, Slug: "badge", Kind: KindOneOff, Enabled: true},
		2: {ID: 2, Slug: "per-grab", Kind: KindPerUnit, Enabled: true},
		3: {ID: 3, Slug: "seasonal", Kind: KindRecurring, Enabled: true},
		4: {ID: 4, Slug: "retired", Kind: KindOneOff, Enabled: false},
	}
	metrics := map[string]bool{"uploads": true}

	def := func(slug string, rewardID int64, metric string) AchievementDef {
		return AchievementDef{Slug: slug, RewardID: rewardID, Metric: metric,
			Threshold: 10, Enabled: true}
	}

	for _, tc := range []struct {
		name     string
		def      AchievementDef
		wantSev  Severity
		wantText string
	}{
		{
			name: "a one_off reward with a registered metric is clean",
			def:  def("ok", 1, "uploads"),
		},
		{
			// The headline case. per_unit pays on every later unit while a
			// criterion latches the moment it is met, so the pairing is not a
			// repeatable achievement — it is an achievement that keeps paying.
			name: "per_unit is refused", def: def("bad-kind", 2, "uploads"),
			wantSev: SeverityError, wantText: "per_unit",
		},
		{
			name: "recurring is refused while achievements are one_off",
			def:  def("seasonal", 3, "uploads"),
			// Coherent one day (a seasonal achievement), not enabled today.
			wantSev: SeverityError, wantText: "one_off",
		},
		{
			name:    "a disabled reward can be earned but not paid",
			def:     def("orphan-pay", 4, "uploads"),
			wantSev: SeverityError, wantText: "disabled",
		},
		{
			name: "a reward that does not exist", def: def("dangling", 99, "uploads"),
			wantSev: SeverityError, wantText: "does not exist",
		},
		{
			name:    "no metric means nothing can ever score it",
			def:     def("no-metric", 1, ""),
			wantSev: SeverityError, wantText: "no metric",
		},
		{
			// WARN not ERROR: a host whose source has not booted yet is a
			// deployment ordering problem, and refusing would make the admin
			// page unusable during one.
			name:    "an unregistered metric warns rather than fails",
			def:     def("future-metric", 1, "comments"),
			wantSev: SeverityWarn, wantText: "no source is registered",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validateAchievements([]AchievementDef{tc.def}, rewards, metrics)
			if tc.wantText == "" {
				if len(got) != 0 {
					t.Fatalf("clean config produced %d finding(s): %+v", len(got), got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("no finding; wanted %s mentioning %q", tc.wantSev, tc.wantText)
			}
			f := got[0]
			if f.Severity != tc.wantSev {
				t.Errorf("severity = %s, want %s", f.Severity, tc.wantSev)
			}
			if !strings.Contains(f.Problem, tc.wantText) {
				t.Errorf("problem = %q, want it to mention %q", f.Problem, tc.wantText)
			}
			if f.Fix == "" {
				t.Error("a finding with no Fix is a complaint rather than a report")
			}
			if !strings.Contains(f.Subject, tc.def.Slug) {
				t.Errorf("subject %q does not name the achievement", f.Subject)
			}
		})
	}
}

// A disabled achievement is not a problem to report — an operator turned it
// off, and listing it would train them to ignore the panel.
func TestValidateAchievementsIgnoresDisabled(t *testing.T) {
	d := AchievementDef{Slug: "off", RewardID: 99, Metric: "", Enabled: false}
	if got := validateAchievements([]AchievementDef{d}, nil, nil); len(got) != 0 {
		t.Errorf("a disabled achievement produced %d finding(s): %+v", len(got), got)
	}
}
