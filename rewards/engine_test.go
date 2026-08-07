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
	// clock drives BOTH the engine and the fake events plugin. Set it with
	// f.travel; assigning f.eng.now directly desynchronises them.
	clock  time.Time
	events *fakeEvents

	eng   *Engine
	store *MemStore
	paid  map[int64]int    // userID -> points credited
	slugs map[int64]string // userID -> the reward slug the last credit named
	mu    sync.Mutex
	fail  error // when set, the points handler returns it
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	f := &fixture{store: NewMemStore(), paid: map[int64]int{}, slugs: map[int64]string{}}
	f.store.Now = now
	f.clock = now
	f.events = newFakeEvents(now)
	// ONE clock behind both. The old tests moved only the engine's, and the mem
	// store answered window questions from a separate instant -- which worked by
	// accident and would now let "tomorrow" ask about today's windows.
	f.events.now = func() time.Time { return f.clock }
	f.eng = NewEngine(f.store, func(string, ...any) {}).WithEvents(f.events)
	f.eng.now = func() time.Time { return f.clock }
	f.eng.Handle(PayoutPoints, func(ctx context.Context, g Grant, p Payout) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != nil {
			return f.fail
		}
		f.paid[g.UserID] += p.Amount
		f.slugs[g.UserID] = g.RewardSlug
		return nil
	})
	return f
}

func (f *fixture) points(userID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paid[userID]
}

// travel moves the one clock both the engine and the events plugin read.
func (f *fixture) travel(to time.Time) {
	f.clock = to
	f.store.Now = to
}

// dailyKey is the occurrence key of the daily event's window containing t.
// Spelled out here rather than hardcoded, so the tests assert the CONTRACT (the
// reference is the occurrence key) and not a string literal that a format change
// would break in nine places.
func dailyKey(t time.Time) string {
	day := t.Truncate(24 * time.Hour)
	w := win(day, day.Add(24*time.Hour))
	// The slug matters: Key() is slug-qualified, which is the whole reason it is
	// a name rather than a timestamp. Building one without it produced
	// "@2026-03-01T00:00:00Z" and the tests caught it immediately.
	w.Slug = "daily"
	return w.Key()
}

// addDaily wires the canonical shape: a contiguous daily event, one recurring
// reward paying points on login.
func (f *fixture) addDaily(now time.Time, delivery Delivery, amount int) Reward {
	day := now.Truncate(24 * time.Hour)
	f.events.add("daily",
		win(day, day.Add(24*time.Hour)),
		win(day.Add(24*time.Hour), day.Add(48*time.Hour)),
	)
	r := Reward{
		ID: 100, Slug: "daily-login", Kind: KindRecurring, EventSlug: "daily",
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
	if want := dailyKey(now); g.Reference != want {
		t.Errorf("reference = %q, want %q (the open occurrence)", g.Reference, want)
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
	f.travel(tomorrow)

	g, err := f.eng.Claim(context.Background(), 7, r.ID)
	if err != nil {
		t.Fatalf("day two: %v", err)
	}
	if want := dailyKey(tomorrow); g.Reference != want {
		t.Errorf("day two reference = %q, want %q (the next occurrence)", g.Reference, want)
	}
	if g.Reference == dailyKey(now) {
		t.Error("day two reused day one's reference; the pay-once UNIQUE would have refused it")
	}
}

// The boundary instant belongs to exactly ONE window. If both matched, every
// midnight would be a free second claim.
func TestWindowBoundaryIsHalfOpen(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.addDaily(now, DeliveryClaim, 10)

	boundary := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	f.travel(boundary)
	got, err := f.events.OpenWindows(context.Background(), []string{"daily"})
	if err != nil {
		t.Fatalf("open windows: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events open at the boundary = %d, want exactly 1", len(got))
	}
	if key := got["daily"].Key(); key != dailyKey(boundary) {
		t.Errorf("boundary belongs to %q, want %q (the NEXT window)", key, dailyKey(boundary))
	}
}

// Outside every window an event-gated reward is not offered at all.
func TestAvailableExcludesClosedEvent(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	summerStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.events.add("summer", win(summerStart, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)))
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 200, Slug: "summer-bonus", Kind: KindRecurring, EventSlug: "summer",
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
	f.travel(inSummer)
	offers, err = f.eng.Available(context.Background(), 7, "login")
	if err != nil {
		t.Fatalf("available in July: %v", err)
	}
	wantSummer := win(summerStart, time.Time{})
	wantSummer.Slug = "summer"
	wantKey := wantSummer.Key()
	if len(offers) != 1 || offers[0].WindowKey != wantKey {
		t.Fatalf("offers in July = %d, want 1 against %q", len(offers), wantKey)
	}

	// And a claim outside the season is refused, not merely unrendered: the
	// surface is advisory, the engine is authoritative.
	f.travel(now)
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
	f.travel(tomorrow)
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
	f.eng.Handle(PayoutMedal, func(ctx context.Context, g Grant, p Payout) error {
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
	f.eng.Handle(PayoutMedal, func(ctx context.Context, g Grant, p Payout) error {
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

// per_unit must pay the DELTA times the rate, not a flat amount and not the
// running total. This is the arithmetic that decides what an uploader is owed,
// and getting it wrong is either underpaying everyone or paying history twice.
func TestGrantPerUnitPaysTheDelta(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	var medals int
	f.eng.Handle(PayoutMedal, func(ctx context.Context, g Grant, p Payout) error {
		medals++
		return nil
	})
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 600, Slug: "grabs", Kind: KindPerUnit, Trigger: "upload",
		Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{
			{Kind: PayoutPoints, Amount: 2},
			{Kind: PayoutMedal, Target: "uploader"},
		},
	})
	ctx := context.Background()

	// 500 grabs from nothing, at 2 points each.
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 500); err != nil {
		t.Fatalf("first: %v", err)
	}
	if got := f.points(7); got != 1000 {
		t.Errorf("first grant paid %d, want 1000 (2 x 500) — a flat payout ignores the delta", got)
	}
	// A medal is not a quantity: 500 grabs is one badge, not 500.
	if medals != 1 {
		t.Errorf("medals awarded = %d, want 1 — only countable lines scale", medals)
	}
	// The handler must be told WHICH reward paid: a 1000-point ledger row with
	// no attribution is the first question in every balance dispute.
	f.mu.Lock()
	slug := f.slugs[7]
	f.mu.Unlock()
	if slug != "grabs" {
		t.Errorf("handler saw reward slug %q, want %q — the ledger cannot attribute this credit", slug, "grabs")
	}

	// Nothing new: no grant, and specifically not an error the caller has to
	// special-case, since a frequent job hits this for nearly every member.
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 500); !errors.Is(err, ErrNothingOwed) {
		t.Errorf("same mark: %v, want ErrNothingOwed", err)
	}

	// 250 more, so 500 points — the DELTA, not 750 x 2.
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 750); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := f.points(7); got != 1500 {
		t.Errorf("running total %d, want 1500 (1000 + 2 x 250) — it re-paid the history", got)
	}

	// The mark going BACKWARDS (a purge, a recount) must pay nothing rather
	// than debiting a member for having fewer grabs than last time.
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 600); !errors.Is(err, ErrNothingOwed) {
		t.Errorf("mark went backwards: %v, want ErrNothingOwed", err)
	}
	if got := f.points(7); got != 1500 {
		t.Errorf("a backwards mark changed the balance to %d", got)
	}
}

