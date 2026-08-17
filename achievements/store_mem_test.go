package achievements

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The MemStore's own semantics, pinned so the double stays as strict as the
// SQL it stands for.

// The completed_at latch is the race arbiter now — the whole design leans on
// "complete twice returns ErrAlreadyCompleted, and times stays 1".
func TestCompleteAchievementLatchesOnce(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "a", Metric: "m", Threshold: 1, Enabled: true})
	ctx := context.Background()

	if _, err := m.IncrementProgress(ctx, d.ID, 5, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAchievement(ctx, d.ID, 5, false); err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if err := m.CompleteAchievement(ctx, d.ID, 5, false); !errors.Is(err, ErrAlreadyCompleted) {
		t.Fatalf("second completion: %v, want ErrAlreadyCompleted", err)
	}
	if _, times, done := m.ProgressOf(d.ID, 5); !done || times != 1 {
		t.Errorf("times=%d done=%v, want 1/true — the latch did not hold", times, done)
	}
}

// A trigger achievement's first contact with a member may be the completion
// itself: no progress row exists, and the SQL's upsert inserts one. A mock
// that required the row would refuse a path production accepts.
func TestCompleteAchievementWithNoProgressRow(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "t", Trigger: "auth.first_login", Enabled: true})
	if err := m.CompleteAchievement(context.Background(), d.ID, 5, false); err != nil {
		t.Fatalf("completion with no prior progress: %v", err)
	}
	if _, times, done := m.ProgressOf(d.ID, 5); !done || times != 1 {
		t.Errorf("times=%d done=%v, want 1/true", times, done)
	}
}

// paid=true stamps paid_at with the completion; paid=false leaves it for the
// payment path.
func TestCompleteAchievementPaidFlag(t *testing.T) {
	m := NewMemStore()
	badge := m.SeedAchievement(AchievementDef{Slug: "badge", Trigger: "x", Enabled: true})
	paidLater := m.SeedAchievement(AchievementDef{Slug: "paid", Trigger: "x", RewardSlug: "r", Enabled: true})
	ctx := context.Background()

	if err := m.CompleteAchievement(ctx, badge.ID, 5, true); err != nil {
		t.Fatal(err)
	}
	if !m.Paid(badge.ID, 5) {
		t.Error("paid=true did not stamp paid_at")
	}
	if err := m.CompleteAchievement(ctx, paidLater.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	if m.Paid(paidLater.ID, 5) {
		t.Error("paid=false stamped paid_at — the payment path owns that stamp")
	}
}

// MarkPaid stamps once, only on completed rows, and never errors on a no-op —
// the same contract as the SQL UPDATE's zero-rows outcome.
func TestMarkPaidIsIdempotentAndCompletionOnly(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "a", Metric: "m", Threshold: 10, RewardSlug: "r", Enabled: true})
	ctx := context.Background()

	// Not completed: a no-op, not a stamp — payment is a property of a
	// completion, never of bare progress.
	if _, err := m.IncrementProgress(ctx, d.ID, 5, 3); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkPaid(ctx, d.ID, 5); err != nil {
		t.Fatal(err)
	}
	if m.Paid(d.ID, 5) {
		t.Error("MarkPaid stamped an uncompleted row")
	}

	if err := m.CompleteAchievement(ctx, d.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkPaid(ctx, d.ID, 5); err != nil {
		t.Fatal(err)
	}
	if !m.Paid(d.ID, 5) {
		t.Error("MarkPaid did not stamp a completed row")
	}
	// A second stamp is a no-op, not an error.
	if err := m.MarkPaid(ctx, d.ID, 5); err != nil {
		t.Errorf("second MarkPaid: %v", err)
	}
}

// The sweep's read: completed + unpaid + reward-bearing, and nothing else.
// A pure badge with paid_at NULL must never appear — nothing is owed on it.
func TestUnpaidCompletionsFiltersBadgesAndPaidRows(t *testing.T) {
	m := NewMemStore()
	owed := m.SeedAchievement(AchievementDef{Slug: "owed", Trigger: "x", RewardSlug: "r", Enabled: true})
	settled := m.SeedAchievement(AchievementDef{Slug: "settled", Trigger: "x", RewardSlug: "r", Enabled: true})
	badge := m.SeedAchievement(AchievementDef{Slug: "badge", Trigger: "x", Enabled: true})
	ctx := context.Background()

	if err := m.CompleteAchievement(ctx, owed.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAchievement(ctx, settled.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkPaid(ctx, settled.ID, 5); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAchievement(ctx, badge.ID, 5, true); err != nil {
		t.Fatal(err)
	}

	rows, err := m.UnpaidCompletions(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Slug != "owed" || rows[0].RewardSlug != "r" || rows[0].UserID != 5 {
		t.Errorf("UnpaidCompletions = %+v, want exactly the owed row", rows)
	}
}

// RecordProgress SETS — downwards included, matching the SQL, whose
// integration test asserts a drifted count is corrected rather than
// compounded. IncrementProgress ADDS. Confusing the two multiplies progress
// by the tick count or flatlines it.
func TestRecordSetsAndIncrementAdds(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "a", Metric: "m", Threshold: 100, Enabled: true})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := m.RecordProgress(ctx, d.ID, 7, 40); err != nil {
			t.Fatal(err)
		}
	}
	if v, _, _ := m.ProgressOf(d.ID, 7); v != 40 {
		t.Fatalf("after two identical metric reads progress = %d, want 40", v)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.IncrementProgress(ctx, d.ID, 7, 1); err != nil {
			t.Fatal(err)
		}
	}
	if v, _, _ := m.ProgressOf(d.ID, 7); v != 42 {
		t.Fatalf("after two increments progress = %d, want 42", v)
	}
	// The metric wins when it next runs: it is the reconciling source.
	if _, err := m.RecordProgress(ctx, d.ID, 7, 40); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := m.ProgressOf(d.ID, 7); v != 40 {
		t.Errorf("progress = %d after reconciliation, want the counter's 40", v)
	}
}

