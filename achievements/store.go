package achievements

import (
	"context"
	"errors"
)

// ErrAlreadyCompleted means the (achievement, member) completion already
// exists.
//
// A business outcome, not a failure: it is what the conditional completion
// says when two evaluations race, and the loser must treat it as "the answer
// is no" rather than a 500. In the shared-schema design this role belonged to
// the reward engine's UNIQUE constraint; the completion row is its own fact
// now, so the sentinel is this plugin's own.
var ErrAlreadyCompleted = errors.New("achievements: already completed")

// UnpaidCompletion is one completed row whose named reward has not been
// recorded as paid — the repair sweep's unit of work.
type UnpaidCompletion struct {
	AchievementID int64  `db:"achievement_id"`
	UserID        int64  `db:"user_id"`
	Slug          string `db:"slug"`
	RewardSlug    string `db:"reward_slug"`
}

// Store is the data seam. Everything the evaluators need and nothing else, so
// a test double is small enough to be honest.
//
// The MemStore is held to the schema's invariants (see store_mem.go): a
// double that is more permissive than production is a test that passes on
// code Postgres rejects. What the mock CANNOT prove is atomicity under
// concurrency — that stays with the conditional-update SQL, whose single
// statement is the arbiter.
type Store interface {
	// AchievementDefsByTrigger returns the enabled achievements a declared
	// event completes the moment it fires.
	AchievementDefsByTrigger(ctx context.Context, trigger string) ([]AchievementDef, error)

	// AchievementDefsByMetric returns every enabled achievement scored by one
	// metric, so a whole set is evaluated from one counter read.
	AchievementDefsByMetric(ctx context.Context, metric string) ([]AchievementDef, error)

	// ListAchievementDefs returns every achievement, enabled or not — the
	// admin page needs to see the disabled ones.
	ListAchievementDefs(ctx context.Context) ([]AchievementDef, error)

	// AchievementBySlug returns one definition, or nil if unknown. The
	// AchievementGranter's lookup.
	AchievementBySlug(ctx context.Context, slug string) (*AchievementDef, error)

	// Achievements is one member's standing on every achievement.
	Achievements(ctx context.Context, userID int64) ([]Achievement, error)

	// RecordProgress SETS progress to an absolute value (a metric read) and
	// reports whether that crossed the threshold.
	RecordProgress(ctx context.Context, achievementID, userID, value int64) (reached bool, err error)

	// IncrementProgress ADDS to progress (an event delta) and reports whether
	// that crossed the threshold. Distinct from RecordProgress because an
	// event knows only what just happened and a metric knows only the total;
	// using either where the other belongs double-counts or flatlines.
	IncrementProgress(ctx context.Context, achievementID, userID, delta int64) (reached bool, err error)

	// CompleteAchievement stamps the completion — completion ONLY, no grant.
	// paid=true also stamps paid_at in the same statement, for the two cases
	// where nothing is owed afterwards: a pure badge, and a direct
	// AchievementGranter award. Returns ErrAlreadyCompleted when the member
	// already holds it.
	CompleteAchievement(ctx context.Context, achievementID, userID int64, paid bool) error

	// MarkPaid stamps paid_at on a completed row that lacks it. Idempotent —
	// the repair sweep calls it after re-granting, and a row already stamped
	// is a no-op.
	MarkPaid(ctx context.Context, achievementID, userID int64) error

	// UnpaidCompletions returns completed rows naming a reward whose payment
	// was never recorded — the crash window between the completion commit and
	// the grant call, which the scoring job repairs. Bounded per call.
	UnpaidCompletions(ctx context.Context, limit int) ([]UnpaidCompletion, error)

	// MarkBackfilled stamps the end of an achievement's first scoring pass,
	// once.
	MarkBackfilled(ctx context.Context, achievementID int64) error

	// ProfileHidden reports whether this member has asked for their badges to
	// be left off other people's view of their profile.
	//
	// False for a member who has never chosen, which is nearly everyone:
	// earned badges are public by design and this is the opt-OUT. The read
	// sits on the profile-card render path, so "no row" must be an answer and
	// not an error — see profile_visibility (migration 003).
	ProfileHidden(ctx context.Context, userID int64) (bool, error)

	// SetProfileHidden records the choice, either way. Idempotent: setting it
	// to what it already is succeeds and changes nothing.
	SetProfileHidden(ctx context.Context, userID int64, hidden bool) error
}
