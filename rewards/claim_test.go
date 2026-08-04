package rewards

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// claimFixture: one claim-delivery reward, no event, paying points and a medal.
func claimFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	f := newFixture(t, now)
	f.eng.Handle(PayoutMedal, func(ctx context.Context, g Grant, p Payout) error { return nil })
	f.store.Rewards = append(f.store.Rewards, Reward{
		ID: 900, Slug: "welcome", Name: "Welcome pack", Kind: KindOneOff,
		Trigger: "signup", Delivery: DeliveryClaim, Enabled: true,
		Payouts: []Payout{
			{Kind: PayoutPoints, Amount: 250},
			{Kind: PayoutMedal, Target: "founder"},
		},
	})
	return f
}

// A pending grant must be reachable by the member it belongs to, with the
// payout it was offered. Before this existed the grant was written and then
// unreachable forever, which is worse than never offering it.
func TestPendingIsVisibleToItsOwner(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := claimFixture(t, now)
	ctx := context.Background()

	if _, err := f.eng.Claim(ctx, 7, 900); err != nil {
		t.Fatalf("claim: %v", err)
	}
	pending, err := f.eng.Pending(ctx, 7)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	// The FROZEN lines, all of them — this is what the member was offered,
	// not what is left to execute.
	if got := describePayouts(pending[0].Payouts); got != "250 points and the founder medal" {
		t.Errorf("offer reads %q, want both lines", got)
	}

	// And invisible to anybody else.
	if other, _ := f.eng.Pending(ctx, 8); len(other) != 0 {
		t.Errorf("another member sees %d pending grant(s)", len(other))
	}
}

// THE security test. Grant ids are sequential integers, so a claim endpoint
// without an ownership check is an IDOR that pays an attacker out of someone
// else's pending grants.
func TestClaimGrantRefusesSomebodyElsesGrant(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := claimFixture(t, now)
	ctx := context.Background()

	g, err := f.eng.Claim(ctx, 7, 900)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// User 8 tries to collect user 7's grant.
	if err := f.eng.ClaimGrant(ctx, 8, g.ID); !errors.Is(err, ErrNotOffered) {
		t.Fatalf("cross-user claim: %v, want ErrNotOffered", err)
	}
	if got := f.points(8); got != 0 {
		t.Errorf("attacker was paid %d", got)
	}
	if got := f.points(7); got != 0 {
		t.Errorf("owner's grant settled by someone else: %d", got)
	}

	// A grant that does not exist must be refused IDENTICALLY, or the endpoint
	// becomes an oracle for which ids are real.
	missing := f.eng.ClaimGrant(ctx, 8, 999999)
	if !errors.Is(missing, ErrNotOffered) {
		t.Errorf("unknown grant: %v, want the same ErrNotOffered", missing)
	}

	// The owner can still collect afterwards — a refused attempt must not
	// consume or corrupt the grant.
	if err := f.eng.ClaimGrant(ctx, 7, g.ID); err != nil {
		t.Fatalf("owner claim after a refused attempt: %v", err)
	}
	if got := f.points(7); got != 250 {
		t.Errorf("owner paid %d, want 250", got)
	}
}

// Double-clicking the claim button must pay once.
func TestClaimGrantIsIdempotent(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := claimFixture(t, now)
	ctx := context.Background()

	g, _ := f.eng.Claim(ctx, 7, 900)
	if err := f.eng.ClaimGrant(ctx, 7, g.ID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := f.eng.ClaimGrant(ctx, 7, g.ID); !errors.Is(err, ErrAlreadyGranted) {
		t.Errorf("second claim: %v, want ErrAlreadyGranted", err)
	}
	if got := f.points(7); got != 250 {
		t.Errorf("paid %d after two clicks, want 250", got)
	}
	// And it leaves the pending list.
	if pending, _ := f.eng.Pending(ctx, 7); len(pending) != 0 {
		t.Errorf("still pending after claiming: %d", len(pending))
	}
}

// An expired grant must not be collectable, however it is reached.
func TestClaimGrantRefusesExpired(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := claimFixture(t, now)
	hour := time.Hour
	f.store.Rewards[0].ExpiresAfter = &hour
	ctx := context.Background()

	g, _ := f.eng.Claim(ctx, 7, 900)
	if _, err := f.store.ExpireGrants(ctx, now.Add(2*time.Hour), 100); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := f.eng.ClaimGrant(ctx, 7, g.ID); err == nil {
		t.Error("an expired grant was claimed")
	}
	if got := f.points(7); got != 0 {
		t.Errorf("expired grant paid %d", got)
	}
}

// Claim delivery notifies; auto delivery does not, because there is nothing
// for the member to do about it.
func TestNotifyOnlyForClaimDelivery(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	f := claimFixture(t, now)
	var notified []string
	f.eng.Notifier(func(_ context.Context, userID int64, title, body, link string) {
		notified = append(notified, body)
	})
	if _, err := f.eng.Claim(ctx, 7, 900); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(notified) != 1 || notified[0] != "Welcome pack" {
		t.Errorf("notifications = %v, want one naming the reward", notified)
	}

	auto := newFixture(t, now)
	var autoNotified int
	auto.eng.Notifier(func(context.Context, int64, string, string, string) { autoNotified++ })
	r := auto.addDaily(now, DeliveryAuto, 10)
	if _, err := auto.eng.Claim(ctx, 7, r.ID); err != nil {
		t.Fatalf("auto claim: %v", err)
	}
	if autoNotified != 0 {
		t.Errorf("auto delivery notified %d times; the points simply arrived", autoNotified)
	}
}

// A grant is still created and collectable when notification fails or is
// absent — the nudge is not the delivery mechanism.
func TestGrantSurvivesNoNotifier(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	f := claimFixture(t, now)
	ctx := context.Background()
	// Notifier deliberately never set.
	if _, err := f.eng.Claim(ctx, 7, 900); err != nil {
		t.Fatalf("claim with no notifier: %v", err)
	}
	if pending, _ := f.eng.Pending(ctx, 7); len(pending) != 1 {
		t.Fatal("grant missing when no notifier is wired")
	}
}

// The card renders every branch, and html/template streams — a field the
// template wants and the model lacks truncates the page rather than failing.
func TestClaimCardRenders(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	vm := claimVM{
		Msg: "reward claimed",
		Grants: []claimRow{
			{ID: 1, Pays: "250 points and the founder medal", Expires: "1 Apr 2026"},
			{ID: 2, Pays: "10 points"}, // no expiry: that line must not render
		},
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "claim_card.html", vm); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"Rewards to claim", "reward claimed",
		"250 points", "founder medal", "claim before 1 Apr 2026",
		`name="grant_id" value="1"`, `name="grant_id" value="2"`,
		`action="/plugin/rewards/claim"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card missing %q", want)
		}
	}
	if strings.Count(out, "claim before") != 1 {
		t.Error("the no-expiry grant rendered an expiry line")
	}
}

func TestDescribePayouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []Payout
		want string
	}{
		{"points only", []Payout{{Kind: PayoutPoints, Amount: 50}}, "50 points"},
		{"points and medal", []Payout{{Kind: PayoutPoints, Amount: 50}, {Kind: PayoutMedal, Target: "founder"}},
			"50 points and the founder medal"},
		// Should not happen (a grant with no lines is refused at grant time),
		// but the card must not render a blank offer if it ever does.
		{"empty", nil, "nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describePayouts(tc.in); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}
