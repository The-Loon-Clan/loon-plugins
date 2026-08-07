//go:build integration

// Integration tests for PGStore. Gated behind the `integration` tag; set
// REWARDS_TEST_DSN to a throwaway database.
//
// These exist because the two things the whole model rests on cannot be tested
// against a mock: the UNIQUE (reward, user, reference) constraint, and the
// half-open window comparison. A mock reproduces whatever its author believed
// the SQL did, which is exactly the belief under test.

package rewards

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("REWARDS_TEST_DSN")
	if dsn == "" {
		t.Skip("REWARDS_TEST_DSN not set; skipping. Point it at a throwaway database.")
	}
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	// A REAL schema, scoped exactly the way production is.
	//
	// The first cut of this applied the migration with no schema, so every
	// table landed in public and every unqualified query found it -- which meant
	// the suite passed against a store that had unwrapped SchemaDB and lost its
	// search_path entirely. Production then failed on the very first request
	// with `relation "events" does not exist`. A harness that does not reproduce
	// the scoping cannot test the scoping, so this one leaves the session on
	// public and makes the store do its own.
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS rewards CASCADE; CREATE SCHEMA rewards`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// EVERY migration, in order — not just the first. Naming one file meant a
	// second migration's tables were missing from the harness while present in
	// production, so a test could only ever exercise the schema as it was on
	// day one.
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found — the harness would test an empty schema")
	}
	sort.Strings(files)
	// Applied INSIDE the schema; the session goes straight back to public.
	if _, err := db.Exec("SET search_path = rewards"); err != nil {
		t.Fatalf("scope for migration: %v", err)
	}
	for _, f := range files {
		schema, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if _, err := db.Exec("SET search_path = public"); err != nil {
		t.Fatalf("reset search_path: %v", err)
	}
	// The guard that keeps this honest. If the tables are reachable from
	// public, the session scoping is doing the work and an unscoped store
	// would pass every test below -- which is precisely how the bug shipped.
	var leaked *string
	if err := db.Get(&leaked, "SELECT to_regclass('public.reward_grants')::text"); err != nil {
		t.Fatalf("check schema isolation: %v", err)
	}
	if leaked != nil {
		t.Fatalf("rewards tables are visible in public (%s) -- the harness is not scoped, so it cannot test scoping", *leaked)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testStore wraps the pool the way Provision does, so every test exercises the
// same scoping path production uses.
func testStore(t *testing.T, db *sqlx.DB) *PGStore {
	t.Helper()
	return NewPGStore(core.NewStorage(db).SchemaDB("rewards"))
}

// seedDaily creates one recurring points reward and returns it with the
// occurrence keys of two consecutive days.
//
// It no longer inserts an event or its windows: rewards has no such tables. The
// keys are built the way the events plugin builds them, because what these tests
// exercise is the GRANT side -- the pay-once constraint over (reward, user,
// reference) -- and the reference is now a name supplied from outside.
func seedDaily(t *testing.T, db *sqlx.DB, day time.Time) (int64, string, string) {
	t.Helper()
	var rewardID int64
	if err := db.QueryRow(`INSERT INTO rewards.rewards (slug, kind, scheduled_event_slug, trigger, delivery)
	                       VALUES ('daily-login','recurring','daily','login','claim') RETURNING id`).Scan(&rewardID); err != nil {
		t.Fatalf("insert reward: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rewards.reward_payouts (reward_id, kind, amount) VALUES ($1,'points',10)`, rewardID); err != nil {
		t.Fatalf("insert payout: %v", err)
	}
	k1 := pluginapi.EventWindow{Slug: "daily", Starts: day, Ends: day.Add(24 * time.Hour)}.Key()
	k2 := pluginapi.EventWindow{Slug: "daily", Starts: day.Add(24 * time.Hour), Ends: day.Add(48 * time.Hour)}.Key()
	return rewardID, k1, k2
}

