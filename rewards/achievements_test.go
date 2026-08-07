package rewards

import (
	"database/sql"
	"testing"
	"time"
)

var earned = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// The grant states and the page states are different vocabularies, and the
// mapping between them is where an achievements page goes quietly wrong: a
// member sees a badge they do not hold, or does not see one they do.
//
// The case that matters most is EXPIRED. The grant row still carries a date,
// so a naive mapping shows "locked — earned 1 Aug", which reads as a bug
// rather than as history.
func TestAchievementRowMapsGrantStateToPageState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		hasDate    bool
		wantState  AchievementState
		wantEarned bool
	}{
		{"credited is unlocked", string(StateCredited), true, AchievementUnlocked, true},
		{"pending is pending", string(StatePending), true, AchievementPending, true},
		{"expired is locked, and loses its date", string(StateExpired), true, AchievementLocked, false},
		{"no grant at all is locked", "", false, AchievementLocked, false},
		// A state this plugin does not know must not read as unlocked. Failing
		// closed is the only safe default for "do they have this".
		{"an unknown state is locked", "some_future_state", true, AchievementLocked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := achievementRow{
				Slug: "first-grab", Name: "First Grab", Target: "first-grab",
				State:    nullStr(tc.state),
				EarnedAt: sql.NullTime{Time: earned, Valid: tc.hasDate},
			}
			got := row.achievement()
			if got.State != tc.wantState {
				t.Errorf("state %q -> %q, want %q", tc.state, got.State, tc.wantState)
			}
			if gotEarned := !got.EarnedAt.IsZero(); gotEarned != tc.wantEarned {
				t.Errorf("state %q -> EarnedAt set = %v, want %v", tc.state, gotEarned, tc.wantEarned)
			}
		})
	}
}

// A reward with no name falls back to its slug in SQL; the mapping must not
// undo that, and Target is a separate field on purpose.
func TestAchievementRowKeepsSlugTargetAndNameDistinct(t *testing.T) {
	row := achievementRow{Slug: "summer-2026-finisher", Name: "Summer Finisher", Target: "marathon"}
	got := row.achievement()
	if got.Slug != "summer-2026-finisher" || got.Name != "Summer Finisher" || got.Target != "marathon" {
		t.Errorf("got %+v, want the three fields carried through unchanged", got)
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
