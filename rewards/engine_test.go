package rewards

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fixture builds an engine over a MemStore with a recording points handler.
type fixture struct {
	eng   *Engine
	store *MemStore
	paid  map[int64]int // userID -> points credited
	mu    sync.Mutex
	fail  error // when set, the points handler returns it
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	f := &fixture{store: NewMemStore(), paid: map[int64]int{}}
	f.store.Now = now
	f.eng = NewEngine(f.store, func(string, ...any) {})
	f.eng.now = func() time.Time { return now }
	f.eng.Handle(PayoutPoints, func(ctx context.Context, userID int64, p Payout) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != nil {
			return f.fail
		}
		f.paid[userID] += p.Amount
		return nil
	})
	return f
}

func (f *fixture) points(userID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paid[userID]
}

func eventID(id int64) *int64 { return &id }

// addDaily wires the canonical shape: a contiguous daily event, one recurring
// reward paying points on login.
func (f *fixture) addDaily(now time.Time, delivery Delivery, amount int) Reward {
	f.store.Events = append(f.store.Events, Event{ID: 1, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC", Enabled: true})
	day := now.Truncate(24 * time.Hour)
	f.store.Windows = append(f.store.Windows,
		Window{ID: 10, EventID: 1, StartsAt: day, EndsAt: day.Add(24 * time.Hour)},
		Window{ID: 11, EventID: 1, StartsAt: day.Add(24 * time.Hour), EndsAt: day.Add(48 * time.Hour)},
	)
	r := Reward{
		ID: 100, Slug: "daily-login", Kind: KindRecurring, EventID: eventID(1),
		Trigger: "login", Delivery: delivery, Enabled: true,
		Payouts: []Payout{{RewardID: 100, Kind: PayoutPoints, Amount: amount}},
	}
	f.store.Rewards = append(f.store.Rewards, r)
	return r
}

// The headline claim: a recurring reward pays once per window and refuses the
// second attempt inside the same one.
func TestClaimRecurringIsOncePerWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	r := f.addDaily(now, DeliveryClaim, 10)

	g, err := f.eng.Claim(context.Background(), 7, r.ID)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if g.Reference != 10 {
		t.Errorf("reference = %d, want 10 (the open window id)", g.Reference)
	}

	if _, err := f.eng.Claim(context.Background(), 7, r.ID); !errors.Is(err, ErrAlreadyGranted) {
		t.Fatalf("second claim in the same window: %v, want ErrAlreadyGranted", err)
	}

	// A different member is unaffected — the key is per (reward, user, window).
	if _, err := f.eng.Claim(context.Background(), 8, r.ID); err != nil {
		t.Errorf("other member's claim: %v", err)
	}
}

// ...and pays again once the window rolls, WITHOUT anything resetting state.
// That is the property that makes downtime cost latency rather than money.
func TestClaimRecurringPaysAgainNextWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	r := f.addDaily(now, DeliveryClaim, 10)

	if _, err := f.eng.Claim(context.Background(), 7, r.ID); err != nil {
		t.Fatalf("day one: %v", err)
	}
	// Move into the next window. Nothing is cleared or expired.
	tomorrow := now.Add(24 * time.Hour)
	f.eng.now = func() time.Time { return tomorrow }

	g, err := f.eng.Claim(context.Background(), 7, r.ID)
	if err != nil {
		t.Fatalf("day two: %v", err)
	}
	if g.Reference != 11 {
		t.Errorf("day two reference = %d, want 11 (the next window)", g.Reference)
	}
}

// The boundary instant belongs to exactly ONE window. If both matched, every
// midnight would be a free second claim.
func TestWindowBoundaryIsHalfOpen(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.addDaily(now, DeliveryClaim, 10)

	boundary := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	got, err := f.store.OpenWindowsFor(context.Background(), []int64{1}, boundary)
	if err != nil {
		t.Fatalf("open windows: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("windows containing the boundary = %d, want exactly 1", len(got))
	}
	if got[1].ID != 11 {
		t.Errorf("boundary belongs to window %d, want 11 (the NEXT one)", got[1].ID)
	}
}

// Outside every window an event-gated reward is not offered at all.
func TestAvailableExcludesClosedEvent(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.store.Events = append(f.store.Events, Event{ID: 2, Slug: "summer", Enabled: true})
	f.store.Windows = append(f.store.Windows, Window{
		ID: 20, EventID: 2,
		StartsAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	})
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 200, Slug: "summer-bonus", Kind: KindRecurring, EventID: eventID(2),
		Trigger: "login", Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 500}},
	})

	offers, err := f.eng.Available(context.Background(), 7, "login")
	if err != nil {
		t.Fatalf("available in March: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("offers outside the season = %d, want 0", len(offers))
	}

	inSummer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	f.eng.now = func() time.Time { return inSummer }
	offers, err = f.eng.Available(context.Background(), 7, "login")
	if err != nil {
		t.Fatalf("available in July: %v", err)
	}
	if len(offers) != 1 || offers[0].WindowID != 20 {
		t.Fatalf("offers in July = %d, want 1 against window 20", len(offers))
	}

	// And a claim outside the season is refused, not merely unrendered: the
	// surface is advisory, the engine is authoritative.
	f.eng.now = func() time.Time { return now }
	if _, err := f.eng.Claim(context.Background(), 7, 200); !errors.Is(err, ErrNotOffered) {
		t.Errorf("claim outside the season: %v, want ErrNotOffered", err)
	}
}