// The constraint, exercised through genuinely concurrent transactions. This is
// the claim the entire design rests on and the one a mock cannot make.
func TestPGConcurrentGrantsHitTheConstraint(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rewardID, w1, _ := seedDaily(t, db, day)
	payouts := []Payout{{Kind: PayoutPoints, Amount: 10}}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, dup int
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateGrant(context.Background(),
				Grant{RewardID: rewardID, UserID: 7, Reference: w1, State: StatePending}, payouts)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrAlreadyGranted):
				dup++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if ok != 1 || dup != 24 {
		t.Errorf("succeeded=%d duplicates=%d, want 1/24", ok, dup)
	}
	var grants, lines int
	_ = db.QueryRow(`SELECT count(*) FROM rewards.reward_grants`).Scan(&grants)
	_ = db.QueryRow(`SELECT count(*) FROM rewards.reward_grant_payouts`).Scan(&lines)
	if grants != 1 || lines != 1 {
		t.Errorf("rows: grants=%d frozen lines=%d, want 1/1", grants, lines)
	}
}

// A grant's payout lines are FROZEN. Retuning the reward afterwards must not
// change what the outstanding grant pays -- verified through the real tables,
// since freezing is a copy the store performs.
func TestPGFrozenPayoutIsACopy(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rewardID, w1, _ := seedDaily(t, db, day)
	ctx := context.Background()

	r, err := store.RewardByID(ctx, rewardID)
	if err != nil || r == nil {
		t.Fatalf("load reward: %v", err)
	}
	g, err := store.CreateGrant(ctx, Grant{RewardID: rewardID, UserID: 7, Reference: w1, State: StatePending}, r.Payouts)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}

	if _, err := db.Exec(`UPDATE rewards.reward_payouts SET amount = 1 WHERE reward_id = $1`, rewardID); err != nil {
		t.Fatalf("retune: %v", err)
	}
	got, err := store.GrantByID(ctx, g.ID)
	if err != nil || got == nil {
		t.Fatalf("reload grant: %v", err)
	}
	if len(got.Payouts) != 1 || got.Payouts[0].Amount != 10 {
		t.Errorf("frozen amount = %v, want 10 — the grant read back through the reward", got.Payouts)
	}
}

// GrantByID returns only UNSETTLED lines, so a resumed settle does not replay
// what already landed. Tested here because the filter lives in SQL.
func TestPGGrantByIDSkipsSettledLines(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rewardID, w1, _ := seedDaily(t, db, day)
	ctx := context.Background()

	g, err := store.CreateGrant(ctx, Grant{RewardID: rewardID, UserID: 7, Reference: w1, State: StatePending},
		[]Payout{{Kind: PayoutPoints, Amount: 10}, {Kind: PayoutMedal, Target: "founder"}})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if err := store.MarkPayoutSettled(ctx, g.Payouts[0].ID, time.Now()); err != nil {
		t.Fatalf("mark settled: %v", err)
	}
	got, err := store.GrantByID(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Payouts) != 1 || got.Payouts[0].Kind != PayoutMedal {
		t.Errorf("unsettled lines = %v, want just the medal", got.Payouts)
	}
}

