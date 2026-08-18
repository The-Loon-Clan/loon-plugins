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
	// PayoutLootbox opens a box: Target is the box slug, one of its entries is
	// drawn by weight, and the reward that entry names is granted. Handled by
	// this plugin rather than by a host registration — the box, the draw and
	// the reward it lands on are all rewards' own furniture (lootbox.go).
	PayoutLootbox PayoutKind = "lootbox"
)

// GrantState tracks a grant from offered to settled.
type GrantState string

const (
	StatePending  GrantState = "pending"
	StateCredited GrantState = "credited"
	StateExpired  GrantState = "expired"
)

// Event and Window used to live here. They are the events plugin's now
// (pluginapi.ScheduledEvent / pluginapi.EventWindow), because a season or a reset
// period is a site fact several systems reference and this one was only the first
// to need it. Rewards holds a SLUG and asks.
//
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
	ID   int64  `db:"id"`
	Slug string `db:"slug"`
	Name string `db:"name"`
	Kind Kind   `db:"kind"`
	// EventSlug names a scheduled event from the events plugin, or is empty for
	// a reward earnable at any time. A slug rather than an id: an id belongs to
	// the schema that owns it, and one stored here would break the moment that
	// table were rebuilt or restored from another host's dump.
	EventSlug    string         `db:"scheduled_event_slug"`
	Trigger      string         `db:"trigger"`
	ExpiresAfter *time.Duration `db:"-"`
	Delivery     Delivery       `db:"delivery"`
	Enabled      bool           `db:"enabled"`

	Payouts []Payout `db:"-"`
}

// Grant is the record of what is owed or was paid.
type Grant struct {
	ID       int64 `db:"id"`
	RewardID int64 `db:"reward_id"`
	// RewardSlug is join-derived on read, never stored: the grant row keeps
	// only reward_id. It exists so a payout handler can attribute what it hands
	// over — a ledger row that says which reward paid is the difference between
	// an auditable credit and 30,000 points labelled "reward".
	RewardSlug string `db:"reward_slug"`
	UserID     int64  `db:"user_id"`
	// Reference is WHICH entitlement this grant is for, and it is what the
	// pay-once UNIQUE is built on. The occurrence key of a scheduled event
	// window for a recurring reward ("summer-2026@2026-08-01T00:00:00Z"), empty
	// for a one_off, because there is only one and it needs no name.
	//
	// It used to be a BIGINT meaning three unrelated things by kind: 0, a window
	// id, or a high-water mark. The mark moved to its own column because a
	// number compared as text makes "9" greater than "10".
	Reference string `db:"reference"`
	// HighWater is HOW FAR we have paid, for per_unit rewards only. Zero
	// everywhere else.
	HighWater int64      `db:"high_water"`
	State     GrantState `db:"state"`
	Reason    string     `db:"reason"`
	CreatedAt time.Time  `db:"created_at"`
	ExpiresAt *time.Time `db:"expires_at"`
	SettledAt *time.Time `db:"settled_at"`

	// Frozen at grant time — see reward_grant_payouts. Reading these back
	// through the reward would let an admin's edit change what an
	// already-offered claim pays.
	Payouts []Payout `db:"-"`
	// Silent hands the reward over without announcing it.
	//
	// Set for a retroactive award: an achievement backfilled to people who
	// earned it before it existed, or an issuance back-paying a cohort.
	// Payout handlers skip the notifying part and do the paying part
	// regardless -- the member gets what they are owed, they just are not
	// told about something that happened months ago.
	Silent bool `db:"silent"`
}

// Offer is a Reward resolved FOR one member at one moment.
//
// Deliberately not a Reward with extra fields: a Reward is true for everybody
// and cacheable, an Offer is true for one member right now and never cached.
// Collapsing them produces a struct whose meaning depends on how it was
// fetched, and a caller that renders a stale "not yet claimed".
type Offer struct {
	Reward Reward
	// WindowKey names the occurrence this would be claimed against, empty for
	// rewards with no event. It IS the reference the claim will key on, which is
	// why it is the key rather than a display string.
	WindowKey string
	// Claimed is ADVISORY. It decides whether the button renders live or
	// greyed; it may be stale by the time it is read. Claim is the only
	// authority, because only the UNIQUE constraint sees both racers.
	Claimed bool
	// PendingGrantID is set when a claim-delivery grant already exists and is
	// waiting to be collected.
	PendingGrantID int64
}
