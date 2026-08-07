//go:build integration

package rewards

import (
	"context"
	"errors"
	"strings"
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

// The catalogue is configuration, and this is what makes that true rather than
// a claim: the seed runs into an EMPTY table and never again.
//
// If it re-ran, a host changing its seed would silently overwrite what an
// operator edited, and rows they deliberately deleted would come back on the
// next deploy — at which point it is not configuration, it is a default with
// extra steps.
func TestSeedSourcesRunsOnceAndThenLeavesConfigurationAlone(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	p := &Plugin{admin: st}
	ctx := context.Background()

	seed := SourceCatalog{
		{Key: "posts.created", Label: "Posts created", Group: "Forum",
			Fires: true, Counts: true, Unit: "post", Units: "posts"},
		{Key: "uploads.created", Label: "Uploads", Group: "Uploads",
			Fires: true, Counts: true, Unit: "upload", Units: "uploads"},
	}

	n, err := p.seedSources(ctx, seed)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 2 {
		t.Fatalf("seeded %d, want 2", n)
	}

	// An operator edits the vocabulary: renames one, turns the other off.
	if _, err := db.Exec(`UPDATE rewards.reward_sources SET label = 'Forum posts' WHERE key = 'posts.created'`); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	if _, err := db.Exec(`UPDATE rewards.reward_sources SET enabled = false WHERE key = 'uploads.created'`); err != nil {
		t.Fatalf("operator disable: %v", err)
	}

	// A later boot, with a seed the host has since grown.
	grown := append(seed, SourceDef{Key: "comments.created", Label: "Comments",
		Group: "Comments", Fires: true, Counts: true, Unit: "comment", Units: "comments"})
	if n, err = p.seedSources(ctx, grown); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n != 0 {
		t.Errorf("the seed ran again and wrote %d row(s) — an operator's catalogue is not theirs "+
			"if a deploy can rewrite it", n)
	}

	cat, err := p.Catalogue(ctx)
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	if len(cat) != 1 {
		t.Fatalf("catalogue has %d enabled source(s), want 1 — the disabled row came back", len(cat))
	}
	if cat[0].Label != "Forum posts" {
		t.Errorf("label = %q, want the operator's %q", cat[0].Label, "Forum posts")
	}
}

// The CHECKs are the schema's copy of SourceDef.Valid, so a row written by any
// other route cannot offer a dropdown entry that does nothing.
func TestSchemaRefusesAnUnusableSource(t *testing.T) {
	db := testDB(t)

	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, fires, counts)
		VALUES ('inert', 'Inert', false, false)`); err == nil {
		t.Error("a source that neither fires nor counts was accepted — it would sit in a " +
			"dropdown doing nothing")
	}
	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, counts, unit)
		VALUES ('unnamed', 'Unnamed', true, '')`); err == nil {
		t.Error("a counter with no unit was accepted — every achievement on it would " +
			"suggest a blank name")
	}
	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, fires)
		VALUES ('ok', 'Fires only', true)`); err != nil {
		t.Errorf("a fires-only source was refused: %v", err)
	}
}

// The two progress paths must not be confused, and the difference is only
// visible against a real database.
//
// An event ADDS ("one more happened"); a metric SETS ("the total is 613").
// Adding the absolute total on every tick would multiply a member's progress
// by the number of ticks — they would cross a 100-post threshold in an
// afternoon with three posts, and the badge would look earned.
func TestMetricPathSetsAndEventPathAdds(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	achID, _ := seedAchievement(t, db, "hundred", 100)
	const userID = int64(7)

	// The metric path, run twice with the same total. Progress must land on
	// the total, not twice it.
	for i := 0; i < 2; i++ {
		if _, err := st.RecordProgress(ctx, achID, userID, 40); err != nil {
			t.Fatalf("RecordProgress: %v", err)
		}
	}
	if got := progressOf(t, db, achID, userID); got != 40 {
		t.Fatalf("after two identical metric reads progress = %d, want 40 — "+
			"the tick is accumulating an absolute total", got)
	}

	// The event path, twice. Each says "one more", so they add.
	for i := 0; i < 2; i++ {
		if _, err := st.IncrementProgress(ctx, achID, userID, 1); err != nil {
			t.Fatalf("IncrementProgress: %v", err)
		}
	}
	if got := progressOf(t, db, achID, userID); got != 42 {
		t.Errorf("after two increments progress = %d, want 42", got)
	}

	// And the metric wins when it next runs: it is the reconciling source, so
	// a drifted count is corrected rather than compounded.
	if _, err := st.RecordProgress(ctx, achID, userID, 40); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	if got := progressOf(t, db, achID, userID); got != 40 {
		t.Errorf("progress = %d after reconciliation, want the counter's 40 — "+
			"the absolute total is what makes a dropped event self-heal", got)
	}
}

func progressOf(t *testing.T, db *sqlx.DB, achID, userID int64) int64 {
	t.Helper()
	var n int64
	if err := db.Get(&n, `SELECT progress FROM rewards.user_achievements
	                       WHERE achievement_id = $1 AND user_id = $2`, achID, userID); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	return n
}

// A reward with no payout lines pays nothing while looking healthy. Engine.grant
// refuses one; this path does not go through Engine.grant, so it has to refuse
// separately — and the cost of not doing so is worse here, because a completion
// is irreversible. completed_at is stamped once, so the member would hold an
// achievement that never paid and could never be re-earned after somebody added
// the missing line.
//
// This is not hypothetical: the first real achievement on the site was created
// with its payout row missing, lost while splitting a repair file.
func TestAchievementRefusesAPayoutlessReward(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	var rewardID int64
	if _, err := db.Exec("SET search_path = rewards"); err != nil {
		t.Fatalf("scope: %v", err)
	}
	err := db.Get(&rewardID, `
		INSERT INTO rewards (slug, name, kind, delivery, enabled)
		VALUES ('empty', 'Empty', 'one_off', 'auto', true) RETURNING id`)
	if _, e := db.Exec("SET search_path = public"); e != nil {
		t.Fatalf("reset: %v", e)
	}
	if err != nil {
		t.Fatalf("seed reward: %v", err)
	}

	var achID int64
	if err := db.Get(&achID, `
		INSERT INTO rewards.achievements (slug, name, reward_id, metric, threshold)
		VALUES ('empty-ach', 'Empty', $1, 'x', 1) RETURNING id`, rewardID); err != nil {
		t.Fatalf("seed achievement: %v", err)
	}
	if _, err := st.RecordProgress(ctx, achID, 7, 1); err != nil {
		t.Fatalf("progress: %v", err)
	}

	p := &Plugin{store: st}
	d := AchievementDef{ID: achID, Slug: "empty-ach", RewardID: rewardID}
	if err := p.completeAchievement(ctx, st, d, 7); err == nil {
		t.Error("a payout-less reward was accepted; the member now holds an achievement " +
			"that paid nothing and can never be re-earned")
	}

	var completed int
	if err := db.Get(&completed, `SELECT count(*) FROM rewards.user_achievements
	                               WHERE achievement_id = $1 AND completed_at IS NOT NULL`, achID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if completed != 0 {
		t.Error("the completion was stamped despite the refusal")
	}
}

// Creating an achievement awards it to everyone already past the threshold and
// notified every one of them — 23 people within seconds of an INSERT on
// 2026-08-07. The award is right; the announcement is not, for somebody who
// earned it before the thing existed.
//
// The first scoring pass is therefore silent, and only the first.
func TestFirstScoringPassIsSilentAndOnlyTheFirst(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	achID, rewardID := seedAchievement(t, db, "silent", 1)
	p := &Plugin{store: st, admin: st}

	// Someone who already qualifies: the backfill cohort.
	if _, err := st.RecordProgress(ctx, achID, 7, 5); err != nil {
		t.Fatalf("progress: %v", err)
	}
	defs, err := st.AchievementDefsByMetric(ctx, "uploads")
	if err != nil {
		t.Fatalf("defs: %v", err)
	}
	if len(defs) != 1 || defs[0].BackfilledAt != nil {
		t.Fatalf("setup: a fresh achievement should have no backfill mark: %+v", defs)
	}
	if err := p.completeAchievement(ctx, st, defs[0], 7); err != nil {
		t.Fatalf("backfill completion: %v", err)
	}
	if !grantSilent(t, db, rewardID, 7) {
		t.Error("a backfilled completion was not marked silent — the member would be " +
			"told about something they did months ago")
	}

	// The pass ends; the mark goes on.
	if err := st.MarkBackfilled(ctx, achID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// The next member to earn it is a live earner and must hear about it.
	defs, _ = st.AchievementDefsByMetric(ctx, "uploads")
	if defs[0].BackfilledAt == nil {
		t.Fatal("the backfill mark did not stick")
	}
	if _, err := st.RecordProgress(ctx, achID, 9, 5); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := p.completeAchievement(ctx, st, defs[0], 9); err != nil {
		t.Fatalf("live completion: %v", err)
	}
	if grantSilent(t, db, rewardID, 9) {
		t.Error("a member who earned it AFTER the backfill was silenced — they did " +
			"the thing just now and heard nothing")
	}

	// And the mark is stamped once: a second call must not reset it and
	// re-silence a later cohort.
	before := defs[0].BackfilledAt
	if err := st.MarkBackfilled(ctx, achID); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	again, _ := st.AchievementDefsByMetric(ctx, "uploads")
	if !again[0].BackfilledAt.Equal(*before) {
		t.Error("MarkBackfilled moved an existing mark; a later cohort would be re-silenced")
	}
}

func grantSilent(t *testing.T, db *sqlx.DB, rewardID, userID int64) bool {
	t.Helper()
	var silent bool
	if err := db.Get(&silent, `SELECT silent FROM rewards.reward_grants
	                            WHERE reward_id = $1 AND user_id = $2`, rewardID, userID); err != nil {
		t.Fatalf("read grant: %v", err)
	}
	return silent
}

// Every mistake made while creating this site's first two achievements by
// hand, now returned as a refusal an operator can read. All four were live in
// production at some point on 2026-08-07 and none raised an error anywhere.
func TestCreateAchievementRefusesTheMistakesWeActuallyMade(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	ctx := context.Background()

	mk := func(slug, kind string, payouts bool, enabled bool) int64 {
		t.Helper()
		var id int64
		if _, err := db.Exec("SET search_path = rewards"); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = db.Exec("SET search_path = public") }()
		if err := db.Get(&id, `
			INSERT INTO rewards (slug, name, kind, delivery, enabled)
			VALUES ($1, $1, $2, 'auto', $3) RETURNING id`, slug, kind, enabled); err != nil {
			t.Fatal(err)
		}
		if payouts {
			if _, err := db.Exec(`INSERT INTO reward_payouts (reward_id, kind, amount)
			                      VALUES ($1, 'points', 10)`, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	good := mk("good", "one_off", true, true)
	noPayout := mk("nopay", "one_off", false, true)
	perUnit := mk("perunit", "per_unit", true, true)
	disabled := mk("off", "one_off", true, false)

	if err := st.UpsertSource(ctx, SourceDef{Key: "posts", Label: "Posts",
		Counts: true, Unit: "post", Units: "posts"}); err != nil {
		t.Fatalf("seed counter source: %v", err)
	}
	if err := st.UpsertSource(ctx, SourceDef{Key: "fires-only", Label: "Fires", Fires: true}); err != nil {
		t.Fatalf("seed event source: %v", err)
	}

	base := NewAchievement{Slug: "x", Name: "X", RewardID: good, Metric: "posts", Threshold: 5}
	for _, tc := range []struct {
		name string
		mut  func(*NewAchievement)
		want string
	}{
		{"no payout lines", func(a *NewAchievement) { a.RewardID = noPayout }, "no payout lines"},
		{"per_unit reward", func(a *NewAchievement) { a.RewardID = perUnit }, "one_off"},
		{"disabled reward", func(a *NewAchievement) { a.RewardID = disabled }, "disabled"},
		{"undeclared metric", func(a *NewAchievement) { a.Metric = "nope" }, "not in the source catalogue"},
		{"metric that only fires", func(a *NewAchievement) { a.Metric = "fires-only" }, "does not count"},
		{"no threshold", func(a *NewAchievement) { a.Threshold = 0 }, "threshold must be positive"},
		{"no name", func(a *NewAchievement) { a.Name = "" }, "name is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			tc.mut(&a)
			_, err := st.CreateAchievement(ctx, a)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
		})
	}

	// The valid one lands, and lands DISABLED — enabling is what triggers the
	// backfill, so it cannot be a side effect of creating.
	id, err := st.CreateAchievement(ctx, base)
	if err != nil {
		t.Fatalf("a valid achievement was refused: %v", err)
	}
	var enabled bool
	if err := db.Get(&enabled, `SELECT enabled FROM rewards.achievements WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("created enabled — one tool call could then backfill a badge to the whole membership")
	}
	if err := st.SetAchievementEnabled(ctx, id, true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// And the slug is unique, with a message that says so rather than a
	// constraint name.
	if _, err := st.CreateAchievement(ctx, base); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate slug: %v", err)
	}
}
