package achievements

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The card's rules are about what a VIEWER may see, so they are worth pinning
// separately from whether the achievement was earned.
func profileFixture(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	m := NewMemStore()
	p := &Plugin{store: m}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return p, m
}

func renderProfile(t *testing.T, p *Plugin, vm profileVM) string {
	t.Helper()
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "profile_achievements.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// The fragment must execute against the real view model. html/template
// streams, so a field the model lacks truncates the card mid-row and returns
// 200.
func TestProfileCardRendersWithRealData(t *testing.T) {
	p, _ := profileFixture(t)
	out := renderProfile(t, p, profileVM{
		Unlocked: 2, Pending: 1, Self: true,
		Earned: []profileAchievement{
			{Achievement: Achievement{Name: "Centurion", Description: "100 posts",
				State: AchievementUnlocked, EarnedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}},
			{Achievement: Achievement{Name: "Claimed", State: AchievementPending}},
		},
		InProgress: []profileAchievement{
			{Achievement: Achievement{Name: "Veteran", Progress: 63, Threshold: 100}, PercentDone: 63},
		},
	})

	for _, want := range []string{"Centurion", "100 posts", "Veteran", "63 / 100", "2 earned"} {
		if !strings.Contains(out, want) {
			t.Errorf("card is missing %q", want)
		}
	}
	// A pending badge says so: the member holds it but the payment has not
	// been recorded, and silently showing it as earned invites "where are my
	// points".
	if !strings.Contains(out, "awaiting payout") {
		t.Error("a pending achievement did not say it is awaiting payout")
	}
	// The bar renders at the computed width, not the raw progress number.
	if !strings.Contains(out, "width:63%") {
		t.Errorf("progress bar width missing from: %s", out)
	}
}

// Progress on something unearned is shown only to the member themselves. On
// someone else's profile it is a list of what they have failed to do.
func TestInProgressIsSelfOnly(t *testing.T) {
	_, m := profileFixture(t)
	d := m.SeedAchievement(AchievementDef{
		Slug: "veteran", Name: "Veteran", Metric: "m", Threshold: 100, Enabled: true,
	})
	if _, err := m.RecordProgress(context.Background(), d.ID, 5, 63); err != nil {
		t.Fatal(err)
	}

	// The rendering decision lives in the handler, so assert on the view
	// model the handler would build rather than on HTML.
	all, err := m.Achievements(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Progress != 63 || all[0].Earned() {
		t.Fatalf("fixture wrong: %+v", all)
	}

	// Viewer is a stranger: no in-progress rows, and with nothing earned
	// there is nothing to show at all.
	strangerVM := buildProfileVM(all, false)
	if len(strangerVM.InProgress) != 0 {
		t.Error("a stranger saw what this member is partway through")
	}
	if len(strangerVM.Earned) != 0 {
		t.Error("nothing was earned, so nothing should be listed")
	}

	selfVM := buildProfileVM(all, true)
	if len(selfVM.InProgress) != 1 {
		t.Errorf("the member cannot see their own progress: %+v", selfVM)
	}
	if selfVM.InProgress[0].PercentDone != 63 {
		t.Errorf("PercentDone = %d, want 63", selfVM.InProgress[0].PercentDone)
	}
}

// A hidden achievement stays secret until earned. Listing it as locked still
// tells the member one exists, which defeats the flag.
func TestHiddenAchievementsAreSecretUntilEarned(t *testing.T) {
	locked := []Achievement{{Name: "Secret", Hidden: true, Threshold: 10, Progress: 3, State: AchievementLocked}}
	if vm := buildProfileVM(locked, true); len(vm.InProgress) != 0 || len(vm.Earned) != 0 {
		t.Error("an unearned hidden achievement was listed")
	}
	earned := []Achievement{{Name: "Secret", Hidden: true, State: AchievementUnlocked}}
	if vm := buildProfileVM(earned, true); len(vm.Earned) != 1 {
		t.Error("an EARNED hidden achievement was withheld — earning it is what reveals it")
	}
}

// Progress that overshoots the threshold must not render a bar wider than its
// track. A metric read can legitimately exceed it (615 of 500).
func TestOvershootIsClampedToOneHundred(t *testing.T) {
	vm := buildProfileVM([]Achievement{
		{Name: "Over", Threshold: 500, Progress: 615, State: AchievementLocked},
	}, true)
	if len(vm.InProgress) != 1 {
		t.Fatal("expected one in-progress row")
	}
	if vm.InProgress[0].PercentDone != 100 {
		t.Errorf("PercentDone = %d, want 100 (clamped)", vm.InProgress[0].PercentDone)
	}
}
