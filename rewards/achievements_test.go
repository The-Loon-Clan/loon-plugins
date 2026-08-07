package rewards

import (
	"database/sql"
	"testing"
	"time"
)

// The page state is derived from two facts that can disagree, which is why it
// is worth its own test: the COMPLETION is the achievement's own record, and
// the GRANT's state says whether the payment landed. A member who earned
// something and has not claimed it has earned it — reporting that as locked
// would tell them they had not.
func TestAchievementRowMapsCompletionAndGrantToPageState(t *testing.T) {
	earned := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		row   achievementRow
		want  AchievementState
		dated bool
	}{
		{
			name: "no progress row at all",
			row:  achievementRow{Slug: "first-upload", Threshold: 1},
			want: AchievementLocked,
		},
		{
			name: "progress but not there yet",
			row: achievementRow{Slug: "hundred", Threshold: 100,
				Progress: sql.NullInt64{Int64: 63, Valid: true}},
			want: AchievementLocked,
		},
		{
			name: "completed, payment settled",
			row: achievementRow{Slug: "hundred", Threshold: 100,
				CompletedAt: sql.NullTime{Time: earned, Valid: true},
				GrantState:  sql.NullString{String: string(StateCredited), Valid: true}},
			want: AchievementUnlocked, dated: true,
		},
		{
			name: "completed, awaiting claim",
			row: achievementRow{Slug: "hundred", Threshold: 100,
				CompletedAt: sql.NullTime{Time: earned, Valid: true},
				GrantState:  sql.NullString{String: string(StatePending), Valid: true}},
			want: AchievementPending, dated: true,
		},
		{
			// A grant that lapsed unclaimed does not un-earn the achievement.
			// The member did the thing; what expired is the payment, and
			// hiding the badge would be a second punishment for the same lapse.
			name: "completed, grant expired unclaimed",
			row: achievementRow{Slug: "hundred", Threshold: 100,
				CompletedAt: sql.NullTime{Time: earned, Valid: true},
				GrantState:  sql.NullString{String: string(StateExpired), Valid: true}},
			want: AchievementUnlocked, dated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.row.achievement()
			if got.State != tc.want {
				t.Errorf("state = %q, want %q", got.State, tc.want)
			}
			if tc.dated && got.EarnedAt.IsZero() {
				t.Error("an earned achievement has no EarnedAt — the page would print the epoch")
			}
			if !tc.dated && !got.EarnedAt.IsZero() {
				t.Error("an unearned achievement carries an EarnedAt")
			}
			if got.Earned() != (tc.want != AchievementLocked) {
				t.Errorf("Earned() = %v, disagrees with state %q", got.Earned(), got.State)
			}
		})
	}
}

// Progress has to survive the read, because "63 / 100" is most of what an
// achievements page is for and is the thing the placeholder could not say.
func TestAchievementRowCarriesProgressAndCriterion(t *testing.T) {
	got := achievementRow{
		Slug: "hundred-uploads", Name: "Centurion", Description: "upload 100",
		Metric: "uploads", Threshold: 100,
		Progress: sql.NullInt64{Int64: 63, Valid: true},
		Times:    sql.NullInt32{Int32: 0, Valid: true},
	}.achievement()

	if got.Progress != 63 || got.Threshold != 100 {
		t.Errorf("progress/threshold = %d/%d, want 63/100", got.Progress, got.Threshold)
	}
	if got.Metric != "uploads" {
		t.Errorf("metric = %q, want uploads", got.Metric)
	}
	if got.Name != "Centurion" || got.Description != "upload 100" {
		t.Errorf("name/description lost: %q / %q", got.Name, got.Description)
	}
}

// The statistics panel is three numbers that must add up to the list beside
// it. Anything not unlocked or pending counts as locked — including a state
// this build does not recognise, which must not vanish from the total.
func TestAchievementCounts(t *testing.T) {
	as := []Achievement{
		{State: AchievementUnlocked},
		{State: AchievementUnlocked},
		{State: AchievementPending},
		{State: AchievementLocked},
		{State: "something-else"},
	}
	unlocked, pending, locked := AchievementCounts(as)
	if unlocked != 2 || pending != 1 || locked != 2 {
		t.Errorf("counts = (%d, %d, %d), want (2, 1, 2)", unlocked, pending, locked)
	}
	if unlocked+pending+locked != len(as) {
		t.Errorf("counts sum to %d, want %d — a state fell out of the total",
			unlocked+pending+locked, len(as))
	}
}

func TestAchievementCountsOnEmpty(t *testing.T) {
	if u, p, l := AchievementCounts(nil); u != 0 || p != 0 || l != 0 {
		t.Errorf("counts = (%d, %d, %d), want all zero", u, p, l)
	}
}
