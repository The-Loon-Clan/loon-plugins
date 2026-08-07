package donations

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What donations announce.
//
// This is the one event so far that is SYSTEM-kind despite a person being
// behind it, and the reason is that a donation does not always have a member.
// The tip jar takes money from anyone; only a donation carrying a
// site_user_id in its BTCPay metadata belongs to an account. So the fact being
// announced is "the site received money", which is true either way, and the
// donor — when there is one — is optional data in the payload rather than
// Event.UserID.
//
// The alternative was worse in both directions. Emitting member-kind with
// UserID 0 for anonymous donations is exactly the case core warns about, and
// only emitting for attributed ones would make the event useless as a
// donations-total signal because it would silently omit part of the money.
//
// A donor badge should NOT be scored off this event. The threshold anyone
// actually wants is dollars, not settlements — "$50 donated", not "donated
// five times" — and that is a SUM over current rows, which is the absolute
// metric path. Counting settlements would rank someone who tipped a dollar
// fifty times above someone who gave five hundred once.
const EventDonationReceived = "donations.received"

// DonationReceived is the Data payload of EventDonationReceived.
//
// DonorUserID is nil for an unattributed donation — the tip jar, a direct
// address, or an invoice whose metadata carried no site user. Subscribers must
// handle nil; it is the normal case, not an error.
type DonationReceived struct {
	DonationID  int64
	DonorUserID *int
	AmountUSD   float64
	Asset       string
	PackageID   *int64
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventDonationReceived, Emitter: "donations", Kind: core.EventSystem, Stable: true,
		Summary: "a donation settled (may be anonymous — the donor, if any, is in the payload not UserID)",
		Payload: "donations.DonationReceived",
	})
}

// WithCore attaches the mediator. Separate from the struct literal in
// Provision so a hand-built Handlers keeps compiling and announces nothing.
func (h *Handlers) WithCore(c *core.Core) *Handlers { h.core = c; return h }

// emit announces, after the donation row has committed. Before it, a webhook
// retry that lost the UNIQUE race would announce money the site never banked.
func (h *Handlers) emit(ctx context.Context, name string, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, Data: data})
}
