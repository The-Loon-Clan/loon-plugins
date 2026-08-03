package rewards

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotOffered means the reward is disabled, gated by a closed event, or not
// offered on the surface the caller asked from. Distinct from ErrAlreadyGranted
// because the member-facing wording differs: "not available" versus "you
// already have this".
var ErrNotOffered = errors.New("rewards: not currently offered")

// PayoutHandler executes one line of a grant. Points has a built-in handler;
// roles, medals, achievements and username effects are host or sibling-plugin
// concerns registered through the extension registry.
//
// A handler MUST be idempotent for the same (userID, grantPayoutID): a grant
// that dies between two lines is resumed, not rolled back, because half its
// payout has already left the building.
type PayoutHandler func(ctx context.Context, userID int64, p Payout) error

// Engine resolves and settles rewards. The type the host talks to.
type Engine struct {
	store    Store
	handlers map[PayoutKind]PayoutHandler
	now      func() time.Time // injectable so window-boundary tests are not flaky
	logf     func(string, ...any)
	// notify tells a member a claim is waiting. nil is fine: the grant still
	// exists and the card still shows it, they just are not nudged.
	notify func(ctx context.Context, userID int64, title, body, link string)
}

func NewEngine(store Store, logf func(string, ...any)) *Engine {
	return &Engine{
		store:    store,
		handlers: map[PayoutKind]PayoutHandler{},
		now:      time.Now,
		logf:     logf,
	}
}

// Notifier sets the "you have something to claim" callback.
func (e *Engine) Notifier(fn func(ctx context.Context, userID int64, title, body, link string)) {
	e.notify = fn
}

// Pending returns a member's outstanding claim-delivery grants.
func (e *Engine) Pending(ctx context.Context, userID int64) ([]Grant, error) {
	return e.store.PendingGrantsFor(ctx, userID, 20)
}

// ClaimGrant settles one pending grant FOR THE MEMBER WHO OWNS IT.
//
// The ownership check is the whole reason this exists rather than callers
// reaching Settle directly: grant ids are sequential integers, so a claim
// endpoint without it is an IDOR that pays an attacker from someone else's
// pending grants. A grant belonging to somebody else returns the same error as
// one that does not exist, so the endpoint cannot be used to probe which ids
// are real.
func (e *Engine) ClaimGrant(ctx context.Context, userID, grantID int64) error {
	g, err := e.store.GrantByID(ctx, grantID)
	if err != nil {
		return err
	}
	if g == nil || g.UserID != userID {
		return ErrNotOffered
	}
	if g.State != StatePending {
		// Already collected, or lapsed. Settle would say so too, but saying it
		// here keeps the member-facing wording ours.
		return ErrAlreadyGranted
	}
	return e.Settle(ctx, grantID)
}

// Handle registers the executor for one payout kind. A kind with no handler is
// refused at grant time rather than silently skipped — see Settle.
func (e *Engine) Handle(kind PayoutKind, h PayoutHandler) { e.handlers[kind] = h }

// Defs returns the CONFIGURATION for a surface: what exists and what it pays.
// No user, no state, cacheable.
func (e *Engine) Defs(ctx context.Context, trigger string) ([]Reward, error) {
	return e.store.RewardsByTrigger(ctx, trigger)
}

// Available resolves a surface's rewards FOR one member: filtered to those
// whose event is open, annotated with whether they have already had it.
//
// Three queries regardless of how many rewards there are — the rewards, their
// open windows, this member's grants — because the obvious per-reward shape
// (is the event open? has this been claimed?) is an N+1 that also cannot be
// self-consistent, each round trip seeing a slightly different moment.
func (e *Engine) Available(ctx context.Context, userID int64, trigger string) ([]Offer, error) {
	rewards, err := e.store.RewardsByTrigger(ctx, trigger)
	if err != nil {
		return nil, err
	}
	if len(rewards) == 0 {
		return nil, nil
	}

	now := e.now()
	eventIDs := make([]int64, 0, len(rewards))
	rewardIDs := make([]int64, 0, len(rewards))
	for _, r := range rewards {
		rewardIDs = append(rewardIDs, r.ID)
		if r.EventID != nil {
			eventIDs = append(eventIDs, *r.EventID)
		}
	}
	windows, err := e.store.OpenWindowsFor(ctx, eventIDs, now)
	if err != nil {
		return nil, err
	}
	grants, err := e.store.GrantsForUser(ctx, userID, rewardIDs)
	if err != nil {
		return nil, err
	}

	out := make([]Offer, 0, len(rewards))
	for _, r := range rewards {
		ref, ok := e.reference(r, windows)
		if !ok {
			// Event gated and no window open: not earnable at all right now,
			// so it is not an offer rather than an unavailable one.
			continue
		}
		o := Offer{Reward: r, WindowID: windowIDFor(r, windows)}
		if g, seen := grants[r.ID]; seen {
			// Same reference means this exact entitlement is spoken for. A
			// grant at a DIFFERENT reference is last period's and says nothing
			// about this one.
			if g.Reference == ref {
				o.Claimed = g.State != StateExpired
				if g.State == StatePending {
					o.PendingGrantID = g.ID
				}
			}
		}
		out = append(out, o)
	}
	return out, nil
}

