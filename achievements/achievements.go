package achievements

import (
	"context"
	"database/sql"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Achievements — a criterion a member can meet, with an OPTIONAL reward.
//
// This plugin used to be the back half of rewards, and the split is worth
// stating because it explains most of the shapes here. An achievement owns the
// criterion — "reach N of X", or "the moment Y happens" — and the reward
// engine owns paying, repeatability and pay-once-as-a-constraint. The two
// lived in one schema so a completion and its grant could land in one
// transaction; the day the reward became OPTIONAL, that transaction stopped
// being the design's spine. A pure badge has no grant to be atomic with, and a
// paid achievement crosses a plugin boundary where no shared transaction can
// exist. What replaced it is idempotence: the completion is this plugin's own
// atomic fact, and payment is an at-least-once call to an idempotent granter
// (pluginapi.RewardBySlugGranter). At-least-once plus idempotent equals
// exactly-once where it matters.

// ListExtension is the registry key the per-member read is published under.
// Absent means the plugin is not installed, which a host handles by not
// offering the page.
const ListExtension = "achievements.list"

// MetricSourcePrefix + a metric name is the registry key its counter is
// published under. Deliberately the same shape as rewards' UnitSourcePrefix: a
// host registering "uploads" does not need to learn a second convention. The
// prefix moved here with the plugin — a host upgrading re-registers its
// sources under achievements.metrics.* instead of rewards.metrics.*.
const MetricSourcePrefix = "achievements.metrics."

// MetricSource supplies the current value of one achievement metric.
//
// Same contract as rewards' UnitSource, and for the same reason: reading every
// member in one call is what keeps the job from doing a round trip per member
// to discover that almost nobody moved.
type MetricSource interface {
	// Values returns userID -> the counter's current value. A source may
	// return every member or only those whose count could have moved; an
	// unchanged value completes nothing either way.
	Values(ctx context.Context) (map[int64]int64, error)
}

// AchievementState is where one member stands on one achievement.
type AchievementState string

const (
	// AchievementLocked — not yet earned. Progress may still be non-zero.
	AchievementLocked AchievementState = "locked"
	// AchievementPending — earned, and the reward payment has not been
	// recorded yet (paid_at is NULL). Ordinarily a moment wide; it persists
	// when the granter is down or absent, and the scoring job's repair sweep
	// is what moves it on.
	AchievementPending AchievementState = "pending"
	// AchievementUnlocked — earned and settled: paid, or a pure badge with
	// nothing owed.
	AchievementUnlocked AchievementState = "unlocked"
)

// Achievement is one earnable achievement and this member's standing on it.
//
// The field set is identical to the one rewards used to publish, Icon and
// ImagePath included, so a host page reads the same shape through the new
// extension name.
type Achievement struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	// Metric and Threshold are the stat criterion, exposed so a page can say
	// "63 / 100" rather than only "locked". Empty metric means the
	// achievement is trigger-driven and has no progress to show.
	Metric    string
	Threshold int64
	Progress  int64
	// Times is how many times this member has completed it. 0 or 1 while
	// completion latches once; a column already, because a future recurring
	// achievement genuinely has several and the read wants to say so.
	Times  int
	State  AchievementState
	Hidden bool
	// Icon and ImagePath are the operator-chosen look. Both may be empty, in
	// which case the host draws its own state icon — the original behaviour,
	// kept as the default rather than a special case.
	Icon      string
	ImagePath string
	// EarnedAt is when the completion was recorded. Zero unless earned, so a
	// caller can print an em dash rather than the epoch.
	EarnedAt time.Time
}

// Earned reports whether this member holds the achievement, regardless of
// whether its payment has settled yet.
func (a Achievement) Earned() bool { return a.State != AchievementLocked }

// ListFunc is the extension's type.
type ListFunc func(ctx context.Context, userID int64) ([]Achievement, error)