// Expiry is bounded per sweep, and only touches pending grants that are past
// their expiry.
func TestPGExpireGrantsIsBoundedAndSelective(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rewardID, _, _ := seedDaily(t, db, day)
	ctx := context.Background()

	past := day.Add(-time.Hour)
	future := day.Add(48 * time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO rewards.reward_grants (reward_id, user_id, reference, state, expires_at)
		                      VALUES ($1, $2, $3, 'pending', $4)`, rewardID, int64(i), int64(i), past); err != nil {
			t.Fatalf("seed expiring grant: %v", err)
		}
	}
	// One with no expiry and one not yet due: neither may be touched.
	_, _ = db.Exec(`INSERT INTO rewards.reward_grants (reward_id, user_id, reference, state) VALUES ($1, 100, 100, 'pending')`, rewardID)
	_, _ = db.Exec(`INSERT INTO rewards.reward_grants (reward_id, user_id, reference, state, expires_at) VALUES ($1, 101, 101, 'pending', $2)`, rewardID, future)

	n, err := store.ExpireGrants(ctx, day, 3)
	if err != nil || n != 3 {
		t.Fatalf("bounded sweep: n=%d err=%v, want 3", n, err)
	}
	n, err = store.ExpireGrants(ctx, day, 100)
	if err != nil || n != 2 {
		t.Fatalf("second sweep: n=%d err=%v, want the remaining 2", n, err)
	}
	var stillPending int
	_ = db.QueryRow(`SELECT count(*) FROM rewards.reward_grants WHERE state='pending'`).Scan(&stillPending)
	if stillPending != 2 {
		t.Errorf("pending after sweeps = %d, want 2 (no-expiry and not-yet-due)", stillPending)
	}
}

// INTERVAL columns come back as seconds and become time.Duration; getting this
// wrong silently gives every reward an expiry of zero.
func TestPGIntervalRoundTrip(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO rewards.rewards (slug, kind, trigger, expires_after)
	                      VALUES ('fleeting','one_off','login', INTERVAL '36 hours')`); err != nil {
		t.Fatalf("insert reward: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rewards.reward_payouts (reward_id, kind, amount)
	                      SELECT id, 'points', 5 FROM rewards.rewards WHERE slug='fleeting'`); err != nil {
		t.Fatalf("insert payout: %v", err)
	}
	r, err := store.RewardBySlug(ctx, "fleeting")
	if err != nil || r == nil {
		t.Fatalf("load: %v", err)
	}
	if r.ExpiresAfter == nil || *r.ExpiresAfter != 36*time.Hour {
		t.Errorf("expires_after = %v, want 36h", r.ExpiresAfter)
	}
	if len(r.Payouts) != 1 {
		t.Errorf("payouts = %d, want 1", len(r.Payouts))
	}
}

// PreviousMark is GREATEST(highest grant reference, baseline) in SQL, and
// SetBaseline must never lower one. Both are arithmetic that decides how far
// back a per_unit reward pays, so both are tested against the real thing.
func TestPGPreviousMarkAndBaseline(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()
	var rewardID int64
	if err := db.QueryRow(`INSERT INTO rewards.rewards (slug, kind, trigger, delivery)
	                       VALUES ('grabs','per_unit','upload','auto') RETURNING id`).Scan(&rewardID); err != nil {
		t.Fatalf("insert reward: %v", err)
	}

	// No grants, no baseline: counting starts at zero, which is why a reward
	// created without seeding pays the member's entire history.
	if mark, err := store.PreviousMark(ctx, rewardID, 7); err != nil || mark != 0 {
		t.Fatalf("unseeded mark = %d (%v), want 0", mark, err)
	}

	if err := store.SetBaseline(ctx, rewardID, 7, 10000); err != nil {
		t.Fatalf("set baseline: %v", err)
	}
	if mark, _ := store.PreviousMark(ctx, rewardID, 7); mark != 10000 {
		t.Errorf("after baseline mark = %d, want 10000", mark)
	}

	// A grant past the baseline wins. The mark lives in high_water now, not in
	// the reference -- that split is exactly what stops PreviousMark comparing
	// "9" against "10" as text.
	if _, err := store.CreateGrant(ctx,
		Grant{RewardID: rewardID, UserID: 7, Reference: perUnitRef(10500), HighWater: 10500, State: StateCredited},
		[]Payout{{Kind: PayoutPoints, Amount: 1000}}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if mark, _ := store.PreviousMark(ctx, rewardID, 7); mark != 10500 {
		t.Errorf("after a grant past the baseline mark = %d, want 10500", mark)
	}

	// Re-seeding LOWER must not move the line back past that grant — doing so
	// would re-pay every unit in between.
	if err := store.SetBaseline(ctx, rewardID, 7, 500); err != nil {
		t.Fatalf("re-seed lower: %v", err)
	}
	if mark, _ := store.PreviousMark(ctx, rewardID, 7); mark != 10500 {
		t.Errorf("a lowered baseline moved the mark to %d, re-opening a paid range", mark)
	}
	var stored int64
	_ = db.QueryRow(`SELECT value FROM rewards.reward_baselines WHERE reward_id=$1 AND user_id=7`, rewardID).Scan(&stored)
	if stored != 10000 {
		t.Errorf("stored baseline = %d, want 10000 — SetBaseline must never lower", stored)
	}

	// Per member, not global.
	if mark, _ := store.PreviousMark(ctx, rewardID, 8); mark != 0 {
		t.Errorf("another member's mark = %d, want 0", mark)
	}
}
