package rewards

import (
	"context"
	"errors"
	"time"
)

// ErrAlreadyGranted means the (reward, user, reference) row already exists.
//
// A business outcome, not a failure: it is what the UNIQUE constraint says
// when two racers both try to claim, and the loser must be told "you already
// have this" rather than shown a 500. Every claim path decides by attempting
// the insert, so this is the normal way "already paid" is discovered.
var ErrAlreadyGranted = errors.New("rewards: already granted")

// ErrNoOpenWindow means the reward's event has no window containing now.
// Outside a season, nothing is owed and nothing is an error.
var ErrNoOpenWindow = errors.New("rewards: no open window")

// Store is the data seam. Everything the engine needs and nothing else, so a
// test double is small enough to be honest.
type Store interface {
	// ── Reads the engine does on a hot path ────────────────────────────────

	// RewardsByTrigger returns enabled rewards for a surface, payouts
	// attached. Called on every login, so it reads one small indexed table.
	RewardsByTrigger(ctx context.Context, trigger string) ([]Reward, error)

	// RewardBySlug returns one reward with its payouts, or nil if unknown.
	RewardBySlug(ctx context.Context, slug string) (*Reward, error)

	// RewardByID returns one reward with its payouts, or nil if unknown.
	RewardByID(ctx context.Context, id int64) (*Reward, error)

	// OpenWindowsFor resolves, in ONE query, which window of each given event
	// contains `at`. Returns eventID -> window for those that have one.
	//
	// Plural because the alternative is a query per reward on the login path,
	// and a per-item round trip is how a 6-reward login becomes 13 queries.
	OpenWindowsFor(ctx context.Context, eventIDs []int64, at time.Time) (map[int64]Window, error)

	// PreviousMark is how far a per_unit reward has already paid this member:
	// the highest reference it has granted, or the baseline recorded when the
	// reward was created, whichever is greater.
	//
	// The baseline is what stops a new reward paying for history. Without one,
	// "2 points per grab" pays every grab the site has ever recorded on its
	// first run — which is why creating a per_unit reward and seeding
	// baselines has to be the same operation.
	PreviousMark(ctx context.Context, rewardID, userID int64) (int64, error)

	// PreviousMarks is PreviousMark for many members in one query. The batch
	// path exists because the per-member one would be thousands of round trips
	// to discover that almost nobody's counter moved.
	PreviousMarks(ctx context.Context, rewardID int64, userIDs []int64) (map[int64]int64, error)

	// SetBaseline records where a per_unit reward starts counting for one
	// member. Idempotent: re-seeding must not move a baseline that has already
	// had grants keyed past it.
	SetBaseline(ctx context.Context, rewardID, userID, value int64) error

	// GrantsForUser returns this member's grants against the given rewards,
	// keyed by reward id. Advisory: it decides whether a button renders live,
	// never whether a claim succeeds.
	GrantsForUser(ctx context.Context, userID int64, rewardIDs []int64) (map[int64]Grant, error)

	// ── Writes ─────────────────────────────────────────────────────────────

	// CreateGrant inserts a grant and freezes the reward's payout lines onto
	// it, in one transaction. Returns ErrAlreadyGranted if the
	// (reward, user, reference) row exists.
	//
	// Freezing is not an optimisation: reading payouts back through the reward
	// at settle time would let an admin's edit change what an already-offered
	// claim pays.
	CreateGrant(ctx context.Context, g Grant, payouts []Payout) (Grant, error)

	// GrantByID returns a grant with its frozen payout lines, or nil.
	GrantByID(ctx context.Context, id int64) (*Grant, error)

	// MarkPayoutSettled records that one frozen line was executed, so a grant
	// that died between its points credit and its medal resumes rather than
	// replaying the credit.
	MarkPayoutSettled(ctx context.Context, payoutID int64, at time.Time) error

	// SettleGrant moves a grant to credited once every line is settled.
	SettleGrant(ctx context.Context, grantID int64, at time.Time) error

	// ExpireGrants marks pending grants past their expiry, returning the
	// count. Bounded per call: an unbounded sweep on a table this size is
	// fine today and is not a habit worth forming.
	ExpireGrants(ctx context.Context, now time.Time, limit int) (int, error)

	// ── Events and windows ─────────────────────────────────────────────────

	// EventsWithCron returns enabled events that generate their own windows.
	EventsWithCron(ctx context.Context) ([]Event, error)

	// LastWindowEnd returns when an event's furthest-ahead window closes, so
	// the generator knows where to resume. Zero if it has none.
	LastWindowEnd(ctx context.Context, eventID int64) (time.Time, error)

	// InsertWindows adds generated windows, ignoring any that already exist.
	// Returns how many were new — the number that makes a generator run
	// legible in a job log.
	InsertWindows(ctx context.Context, ws []Window) (int, error)
}