// achievementRow is the query's shape.
type achievementRow struct {
	ID          int64         `db:"id"`
	Slug        string        `db:"slug"`
	Name        string        `db:"name"`
	Description string        `db:"description"`
	Metric      string        `db:"metric"`
	Threshold   int64         `db:"threshold"`
	Hidden      bool          `db:"hidden"`
	Icon        string        `db:"icon"`
	ImagePath   string        `db:"image_path"`
	Progress    sql.NullInt64 `db:"progress"`
	Times       sql.NullInt32 `db:"times"`
	CompletedAt sql.NullTime  `db:"completed_at"`
	PaidAt      sql.NullTime  `db:"paid_at"`
}

func (r achievementRow) achievement() Achievement {
	a := Achievement{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		Metric: r.Metric, Threshold: r.Threshold, Hidden: r.Hidden,
		Icon: r.Icon, ImagePath: r.ImagePath,
		Progress: r.Progress.Int64, Times: int(r.Times.Int32),
		State: AchievementLocked,
	}
	if r.CompletedAt.Valid {
		a.EarnedAt = r.CompletedAt.Time
		// The completion is the achievement's own fact; paid_at says whether
		// the PAYMENT landed. A member who earned something whose reward has
		// not settled has still earned it — reporting that as locked would
		// tell them they had not.
		a.State = AchievementUnlocked
		if !r.PaidAt.Valid {
			a.State = AchievementPending
		}
	}
	return a
}

// AchievementDef is an achievement as configured, without any member's
// standing. The evaluator reads these; the admin page edits them.
type AchievementDef struct {
	ID int64 `db:"id"`
	// BackfilledAt is when the first scoring pass finished. Nil means it has
	// never been scored, so the NEXT pass is the backfill and its completions
	// are silent — everyone earning it before it existed gets the badge
	// without being told about something that happened months ago.
	BackfilledAt *time.Time `db:"backfilled_at"`
	Slug         string     `db:"slug"`
	Name         string     `db:"name"`
	Description  string     `db:"description"`
	// RewardSlug names a one_off reward in the REWARDS plugin, paid through
	// pluginapi.RewardBySlugGranter with this achievement's slug as the
	// dedup reference. Empty means a pure badge — a legitimate achievement
	// that pays nothing, which is the change that made this plugin possible.
	RewardSlug string `db:"reward_slug"`
	// Metric + Threshold are the stat criterion; Trigger names a declared
	// event that completes this the moment it fires. One or the other — the
	// schema CHECK carries the rule.
	Metric    string `db:"metric"`
	Threshold int64  `db:"threshold"`
	Trigger   string `db:"trigger"`
	// Icon is a host sprite symbol; ImagePath an uploaded badge image's URL.
	// Empty means the host picks, which is what every early row did.
	Icon      string `db:"icon"`
	ImagePath string `db:"image_path"`
	Ordinal   int    `db:"ordinal"`
	Hidden    bool   `db:"hidden"`
	Enabled   bool   `db:"enabled"`
}

// AchievementCounts summarises a member's standing for a statistics panel.
//
// Anything not unlocked or pending counts as locked, INCLUDING a state this
// build does not recognise: the three numbers sit beside the list they
// describe, and one that quietly dropped a row would make the panel disagree
// with the page.
func AchievementCounts(as []Achievement) (unlocked, pending, locked int) {
	for _, a := range as {
		switch a.State {
		case AchievementUnlocked:
			unlocked++
		case AchievementPending:
			pending++
		default:
			locked++
		}
	}
	return
}

// registerList publishes the per-member read. Split out so Provision reads as
// a list of what this plugin offers rather than a wall of closures.
func (p *Plugin) registerList(c *core.Core) error {
	return c.RegisterDef(core.ExtensionDef{
		Name:    ListExtension,
		Summary: "one member's standing on every achievement: progress, state, earned-at",
		Kind:    core.ExtService,
		// Not stable YET: the shape just moved out of rewards and the state
		// vocabulary changed meaning (pending is now about paid_at rather
		// than a grant's claim state). Saying so is kinder than letting a
		// host find out.
		Stable: false,
	}, ListFunc(p.store.Achievements))
}