// The baseline is what stops a NEW per_unit reward paying for history. Without
// it, a reward worth a point per grab pays every grab the site ever recorded,
// for everyone, on its first run.
func TestGrantPerUnitHonoursTheBaseline(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 600, Slug: "grabs", Kind: KindPerUnit, Trigger: "upload",
		Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 2}},
	})
	ctx := context.Background()

	// This member already had 10,000 grabs when the reward was created.
	if err := f.store.SetBaseline(ctx, 600, 7, 10000); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 10050); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if got := f.points(7); got != 100 {
		t.Errorf("paid %d, want 100 (2 x the 50 since the baseline) — it paid the whole history", got)
	}

	// Re-seeding must never move a baseline BACKWARDS past grants already
	// keyed on it, or everything in between gets paid again.
	if err := f.store.SetBaseline(ctx, 600, 7, 5000); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if _, err := f.eng.GrantPerUnit(ctx, 7, 600, 10050); !errors.Is(err, ErrNothingOwed) {
		t.Error("a lowered baseline re-opened an already-paid range")
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

// Available must ask the events plugin ONCE for a whole trigger, not once per
// reward.
//
// The capability takes a slice for exactly this reason. Per-reward lookups are an
// N+1 across a plugin boundary on the login path, which is where the old store
// comment warned that "a 6-reward login becomes 13 queries" — and the boundary
// makes it worse, since the events plugin is free to answer over a network one
// day.
func TestAvailableAsksEventsOncePerTrigger(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	day := now.Truncate(24 * time.Hour)

	// Three event-gated rewards on one trigger, across two events.
	f.events.add("daily", win(day, day.Add(24*time.Hour)))
	f.events.add("weekly", win(day, day.Add(7*24*time.Hour)))
	for i, slug := range []string{"daily", "weekly", "daily"} {
		f.store.Rewards = append(f.store.Rewards, Reward{
			ID: int64(300 + i), Slug: "gated-" + slug + string(rune('a'+i)),
			Kind: KindRecurring, EventSlug: slug, Trigger: "login",
			Delivery: DeliveryClaim, Enabled: true,
			Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}},
		})
	}

	before := f.events.callCount()
	offers, err := f.eng.Available(context.Background(), 7, "login")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 3 {
		t.Fatalf("offers = %d, want 3", len(offers))
	}
	if n := f.events.callCount() - before; n != 1 {
		t.Errorf("asked the events plugin %d times for one trigger, want 1 — that is an N+1 across a plugin boundary", n)
	}
}