// Available's Claimed flag must reflect THIS window only. A grant from
// yesterday says nothing about today, and treating it as "claimed" is how a
// daily reward silently stops paying after day one.
func TestAvailableClaimedIsPerWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	r := f.addDaily(now, DeliveryClaim, 10)

	if _, err := f.eng.Claim(context.Background(), 7, r.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	offers, _ := f.eng.Available(context.Background(), 7, "login")
	if len(offers) != 1 || !offers[0].Claimed {
		t.Fatalf("same window: offers=%d claimed=%v, want 1/true", len(offers), offers[0].Claimed)
	}

	tomorrow := now.Add(24 * time.Hour)
	f.eng.now = func() time.Time { return tomorrow }
	offers, _ = f.eng.Available(context.Background(), 7, "login")
	if len(offers) != 1 || offers[0].Claimed {
		t.Fatalf("next window: offers=%d claimed=%v, want 1/false", len(offers), offers[0].Claimed)
	}
}

// Auto delivery credits immediately; claim delivery leaves it pending.
func TestDeliveryDecidesWhenPointsMove(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	auto := newFixture(t, now)
	rAuto := auto.addDaily(now, DeliveryAuto, 25)
	if _, err := auto.eng.Claim(context.Background(), 7, rAuto.ID); err != nil {
		t.Fatalf("auto claim: %v", err)
	}
	if got := auto.points(7); got != 25 {
		t.Errorf("auto delivery credited %d, want 25", got)
	}
	if gs := auto.store.Grants(); len(gs) != 1 || gs[0].State != StateCredited {
		t.Errorf("auto grant state = %v, want credited", gs[0].State)
	}

	claim := newFixture(t, now)
	rClaim := claim.addDaily(now, DeliveryClaim, 25)
	g, err := claim.eng.Claim(context.Background(), 7, rClaim.ID)
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	if got := claim.points(7); got != 0 {
		t.Errorf("claim delivery credited %d before settle, want 0", got)
	}
	if err := claim.eng.Settle(context.Background(), g.ID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := claim.points(7); got != 25 {
		t.Errorf("after settle credited %d, want 25", got)
	}
}

// A grant hands over EVERY line, and settling twice does not pay twice.
func TestSettleExecutesEveryLineExactlyOnce(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)

	var medals []string
	f.eng.Handle(PayoutMedal, func(ctx context.Context, userID int64, p Payout) error {
		medals = append(medals, p.Target)
		return nil
	})
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 300, Slug: "welcome", Kind: KindOneOff, Trigger: "signup",
		Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{
			{Kind: PayoutPoints, Amount: 500, Ordinal: 0},
			{Kind: PayoutMedal, Target: "founder", Ordinal: 1},
		},
	})

	g, err := f.eng.Claim(context.Background(), 7, 300)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.eng.Settle(context.Background(), g.ID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if f.points(7) != 500 || len(medals) != 1 {
		t.Fatalf("after settle: points=%d medals=%v, want 500/[founder]", f.points(7), medals)
	}

	if err := f.eng.Settle(context.Background(), g.ID); err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if f.points(7) != 500 || len(medals) != 1 {
		t.Errorf("double settle paid again: points=%d medals=%v", f.points(7), medals)
	}
}

// A grant that dies partway must RESUME, not replay. Replaying re-credits
// points that already landed.
func TestSettleResumesAfterAFailedLine(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)

	var medalCalls int
	medalErr := errors.New("medal service down")
	f.eng.Handle(PayoutMedal, func(ctx context.Context, userID int64, p Payout) error {
		medalCalls++
		if medalCalls == 1 {
			return medalErr
		}
		return nil
	})
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 300, Slug: "welcome", Kind: KindOneOff, Trigger: "signup",
		Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{
			{Kind: PayoutPoints, Amount: 500, Ordinal: 0},
			{Kind: PayoutMedal, Target: "founder", Ordinal: 1},
		},
	})

	g, _ := f.eng.Claim(context.Background(), 7, 300)
	if err := f.eng.Settle(context.Background(), g.ID); !errors.Is(err, medalErr) {
		t.Fatalf("first settle: %v, want the medal failure", err)
	}
	if f.points(7) != 500 {
		t.Fatalf("points after the failed settle = %d, want 500 (the line that DID land)", f.points(7))
	}

	if err := f.eng.Settle(context.Background(), g.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := f.points(7); got != 500 {
		t.Errorf("points after retry = %d, want 500 — the credited line was replayed", got)
	}
}

