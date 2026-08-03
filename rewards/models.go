package rewards

import "time"

// Kind decides what a grant's reference MEANS, and therefore what "already
// paid" is. A typed set rather than bare strings because every one of these is
// compared against a literal somewhere, and a mistyped literal silently takes
// the wrong branch — which here means paying twice or never.
type Kind string

const (
	// KindOneOff pays at most once ever. reference is always 0.
	KindOneOff Kind = "one_off"
	// KindRecurring pays at most once per event window. reference is the
	// window id, so "already paid for this window" is a key lookup.
	KindRecurring Kind = "recurring"
	// KindPerUnit pays for the delta since last time. reference is the
	// high-water mark of whatever is being counted.
	KindPerUnit Kind = "per_unit"
)

// Delivery decides whether a grant lands in the balance or waits.
type Delivery string

const (
	// DeliveryAuto credits as soon as the grant is created.
	DeliveryAuto Delivery = "auto"
	// DeliveryClaim creates a pending grant the member collects later. This
	// is what lets someone who appears once a year still be paid.
	DeliveryClaim Delivery = "claim"
)

// PayoutKind is the vocabulary of things a reward can hand over. Adding one is
// a CHECK edit plus a handler; the schema does not change shape.
type PayoutKind string

const (
	PayoutPoints      PayoutKind = "points"
	PayoutRole        PayoutKind = "role"
	PayoutMedal       PayoutKind = "medal"
	PayoutAchievement PayoutKind = "achievement"
	PayoutUsernameFX  PayoutKind = "username_fx"
)

// GrantState tracks a grant from offered to settled.
type GrantState string

const (
	StatePending  GrantState = "pending"
	StateCredited GrantState = "credited"
	StateExpired  GrantState = "expired"
)

// Event is a thing that happens; its windows are when.
type Event struct {
	ID          int64          `db:"id"`
	Slug        string         `db:"slug"`
	Name        string         `db:"name"`
	Description string         `db:"description"`
	Cron        *string        `db:"cron"`
	Duration    *time.Duration `db:"-"`
	Timezone    string         `db:"timezone"`
	Enabled     bool           `db:"enabled"`
}

// Window is one concrete occurrence, half-open: a member acting exactly at
// ends_at belongs to the next window, not this one. Closed-closed would make
// the boundary instant belong to both, which for a contiguous reset is a
// second free claim every midnight.
type Window struct {
	ID       int64     `db:"id"`
	EventID  int64     `db:"event_id"`
	StartsAt time.Time `db:"starts_at"`
	EndsAt   time.Time `db:"ends_at"`
}

// Payout is one line of what a reward hands over.
type Payout struct {
	ID       int64      `db:"id"`
	RewardID int64      `db:"reward_id"`
	Kind     PayoutKind `db:"kind"`
	Target   string     `db:"target"`
	Amount   int        `db:"amount"`
	Ordinal  int        `db:"ordinal"`

	// settled is only meaningful for a FROZEN line (reward_grant_payouts);
	// unexported because a caller has no business setting it and the store is
	// the only thing that reads it back.
	settled time.Time `db:"-"`
}

// Reward is what is earnable and on what terms. What it PAYS is Payouts.
type Reward struct {
	ID           int64          `db:"id"`
	Slug         string         `db:"slug"`
	Name         string         `db:"name"`
	Kind         Kind           `db:"kind"`
	EventID      *int64         `db:"event_id"`
	Trigger      string         `db:"trigger"`
	ExpiresAfter *time.Duration `db:"-"`
	Delivery     Delivery       `db:"delivery"`
	Enabled      bool           `db:"enabled"`

	Payouts []Payout `db:"-"`
}

// Grant is the record of what is owed or was paid.
type Grant struct {
	ID        int64      `db:"id"`
	RewardID  int64      `db:"reward_id"`
	UserID    int64      `db:"user_id"`
	Reference int64      `db:"reference"`
	State     GrantState `db:"state"`
	Reason    string     `db:"reason"`
	CreatedAt time.Time  `db:"created_at"`
	ExpiresAt *time.Time `db:"expires_at"`
	SettledAt *time.Time `db:"settled_at"`

	// Frozen at grant time — see reward_grant_payouts. Reading these back
	// through the reward would let an admin's edit change what an
	// already-offered claim pays.
	Payouts []Payout `db:"-"`
}

// Offer is a Reward resolved FOR one member at one moment.
//
// Deliberately not a Reward with extra fields: a Reward is true for everybody
// and cacheable, an Offer is true for one member right now and never cached.
// Collapsing them produces a struct whose meaning depends on how it was
// fetched, and a caller that renders a stale "not yet claimed".
type Offer struct {
	Reward Reward
	// WindowID is which window this would be claimed against, 0 for rewards
	// with no event. It is the reference the claim will key on.
	WindowID int64
	// Claimed is ADVISORY. It decides whether the button renders live or
	// greyed; it may be stale by the time it is read. Claim is the only
	// authority, because only the UNIQUE constraint sees both racers.
	Claimed bool
	// PendingGrantID is set when a claim-delivery grant already exists and is
	// waiting to be collected.
	PendingGrantID int64
}
