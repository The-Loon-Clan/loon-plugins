//go:build integration

package rewards

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The transaction is the point of this table pair, so it is what gets tested
// against a real database. A completion with no grant is an achievement that
// paid nothing; a grant with no completion pays again on the next evaluation
// because nothing records that it already fired. Neither may happen alone, and
// a mock cannot prove that — it would reproduce whatever its author believed
// the SQL did, which is the belief under test.

func seedAchievement(t *testing.T, db *sqlx.DB, slug string, threshold int64) (achID, rewardID int64) {
	t.Helper()
	if _, err := db.Exec("SET search_path = rewards"); err != nil {
		t.Fatalf("scope: %v", err)
	}
	defer func() { _, _ = db.Exec("SET search_path = public") }()

	if err := db.Get(&rewardID, `
		INSERT INTO rewards (slug, name, kind, delivery, enabled)
		VALUES ($1, 'Badge', 'one_off', 'auto', true) RETURNING id`, slug+"-reward"); err != nil {
		t.Fatalf("seed reward: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO reward_payouts (reward_id, kind, amount) VALUES ($1, 'points', 50)`,
		rewardID); err != nil {
		t.Fatalf("seed payout: %v", err)
	}
	if err := db.Get(&achID, `
		INSERT INTO achievements (slug, name, reward_id, metric, threshold)
		VALUES ($1, 'Centurion', $2, 'uploads', $3) RETURNING id`,
		slug, rewardID, threshold); err != nil {
		t.Fatalf("seed achievement: %v", err)
	}
	return achID, rewardID
}

func TestCompleteAchievementIsAtomic(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	achID, rewardID := seedAchievement(t, db, "centurion", 100)
	const userID = int64(7)

	// Below the threshold: progress is recorded, nothing completes.
	reached, err := st.RecordProgress(ctx, achID, userID, 63)
	if err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	if reached {
		t.Fatal("63 of 100 reported the threshold as reached")
	}

	// At the threshold.
	if reached, err = st.RecordProgress(ctx, achID, userID, 100); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	if !reached {
		t.Fatal("100 of 100 did not report the threshold as reached")
	}

	g := Grant{RewardID: rewardID, UserID: userID, Reference: 0, State: StatePending}
	got, err := st.CompleteAchievement(ctx, achID, g, []Payout{{Kind: PayoutPoints, Amount: 50}})
	if err != nil {
		t.Fatalf("CompleteAchievement: %v", err)
	}
	if got.ID == 0 {
		t.Fatal("no grant id came back")
	}

	// Both halves, and the link between them.
	as, err := st.Achievements(ctx, userID)
	if err != nil {
		t.Fatalf("Achievements: %v", err)
	}
	if len(as) != 1 {
		t.Fatalf("%d achievements, want 1", len(as))
	}
	if as[0].State != AchievementPending {
		t.Errorf("state = %q, want pending (the grant is unsettled)", as[0].State)
	}
	if as[0].Progress != 100 || as[0].Times != 1 {
		t.Errorf("progress/times = %d/%d, want 100/1", as[0].Progress, as[0].Times)
	}
	if as[0].EarnedAt.IsZero() {
		t.Error("completed_at was not stamped")
	}
}

// The second completion must not pay again. This is the whole reason the
// completion is written first and conditionally.
func TestCompleteAchievementRefusesASecondTime(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	achID, rewardID := seedAchievement(t, db, "once", 1)
	const userID = int64(7)
	if _, err := st.RecordProgress(ctx, achID, userID, 1); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	g := Grant{RewardID: rewardID, UserID: userID, State: StatePending}
	lines := []Payout{{Kind: PayoutPoints, Amount: 50}}

	if _, err := st.CompleteAchievement(ctx, achID, g, lines); err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if _, err := st.CompleteAchievement(ctx, achID, g, lines); !errors.Is(err, ErrAlreadyGranted) {
		t.Fatalf("second completion: %v, want ErrAlreadyGranted", err)
	}

	var grants int
	if err := db.Get(&grants,
		`SELECT count(*) FROM rewards.reward_grants WHERE reward_id = $1 AND user_id = $2`,
		rewardID, userID); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grants != 1 {
		t.Errorf("%d grants, want 1 — the achievement paid twice", grants)
	}
}

// Two evaluations racing must produce exactly one payment. The conditional
// completion arbitrates first; the engine's UNIQUE constraint is underneath as
// the backstop that does not depend on this being right.
func TestCompleteAchievementUnderRace(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	achID, rewardID := seedAchievement(t, db, "racy", 1)
	const userID = int64(7)
	if _, err := st.RecordProgress(ctx, achID, userID, 1); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			g := Grant{RewardID: rewardID, UserID: userID, State: StatePending}
			_, errs[i] = st.CompleteAchievement(ctx, achID, g,
				[]Payout{{Kind: PayoutPoints, Amount: 50}})
		}(i)
	}
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrAlreadyGranted):
		default:
			t.Errorf("racer %d: unexpected error %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d racers completed the achievement, want exactly 1", won)
	}

	var grants, completions int
	if err := db.Get(&grants,
		`SELECT count(*) FROM rewards.reward_grants WHERE reward_id = $1`, rewardID); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if err := db.Get(&completions,
		`SELECT count(*) FROM rewards.user_achievements
		  WHERE achievement_id = $1 AND completed_at IS NOT NULL`, achID); err != nil {
		t.Fatalf("count completions: %v", err)
	}
	if grants != 1 || completions != 1 {
		t.Errorf("grants=%d completions=%d, want 1 and 1", grants, completions)
	}
}

// The CHECK is the schema's own copy of the invariant: it catches any future
// writer that stamps one half without the other, including one that never goes
// through CompleteAchievement.
func TestSchemaRefusesAHalfCompletion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	_ = ctx

	achID, _ := seedAchievement(t, db, "halves", 1)
	if _, err := db.Exec(`
		INSERT INTO rewards.user_achievements (achievement_id, user_id, progress, completed_at)
		VALUES ($1, 9, 1, now())`, achID); err == nil {
		t.Error("a completion with no grant was accepted — the achievement paid nothing")
	}
	if _, err := db.Exec(`
		INSERT INTO rewards.user_achievements (achievement_id, user_id, progress, grant_id)
		VALUES ($1, 9, 1, NULL)`, achID); err != nil {
		t.Errorf("a plain progress row was refused: %v", err)
	}
}
