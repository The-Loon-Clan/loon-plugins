package donations

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// A donation does not always have a donor. The tip jar takes money from
// anyone, and only an invoice carrying site_user_id in its BTCPay metadata
// belongs to an account — so this event is SYSTEM-kind and puts the donor in
// the payload.
//
// Turning it member-kind is the obvious-looking "fix" and it breaks two ways
// at once: anonymous donations would emit with UserID 0, which core warns
// about precisely because a member event with no member is a bug; and if it
// were suppressed instead, the event would silently omit part of the money and
// be useless as a donations-total signal.
func TestAnonymousDonationIsAnnouncedWithoutInventingAMember(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatalf("declare: %v", err)
	}
	defs := c.EventDefs()
	if len(defs) != 1 {
		t.Fatalf("%d events declared, want 1", len(defs))
	}
	if defs[0].Kind != core.EventSystem {
		t.Fatalf("kind = %q, want system — an anonymous tip has no member to attribute, "+
			"and member-kind would emit UserID 0 for every one of them", defs[0].Kind)
	}

	var got []core.Event
	c.On(EventDonationReceived, "test", func(_ context.Context, e core.Event) { got = append(got, e) })

	donor := 42
	c.Emit(context.Background(), core.Event{Name: EventDonationReceived,
		Data: DonationReceived{DonationID: 1, DonorUserID: nil, AmountUSD: 10}})
	c.Emit(context.Background(), core.Event{Name: EventDonationReceived,
		Data: DonationReceived{DonationID: 2, DonorUserID: &donor, AmountUSD: 25}})

	if len(got) != 2 {
		t.Fatalf("%d events delivered, want 2 — the anonymous one must not be dropped", len(got))
	}
	for i, e := range got {
		if e.UserID != 0 {
			t.Errorf("event %d: UserID = %d; attribution belongs in the payload so subscribers "+
				"read it the same way for both kinds of donation", i, e.UserID)
		}
		if _, ok := e.Data.(DonationReceived); !ok {
			t.Fatalf("event %d: Data is %T, want donations.DonationReceived", i, e.Data)
		}
	}
	if d := got[0].Data.(DonationReceived); d.DonorUserID != nil {
		t.Error("the anonymous donation grew a donor")
	}
	if d := got[1].Data.(DonationReceived); d.DonorUserID == nil || *d.DonorUserID != donor {
		t.Error("the attributed donation lost its donor")
	}
}

// A Handlers with no Core must announce nothing rather than panic.
func TestEmitIsInertWithoutCore(t *testing.T) {
	(&Handlers{}).emit(context.Background(), EventDonationReceived, DonationReceived{})
}
