package rewards

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Available and Claim must agree about an expired grant.
//
// The UNIQUE (reward, user, reference) constraint has no state column: an
// expired grant occupies its reference forever, so Claim on the same reference
// can only ever return ErrAlreadyGranted. If Available meanwhile reports the
// offer as unclaimed, every surface renders a live button that no member can
// ever successfully press — and Fire retries the doomed insert on every single
// login for the rest of the account's life.
//
// Lapsed means lost. That is what ClaimGrant says ("already collected, or
// lapsed"), what Settle enforces, and what PreviousMark assumes when it counts
// expired references as paid. Available is not entitled to a private opinion.
func TestAvailableAgreesWithClaimAfterExpiry(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	hour := time.Hour
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 300, Slug: "welcome", Kind: KindOneOff, Trigger: "login",
		Delivery: DeliveryClaim, ExpiresAfter: &hour, Enabled: true,
		Payouts: []Payout{{RewardID: 300, Kind: PayoutPoints, Amount: 100}},
	})
	ctx := context.Background()

	if _, err := f.eng.Claim(ctx, 7, 300); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The member never collects; the sweep lapses the grant.
	if _, err := f.store.ExpireGrants(ctx, now.Add(2*time.Hour), 100); err != nil {
		t.Fatalf("expire: %v", err)
	}

	offers, err := f.eng.Available(ctx, 7, "login")
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want 1", len(offers))
	}

	// What would actually happen if the member pressed the button.
	_, claimErr := f.eng.Claim(ctx, 7, 300)

	if !offers[0].Claimed && errors.Is(claimErr, ErrAlreadyGranted) {
		t.Fatalf("Available says unclaimed but Claim says ErrAlreadyGranted — " +
			"the offer renders as a live button that can never succeed")
	}
	if offers[0].Claimed && claimErr == nil {
		t.Fatalf("Available says claimed but Claim paid — the button was greyed for a live offer")
	}
	// A lapsed pending grant must also not resurface as collectable.
	if offers[0].PendingGrantID != 0 {
		t.Errorf("PendingGrantID = %d for an expired grant — the card would offer a collect that ClaimGrant refuses", offers[0].PendingGrantID)
	}
}

// The expiry sweep must not lapse a grant whose delivery is mid-flight.
//
// Settle executes payout lines one at a time, marking each as it lands. If the
// sweep runs between two lines, the grant is pending with settled lines: part
// of the payout has left the building. Expiring it then strands the remainder
// in a state Settle refuses to resume — the member got the points and lost the
// medal, permanently, with nothing anywhere reporting it.
func TestExpirySweepSkipsMidDeliveryGrants(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := newFixture(t, now)
	hour := time.Hour
	medalDown := errors.New("medal service down")
	medalErr := &medalDown
	var medals int
	f.eng.Handle(PayoutMedal, func(ctx context.Context, g Grant, p Payout) error {
		if *medalErr != nil {
			return *medalErr
		}
		medals++
		return nil
	})
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 400, Slug: "combo", Kind: KindOneOff, Trigger: "login",
		Delivery: DeliveryClaim, ExpiresAfter: &hour, Enabled: true,
		Payouts: []Payout{
			{RewardID: 400, Kind: PayoutPoints, Amount: 100},
			{RewardID: 400, Kind: PayoutMedal, Target: "founder"},
		},
	})
	ctx := context.Background()

	g, err := f.eng.Claim(ctx, 7, 400)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// First settle: points land, the medal line fails — delivery is mid-flight.
	if err := f.eng.Settle(ctx, g.ID); err == nil {
		t.Fatal("settle succeeded with the medal handler down")
	}
	if got := f.points(7); got != 100 {
		t.Fatalf("points after partial settle = %d, want 100", got)
	}

	// The sweep runs long after expiry. It must leave this grant alone.
	if _, err := f.store.ExpireGrants(ctx, now.Add(48*time.Hour), 100); err != nil {
		t.Fatalf("expire: %v", err)
	}
	loaded, err := f.store.GrantByID(ctx, g.ID)
	if err != nil || loaded == nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.State == StateExpired {
		t.Fatal("the sweep expired a mid-delivery grant — the member keeps the points and loses the medal forever")
	}

	// The medal service recovers; the resume pays ONLY the remainder.
	*medalErr = nil
	if err := f.eng.Settle(ctx, g.ID); err != nil {
		t.Fatalf("resumed settle: %v", err)
	}
	if got := f.points(7); got != 100 {
		t.Errorf("points after resume = %d, want 100 — the settled line replayed", got)
	}
	if medals != 1 {
		t.Errorf("medals = %d, want 1", medals)
	}
}
