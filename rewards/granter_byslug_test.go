package rewards

import (
	"context"
	"strings"
	"testing"
)

// The by-slug granter's contract, against the MemStore (which enforces the
// UNIQUE the idempotence claim rests on).

func byslugFixture(t *testing.T) (byslugGranter, *MemStore) {
	t.Helper()
	m := NewMemStore()
	e := NewEngine(m, nil)
	e.Handle(PayoutPoints, func(context.Context, Grant, Payout) error { return nil })
	return byslugGranter{store: m, engine: e}, m
}

// The happy path settles immediately for auto delivery, and the second call
// under the same reference answers granted=false — "already held", which is
// what makes a crashed caller's retry safe.
func TestGrantOneOffIsIdempotentPerReference(t *testing.T) {
	g, m := byslugFixture(t)
	m.Rewards = append(m.Rewards, Reward{
		ID: 1, Slug: "badge-bonus", Kind: KindOneOff, Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 50}},
	})
	ctx := context.Background()

	granted, err := g.GrantOneOff(ctx, 5, "badge-bonus", "centurion")
	if err != nil || !granted {
		t.Fatalf("first grant: granted=%v err=%v", granted, err)
	}
	if gs := m.Grants(); len(gs) != 1 || gs[0].State != StateCredited {
		t.Fatalf("grants = %+v, want one credited (auto delivery settles immediately)", gs)
	}
	if m.Grants()[0].Reason != "achievement: centurion" {
		t.Errorf("reason = %q — a ledger reader should see which achievement paid", m.Grants()[0].Reason)
	}

	// The retry: same member, same reward, same reference.
	granted, err = g.GrantOneOff(ctx, 5, "badge-bonus", "centurion")
	if err != nil || granted {
		t.Fatalf("retry: granted=%v err=%v, want false/nil — already held is the answer, not an error", granted, err)
	}
	if len(m.Grants()) != 1 {
		t.Errorf("%d grants after a retry, want 1", len(m.Grants()))
	}

	// A DIFFERENT reference is a different entitlement: two achievements may
	// pay the same reward, which is what retired the old rule that every
	// achievement owns its reward.
	granted, err = g.GrantOneOff(ctx, 5, "badge-bonus", "veteran")
	if err != nil || !granted {
		t.Fatalf("second reference: granted=%v err=%v", granted, err)
	}
	if len(m.Grants()) != 2 {
		t.Errorf("%d grants for two references, want 2", len(m.Grants()))
	}
}

// Every way a reward can look healthy and hand over nothing is an error —
// the caller is in another plugin now, and this error IS its payability
// report.
func TestGrantOneOffRefusesUnpayableRewards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reward *Reward
		want   string
	}{
		{"unknown", nil, "does not exist"},
		{"disabled", &Reward{ID: 1, Slug: "off", Kind: KindOneOff, Delivery: DeliveryAuto,
			Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}}, "disabled"},
		{"no payout lines", &Reward{ID: 1, Slug: "hollow", Kind: KindOneOff,
			Delivery: DeliveryAuto, Enabled: true}, "no payout lines"},
		{"wrong kind", &Reward{ID: 1, Slug: "seasonal", Kind: KindRecurring,
			Delivery: DeliveryAuto, Enabled: true,
			Payouts: []Payout{{Kind: PayoutPoints, Amount: 1}}}, "one_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, m := byslugFixture(t)
			slug := "missing"
			if tc.reward != nil {
				m.Rewards = append(m.Rewards, *tc.reward)
				slug = tc.reward.Slug
			}
			granted, err := g.GrantOneOff(context.Background(), 5, slug, "ref")
			if err == nil || granted {
				t.Fatalf("granted=%v err=%v, want a refusal", granted, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
			if len(m.Grants()) != 0 {
				t.Errorf("%d grants written despite the refusal", len(m.Grants()))
			}
		})
	}
}

// Claim delivery waits for the member: the grant is written pending and NOT
// settled — the claim card is where it gets collected.
func TestGrantOneOffLeavesClaimDeliveryPending(t *testing.T) {
	g, m := byslugFixture(t)
	m.Rewards = append(m.Rewards, Reward{
		ID: 1, Slug: "claimable", Kind: KindOneOff, Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 50}},
	})
	granted, err := g.GrantOneOff(context.Background(), 5, "claimable", "centurion")
	if err != nil || !granted {
		t.Fatalf("granted=%v err=%v", granted, err)
	}
	if gs := m.Grants(); len(gs) != 1 || gs[0].State != StatePending {
		t.Errorf("grants = %+v, want one pending", gs)
	}
}

// A settle failure does not fail the grant: the row exists and is pending,
// which the expiry sweep and a manual settle can both still pay — losing the
// grant would not be recoverable, a slow settle is.
func TestGrantOneOffSurvivesASettleFailure(t *testing.T) {
	m := NewMemStore()
	e := NewEngine(m, func(string, ...any) {})
	e.Handle(PayoutPoints, func(context.Context, Grant, Payout) error {
		return context.DeadlineExceeded
	})
	g := byslugGranter{store: m, engine: e}
	m.Rewards = append(m.Rewards, Reward{
		ID: 1, Slug: "badge-bonus", Kind: KindOneOff, Delivery: DeliveryAuto, Enabled: true,
		Payouts: []Payout{{Kind: PayoutPoints, Amount: 50}},
	})

	granted, err := g.GrantOneOff(context.Background(), 5, "badge-bonus", "centurion")
	if err != nil || !granted {
		t.Fatalf("granted=%v err=%v — a failed settle must not fail the grant", granted, err)
	}
	if gs := m.Grants(); len(gs) != 1 || gs[0].State != StatePending {
		t.Errorf("grants = %+v, want one still pending after the failed settle", gs)
	}
}