func windowIDFor(r Reward, windows map[int64]Window) int64 {
	if r.EventID == nil {
		return 0
	}
	return windows[*r.EventID].ID
}

// reference computes the idempotency key for a reward right now, and reports
// whether the reward is earnable at all.
//
// This is the single place the kind taxonomy turns into a number, which is the
// entire point of the taxonomy: no reward author ever writes this logic again,
// and there is one implementation to get right rather than one per rule.
func (e *Engine) reference(r Reward, windows map[int64]Window) (int64, bool) {
	if r.EventID != nil {
		w, open := windows[*r.EventID]
		if !open {
			return 0, false
		}
		if r.Kind == KindRecurring {
			// The window IS the period. A real row, so two subsystems cannot
			// compute it differently.
			return w.ID, true
		}
	}
	switch r.Kind {
	case KindOneOff:
		return 0, true
	case KindRecurring:
		// Guarded by a CHECK in the schema, so reaching here means a row was
		// written around it.
		return 0, false
	case KindPerUnit:
		// The high-water mark is the caller's to supply — only the counting
		// system knows how far it has got. GrantPerUnit takes it explicitly.
		return 0, false
	}
	return 0, false
}

// Claim settles one offer for a member. Authoritative: it inserts and lets the
// UNIQUE constraint arbitrate rather than asking first, because asking first is
// what creates the window two tabs race through.
func (e *Engine) Claim(ctx context.Context, userID, rewardID int64) (Grant, error) {
	r, err := e.store.RewardByID(ctx, rewardID)
	if err != nil {
		return Grant{}, err
	}
	if r == nil || !r.Enabled {
		return Grant{}, ErrNotOffered
	}
	now := e.now()

	var windows map[int64]Window
	if r.EventID != nil {
		windows, err = e.store.OpenWindowsFor(ctx, []int64{*r.EventID}, now)
		if err != nil {
			return Grant{}, err
		}
	}
	ref, ok := e.reference(*r, windows)
	if !ok {
		return Grant{}, ErrNotOffered
	}
	return e.grant(ctx, *r, userID, ref, "", now)
}

// ErrNothingOwed means a per_unit reward has already paid up to this mark.
//
// The ordinary outcome, not a failure: a job that runs every ten minutes gets
// this for almost every member almost every time, and a caller that logged it
// as an error would drown in it.
var ErrNothingOwed = errors.New("rewards: nothing owed")

// GrantPerUnit awards the DELTA for a per_unit reward. The caller supplies the
// counter's current value because only it knows what is being counted; the
// engine owns the "have I paid up to here" half.
//
// Separate from Claim because per_unit is the one kind whose reference cannot
// be derived from configuration alone.
func (e *Engine) GrantPerUnit(ctx context.Context, userID, rewardID, highWaterMark int64) (Grant, error) {
	r, err := e.store.RewardByID(ctx, rewardID)
	if err != nil {
		return Grant{}, err
	}
	if r == nil || !r.Enabled || r.Kind != KindPerUnit {
		return Grant{}, ErrNotOffered
	}
	previous, err := e.store.PreviousMark(ctx, rewardID, userID)
	if err != nil {
		return Grant{}, err
	}
	units := highWaterMark - previous
	if units <= 0 {
		// Includes the mark going BACKWARDS — a purge, a recount, a manual
		// ledger edit. Paying a negative delta would debit a member for having
		// fewer grabs than last time, so the floor is "nothing", never a debit.
		return Grant{}, ErrNothingOwed
	}
	return e.grant(ctx, *r, userID, highWaterMark, "", e.now(), units)
}

