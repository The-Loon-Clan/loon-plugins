package donations

import (
	"testing"
	"time"
)

// The rule, with both silent failures it exists to prevent.
func TestGoalRewardDue(t *testing.T) {
	const thisMonth, lastMonth = "2026-08", "2026-07"
	for _, tc := range []struct {
		name         string
		raised, goal float64
		fired        string
		want         bool
	}{
		{"goal met this month, never fired", 300, 250, "", true},
		{"goal met, last fired a month ago", 300, 250, lastMonth, true},
		{"exactly on the goal counts", 250, 250, "", true},
		{"short of the goal", 200, 250, "", false},

		// A group with no monthly costs sums to zero, and `raised >= 0` is true
		// of every site that ever received a penny. Without the guard an empty
		// cost list declares the site funded and frees it forever.
		{"no goal configured at all", 300, 0, "", false},
		{"no goal, and nothing raised either", 0, 0, "", false},

		// The window closes after a week; the goal stays met for the rest of
		// the month. Without the period check the next donation opens a second
		// week, and the one after that a third.
		{"already fired this month", 300, 250, thisMonth, false},
		{"far past the goal, already fired", 5000, 250, thisMonth, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := goalRewardDue(tc.raised, tc.goal, tc.fired, thisMonth); got != tc.want {
				t.Errorf("goalRewardDue(raised=%.0f goal=%.0f fired=%q) = %v, want %v",
					tc.raised, tc.goal, tc.fired, got, tc.want)
			}
		})
	}
}

// The period key must roll at the month boundary and not before, because it is
// what makes a site free twice for meeting its bills in two months and once for
// meeting them twice in one.
func TestGoalRewardPeriodRollsMonthly(t *testing.T) {
	end := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)
	next := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if goalRewardPeriod(end) == goalRewardPeriod(next) {
		t.Errorf("the period did not roll over the month boundary (%s)", goalRewardPeriod(end))
	}
	// And a UTC period regardless of the caller's zone: two processes in
	// different zones must not disagree about which month it is and fire twice.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	if got, want := goalRewardPeriod(next.In(ny)), "2026-09"; got != want {
		t.Errorf("period in New York = %q, want %q", got, want)
	}
}