// A reward naming a payout kind nothing can execute must be refused at grant
// time. Freezing it instead leaves a grant stuck pending forever, with the
// member told they have something they cannot collect.
func TestGrantRefusesUnhandledPayoutKind(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 400, Slug: "fancy", Kind: KindOneOff, Trigger: "signup",
		Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{{Kind: PayoutUsernameFX, Target: "rainbow"}},
	})
	if _, err := f.eng.Claim(context.Background(), 7, 400); err == nil {
		t.Error("claim succeeded with no handler for username_fx")
	}
	if gs := f.store.Grants(); len(gs) != 0 {
		t.Errorf("grants created = %d, want 0 — nothing should be frozen", len(gs))
	}
}

// A reward with no payout lines pays nothing while looking healthy.
func TestGrantRefusesEmptyPayout(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 500, Slug: "hollow", Kind: KindOneOff, Trigger: "signup",
		Delivery: DeliveryClaim, Enabled: true,
	})
	if _, err := f.eng.Claim(context.Background(), 7, 500); err == nil {
		t.Error("a reward with no payout lines was granted")
	}
}

// per_unit pays the delta and never the history: the second call at the same
// high-water mark is refused, and a higher one is a new grant.
func TestGrantPerUnitPaysTheDelta(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 600, Slug: "grabs", Kind: KindPerUnit, Trigger: "upload",
		Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 2}},
	})
	ctx := context.Background()

	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 500); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 500); !errors.Is(err, ErrAlreadyGranted) {
		t.Errorf("same high-water mark: %v, want ErrAlreadyGranted", err)
	}
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 750); err != nil {
		t.Errorf("higher high-water mark: %v, want a new grant", err)
	}
}

// Concurrent claims must produce exactly one grant. This is the property the
// whole model rests on, so it is asserted rather than assumed.
func TestConcurrentClaimsProduceOneGrant(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	r := f.addDaily(now, DeliveryAuto, 10)

	var wg sync.WaitGroup
	var succeeded int
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.eng.Claim(context.Background(), 7, r.ID); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("successful claims = %d, want exactly 1", succeeded)
	}
	if gs := f.store.Grants(); len(gs) != 1 {
		t.Errorf("grants = %d, want 1", len(gs))
	}
	if got := f.points(7); got != 10 {
		t.Errorf("points credited = %d, want 10", got)
	}
}

// Fire grants auto-delivery rewards and is safe to call on every login.
func TestFireIsIdempotentWithinAWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.addDaily(now, DeliveryAuto, 10)
	ctx := context.Background()

	if n := f.eng.Fire(ctx, 7, "login"); n != 1 {
		t.Errorf("first fire granted %d, want 1", n)
	}
	if n := f.eng.Fire(ctx, 7, "login"); n != 0 {
		t.Errorf("second fire in the same window granted %d, want 0", n)
	}
	if got := f.points(7); got != 10 {
		t.Errorf("points after two fires = %d, want 10", got)
	}
}

// One broken reward must not stop the others paying.
func TestFireSurvivesABrokenReward(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.addDaily(now, DeliveryAuto, 10)
	// A second reward on the same trigger whose payout kind has no handler.
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 700, Slug: "broken", Kind: KindOneOff, Trigger: "login",
		Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutUsernameFX, Target: "rainbow"}},
	})

	if n := f.eng.Fire(context.Background(), 7, "login"); n != 1 {
		t.Errorf("fire granted %d, want 1 (the healthy reward)", n)
	}
	if got := f.points(7); got != 10 {
		t.Errorf("points = %d, want 10 — the broken reward blocked the working one", got)
	}
}

// A grant carries the payout FROZEN: retuning the reward afterwards must not
// change what an outstanding claim pays.
func TestFrozenPayoutSurvivesARetune(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	r := f.addDaily(now, DeliveryClaim, 50)

	g, err := f.eng.Claim(context.Background(), 7, r.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// An admin cuts the reward in half before the member collects.
	f.store.Rewards[0].Payouts[0].Amount = 20

	if err := f.eng.Settle(context.Background(), g.ID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := f.points(7); got != 50 {
		t.Errorf("settled for %d, want 50 — what was offered is what is owed", got)
	}
}

// An expired grant must not settle: the offer lapsed.
func TestSettleRefusesAnExpiredGrant(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	hour := time.Hour
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 800, Slug: "fleeting", Kind: KindOneOff, Trigger: "login",
		Delivery: DeliveryClaim, Enabled: true, ExpiresAfter: &hour,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 10}},
	})
	ctx := context.Background()
	g, err := f.eng.Claim(ctx, 7, 800)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if g.ExpiresAt == nil || !g.ExpiresAt.Equal(now.Add(hour)) {
		t.Fatalf("expires_at = %v, want %v", g.ExpiresAt, now.Add(hour))
	}

	later := now.Add(2 * time.Hour)
	n, err := f.store.ExpireGrants(ctx, later, 100)
	if err != nil || n != 1 {
		t.Fatalf("expire sweep: n=%d err=%v, want 1/nil", n, err)
	}
	if err := f.eng.Settle(ctx, g.ID); err == nil {
		t.Error("settled an expired grant")
	}
	if got := f.points(7); got != 0 {
		t.Errorf("expired grant paid %d, want 0", got)
	}
}