// grant creates the row and, for auto delivery, settles it immediately.
//
// units scales the payout for a per_unit reward (variadic so the one_off and
// recurring callers, where scaling is meaningless, cannot pass one by mistake).
func (e *Engine) grant(ctx context.Context, r Reward, userID, ref int64, reason string, now time.Time, units ...int64) (Grant, error) {
	if len(r.Payouts) == 0 {
		// A reward with no payout lines pays nothing while looking perfectly
		// healthy — the exact failure that would otherwise be discovered by a
		// member asking where their points went.
		return Grant{}, fmt.Errorf("reward %q has no payout lines", r.Slug)
	}
	// Refuse up front rather than freezing a line nothing can execute. A grant
	// half-settled by a missing handler is stuck pending forever, and the
	// member has been told they have something they cannot get.
	for _, p := range r.Payouts {
		if _, ok := e.handlers[p.Kind]; !ok {
			return Grant{}, fmt.Errorf("reward %q: no handler registered for payout kind %q", r.Slug, p.Kind)
		}
	}

	g := Grant{RewardID: r.ID, UserID: userID, Reference: ref, State: StatePending, Reason: reason}
	if r.ExpiresAfter != nil {
		exp := now.Add(*r.ExpiresAfter)
		g.ExpiresAt = &exp
	}
	lines := r.Payouts
	if len(units) == 1 && units[0] > 1 {
		lines = scalePayouts(lines, units[0])
	}
	g, err := e.store.CreateGrant(ctx, g, lines)
	if err != nil {
		return Grant{}, err
	}
	if r.Delivery == DeliveryClaim && e.notify != nil {
		// Best-effort by design: the grant is durable and the card will show
		// it regardless, so a flaky notification channel must not fail a grant
		// that has already been written.
		name := r.Name
		if name == "" {
			name = r.Slug
		}
		e.notify(ctx, userID, "You have a reward to claim", name, "/")
	}
	if r.Delivery == DeliveryAuto {
		if err := e.Settle(ctx, g.ID); err != nil {
			// The grant exists and is pending, which is recoverable: a later
			// sweep or a claim can settle it. Losing the grant would not be.
			return g, fmt.Errorf("settle grant %d: %w", g.ID, err)
		}
	}
	return g, nil
}

// Settle executes a grant's frozen payout lines and marks it credited.
//
// Line by line, each marked as it lands. A grant that dies partway resumes
// from where it stopped rather than replaying — because the first half of a
// payout has already left the building, and "retry the whole thing" pays the
// points twice.
func (e *Engine) Settle(ctx context.Context, grantID int64) error {
	g, err := e.store.GrantByID(ctx, grantID)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("grant %d not found", grantID)
	}
	if g.State == StateCredited {
		return nil // already done; a double-settle is a no-op, not an error
	}
	if g.State == StateExpired {
		return fmt.Errorf("grant %d has expired", grantID)
	}

	now := e.now()
	for _, p := range g.Payouts {
		h, ok := e.handlers[p.Kind]
		if !ok {
			return fmt.Errorf("grant %d: no handler for payout kind %q", grantID, p.Kind)
		}
		if err := h(ctx, g.UserID, p); err != nil {
			return fmt.Errorf("grant %d: payout %s: %w", grantID, p.Kind, err)
		}
		if err := e.store.MarkPayoutSettled(ctx, p.ID, now); err != nil {
			// The payout LANDED but we failed to record it. Stopping here is
			// the safe direction: a resumed settle re-runs this line, and
			// handlers are required to be idempotent precisely for this.
			return fmt.Errorf("grant %d: mark payout %d settled: %w", grantID, p.ID, err)
		}
	}
	return e.store.SettleGrant(ctx, grantID, now)
}

// Fire grants every auto-delivery reward on a trigger that this member is
// currently entitled to. The push half of the login path.
//
// Errors are collected rather than propagated per reward: one broken reward
// must not stop the other five from paying, and the caller (a login handler)
// has nothing useful to do with the failure anyway.
func (e *Engine) Fire(ctx context.Context, userID int64, trigger string) int {
	offers, err := e.Available(ctx, userID, trigger)
	if err != nil {
		e.logf("rewards: fire %s for user %d: %v", trigger, userID, err)
		return 0
	}
	var granted int
	for _, o := range offers {
		if o.Reward.Delivery != DeliveryAuto || o.Claimed {
			continue
		}
		if _, err := e.Claim(ctx, userID, o.Reward.ID); err != nil {
			if errors.Is(err, ErrAlreadyGranted) {
				// Another request beat us to it. The constraint did its job.
				continue
			}
			e.logf("rewards: grant %q to user %d: %v", o.Reward.Slug, userID, err)
			continue
		}
		granted++
	}
	return granted
}

// scalePayouts multiplies the COUNTABLE lines of a per_unit payout by how many
// units are owed.
//
// Only points scale. A reward paying "2 points and the Uploader medal" for 500
// new grabs owes 1000 points and ONE medal — a medal is not a quantity, and
// neither is a role, an achievement or a username effect. Scaling those would
// hand out 500 identical badges, which is at best noise and at worst 500 rows
// in someone else's table.
func scalePayouts(lines []Payout, units int64) []Payout {
	out := make([]Payout, 0, len(lines))
	for _, p := range lines {
		if p.Kind == PayoutPoints {
			// int64 throughout the multiply: a large delta against a missing
			// baseline is exactly the case that would overflow an int, and
			// silently paying a negative amount is worse than paying too much.
			scaled := int64(p.Amount) * units
			p.Amount = int(scaled)
		}
		out = append(out, p)
	}
	return out
}
