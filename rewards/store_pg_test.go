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
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
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
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE events, event_windows, rewards, reward_payouts,
	                      reward_grants, reward_grant_payouts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedDaily creates a contiguous daily event with two windows and one
// recurring points reward, returning the reward and the two window ids.
func seedDaily(t *testing.T, db *sqlx.DB, day time.Time) (int64, int64, int64) {
	t.Helper()
	var eventID int64
	if err := db.QueryRow(`INSERT INTO events (slug, cron, timezone) VALUES ('daily','0 0 * * *','UTC') RETURNING id`).Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	var w1, w2 int64
	if err := db.QueryRow(`INSERT INTO event_windows (event_id, starts_at, ends_at) VALUES ($1,$2,$3) RETURNING id`,
		eventID, day, day.Add(24*time.Hour)).Scan(&w1); err != nil {
		t.Fatalf("insert window 1: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO event_windows (event_id, starts_at, ends_at) VALUES ($1,$2,$3) RETURNING id`,
		eventID, day.Add(24*time.Hour), day.Add(48*time.Hour)).Scan(&w2); err != nil {
		t.Fatalf("insert window 2: %v", err)
	}
	var rewardID int64
	if err := db.QueryRow(`INSERT INTO rewards (slug, kind, event_id, trigger, delivery)
	                       VALUES ('daily-login','recurring',$1,'login','claim') RETURNING id`, eventID).Scan(&rewardID); err != nil {
		t.Fatalf("insert reward: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reward_payouts (reward_id, kind, amount) VALUES ($1,'points',10)`, rewardID); err != nil {
		t.Fatalf("insert payout: %v", err)
	}
	return rewardID, w1, w2
}

// The constraint, exercised through genuinely concurrent transactions. This is
// the claim the entire design rests on and the one a mock cannot make.
func TestPGConcurrentGrantsHitTheConstraint(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
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
	_ = db.QueryRow(`SELECT count(*) FROM reward_grants`).Scan(&grants)
	_ = db.QueryRow(`SELECT count(*) FROM reward_grant_payouts`).Scan(&lines)
	if grants != 1 || lines != 1 {
		t.Errorf("rows: grants=%d frozen lines=%d, want 1/1", grants, lines)
	}
}

// The half-open comparison, in SQL rather than in Go. At exactly ends_at the
// member must be in the NEXT window; matching both would be a free extra claim
// at every boundary.
func TestPGOpenWindowIsHalfOpen(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	_, w1, w2 := seedDaily(t, db, day)
	ctx := context.Background()

	var eventID int64
	_ = db.QueryRow(`SELECT id FROM events WHERE slug='daily'`).Scan(&eventID)

	for _, tc := range []struct {
		name string
		at   time.Time
		want int64
	}{
		{"just after open", day.Add(time.Second), w1},
		{"one ns before the boundary", day.Add(24*time.Hour - time.Microsecond), w1},
		{"exactly at the boundary", day.Add(24 * time.Hour), w2},
		{"inside the second", day.Add(30 * time.Hour), w2},
		{"after everything", day.Add(72 * time.Hour), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.OpenWindowsFor(ctx, []int64{eventID}, tc.at)
			if err != nil {
				t.Fatalf("open windows: %v", err)
			}
			if tc.want == 0 {
				if len(got) != 0 {
					t.Errorf("windows = %d, want none", len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("windows = %d, want exactly 1", len(got))
			}
			if got[eventID].ID != tc.want {
				t.Errorf("window = %d, want %d", got[eventID].ID, tc.want)
			}
		})
	}
}

// A grant's payout lines are FROZEN. Retuning the reward afterwards must not
// change what the outstanding grant pays -- verified through the real tables,
// since freezing is a copy the store performs.
func TestPGFrozenPayoutIsACopy(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
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

	if _, err := db.Exec(`UPDATE reward_payouts SET amount = 1 WHERE reward_id = $1`, rewardID); err != nil {
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
	store := NewPGStore(db)
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

// The window generator's idempotency is ON CONFLICT DO NOTHING against
// UNIQUE (event_id, starts_at) -- which is what lets it run every tick over
// overlapping ranges.
func TestPGInsertWindowsIsIdempotent(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
	ctx := context.Background()
	var eventID int64
	if err := db.QueryRow(`INSERT INTO events (slug, cron, timezone) VALUES ('daily','0 0 * * *','UTC') RETURNING id`).Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	ev := Event{ID: eventID, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC"}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	ws, err := GenerateWindows(ev, from, from.Add(5*24*time.Hour))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	n, err := store.InsertWindows(ctx, ws)
	if err != nil || n != len(ws) {
		t.Fatalf("first insert: n=%d err=%v, want %d", n, err, len(ws))
	}
	n, err = store.InsertWindows(ctx, ws)
	if err != nil || n != 0 {
		t.Errorf("re-insert: n=%d err=%v, want 0/nil", n, err)
	}

	// An overlapping range adds only what is new, which is the generator's
	// steady state: every tick regenerates ground it has already covered.
	ws2, _ := GenerateWindows(ev, from.Add(3*24*time.Hour), from.Add(8*24*time.Hour))
	n, err = store.InsertWindows(ctx, ws2)
	if err != nil {
		t.Fatalf("overlapping insert: %v", err)
	}
	if n != 3 {
		t.Errorf("overlapping insert added %d, want 3", n)
	}
}

// Expiry is bounded per sweep, and only touches pending grants that are past
// their expiry.
func TestPGExpireGrantsIsBoundedAndSelective(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rewardID, _, _ := seedDaily(t, db, day)
	ctx := context.Background()

	past := day.Add(-time.Hour)
	future := day.Add(48 * time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO reward_grants (reward_id, user_id, reference, state, expires_at)
		                      VALUES ($1, $2, $3, 'pending', $4)`, rewardID, int64(i), int64(i), past); err != nil {
			t.Fatalf("seed expiring grant: %v", err)
		}
	}
	// One with no expiry and one not yet due: neither may be touched.
	_, _ = db.Exec(`INSERT INTO reward_grants (reward_id, user_id, reference, state) VALUES ($1, 100, 100, 'pending')`, rewardID)
	_, _ = db.Exec(`INSERT INTO reward_grants (reward_id, user_id, reference, state, expires_at) VALUES ($1, 101, 101, 'pending', $2)`, rewardID, future)

	n, err := store.ExpireGrants(ctx, day, 3)
	if err != nil || n != 3 {
		t.Fatalf("bounded sweep: n=%d err=%v, want 3", n, err)
	}
	n, err = store.ExpireGrants(ctx, day, 100)
	if err != nil || n != 2 {
		t.Fatalf("second sweep: n=%d err=%v, want the remaining 2", n, err)
	}
	var stillPending int
	_ = db.QueryRow(`SELECT count(*) FROM reward_grants WHERE state='pending'`).Scan(&stillPending)
	if stillPending != 2 {
		t.Errorf("pending after sweeps = %d, want 2 (no-expiry and not-yet-due)", stillPending)
	}
}

// INTERVAL columns come back as seconds and become time.Duration; getting this
// wrong silently gives every reward an expiry of zero.
func TestPGIntervalRoundTrip(t *testing.T) {
	db := testDB(t)
	store := NewPGStore(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO rewards (slug, kind, trigger, expires_after)
	                      VALUES ('fleeting','one_off','login', INTERVAL '36 hours')`); err != nil {
		t.Fatalf("insert reward: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reward_payouts (reward_id, kind, amount)
	                      SELECT id, 'points', 5 FROM rewards WHERE slug='fleeting'`); err != nil {
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
	store := NewPGStore(db)
	ctx := context.Background()
	var rewardID int64
	if err := db.QueryRow(`INSERT INTO rewards (slug, kind, trigger, delivery)
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

	// A grant past the baseline wins.
	if _, err := store.CreateGrant(ctx,
		Grant{RewardID: rewardID, UserID: 7, Reference: 10500, State: StateCredited},
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
	_ = db.QueryRow(`SELECT value FROM reward_baselines WHERE reward_id=$1 AND user_id=7`, rewardID).Scan(&stored)
	if stored != 10000 {
		t.Errorf("stored baseline = %d, want 10000 — SetBaseline must never lower", stored)
	}

	// Per member, not global.
	if mark, _ := store.PreviousMark(ctx, rewardID, 8); mark != 0 {
		t.Errorf("another member's mark = %d, want 0", mark)
	}
}