// A completed achievement records further progress ("100 / 100" keeps
// climbing) but never reports a fresh crossing — otherwise every later event
// re-attempts a completion that can only fail.
func TestReachedIsNotReportedAfterCompletion(t *testing.T) {
	m := NewMemStore()
	d := m.SeedAchievement(AchievementDef{Slug: "a", Metric: "m", Threshold: 2, Enabled: true})
	ctx := context.Background()

	reached, err := m.IncrementProgress(ctx, d.ID, 5, 2)
	if err != nil || !reached {
		t.Fatalf("reached=%v err=%v, want true/nil", reached, err)
	}
	if err := m.CompleteAchievement(ctx, d.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	reached, err = m.IncrementProgress(ctx, d.ID, 5, 1)
	if err != nil || reached {
		t.Errorf("reached=%v err=%v after completion, want false/nil", reached, err)
	}
	if v, _, _ := m.ProgressOf(d.ID, 5); v != 3 {
		t.Errorf("progress = %d, want 3 — progress keeps recording after completion", v)
	}
}

// A stale id is refused, standing in for the FK.
func TestUnknownAchievementIsRefused(t *testing.T) {
	m := NewMemStore()
	if _, err := m.IncrementProgress(context.Background(), 99, 5, 1); !errors.Is(err, errAchievementUnknown) {
		t.Errorf("progress on unknown achievement: %v, want errAchievementUnknown", err)
	}
	if err := m.CompleteAchievement(context.Background(), 99, 5, false); !errors.Is(err, errAchievementUnknown) {
		t.Errorf("completing unknown achievement: %v, want errAchievementUnknown", err)
	}
}

// The read applies the PG query's visibility rules: hidden withheld until
// earned, disabled kept only for members who completed one. The old rewards
// double skipped both, which made its read looser than the page production
// serves.
func TestAchievementsReadVisibilityRules(t *testing.T) {
	m := NewMemStore()
	live := m.SeedAchievement(AchievementDef{Slug: "live", Metric: "m", Threshold: 1, Enabled: true})
	hidden := m.SeedAchievement(AchievementDef{Slug: "secret", Metric: "m", Threshold: 1, Enabled: true, Hidden: true})
	retired := m.SeedAchievement(AchievementDef{Slug: "retired", Metric: "m", Threshold: 1, Enabled: false})
	ctx := context.Background()
	_ = live

	// Member 5 completed the hidden and the retired one; member 6 nothing.
	for _, id := range []int64{hidden.ID, retired.ID} {
		if err := m.CompleteAchievement(ctx, id, 5, true); err != nil {
			t.Fatal(err)
		}
	}

	slugsOf := func(userID int64) map[string]bool {
		t.Helper()
		as, err := m.Achievements(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, a := range as {
			out[a.Slug] = true
		}
		return out
	}

	got5 := slugsOf(5)
	if !got5["live"] || !got5["secret"] || !got5["retired"] {
		t.Errorf("earner's shelf = %v, want all three — earning reveals hidden and keeps retired", got5)
	}
	got6 := slugsOf(6)
	if !got6["live"] || got6["secret"] || got6["retired"] {
		t.Errorf("stranger's list = %v, want only the live one", got6)
	}
}

// The frozen-clock hook: a test controlling Now gets deterministic stamps.
func TestMemStoreUsesInjectedClock(t *testing.T) {
	m := NewMemStore()
	m.Now = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	d := m.SeedAchievement(AchievementDef{Slug: "a", Trigger: "x", Enabled: true})
	if err := m.CompleteAchievement(context.Background(), d.ID, 5, false); err != nil {
		t.Fatal(err)
	}
	as, err := m.Achievements(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || !as[0].EarnedAt.Equal(m.Now) {
		t.Errorf("EarnedAt = %v, want the injected clock %v", as[0].EarnedAt, m.Now)
	}
}
