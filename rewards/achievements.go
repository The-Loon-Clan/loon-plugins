package rewards

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Achievements — a criterion attached to a reward.
//
// The split is the design. The engine already owns definitions,
// repeatability, triggers, jobs, callbacks and pay-once-as-a-constraint; the
// one thing it cannot express is "reach N of X", because per_unit counts a
// number and pays per delta with no threshold that latches. So an achievement
// owns the criterion and delegates paying to the reward it names.
//
// What that buys, concretely: an achievement cannot pay twice even if two
// evaluations race, because the payment goes through reward_grants and its
// UNIQUE (reward_id, user_id, reference) arbitrates. No new locking, no new
// idempotency scheme.
//
// This replaced a placeholder that defined an achievement as "a reward
// carrying a payout of kind achievement". Nothing could award one under that
// reading — there was no criterion to satisfy — and no progress could be
// shown, which is most of what an achievements page is for.

// AchievementsExtension is the registry key the per-member read is published
// under. Absent means the plugin is not installed, which a host handles by not
// offering the page.
const AchievementsExtension = "rewards.achievements"

// MetricSourcePrefix + a metric name is the registry key its counter is
// published under. Deliberately the same shape as UnitSourcePrefix: a host
// registering "uploads" does not need to learn a second convention.
const MetricSourcePrefix = "rewards.metrics."

// MetricSource supplies the current value of one achievement metric.
//
// Same contract as UnitSource, and for the same reason: reading every member
// in one call is what keeps the job from doing a round trip per member to
// discover that almost nobody moved.
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
	// AchievementPending — earned, and the grant is awaiting claim or
	// settlement. Only reachable for a reward with delivery='claim'.
	AchievementPending AchievementState = "pending"
	// AchievementUnlocked — earned and paid.
	AchievementUnlocked AchievementState = "unlocked"
)

// Achievement is one earnable achievement and this member's standing on it.
//
// No icon or colour: this plugin owns the rules, the host owns the page. A
// presentation field here would be one the site could not override and the
// admin UI could not set.
type Achievement struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	// Metric and Threshold are the criterion, exposed so a page can say
	// "63 / 100" rather than only "locked".
	Metric    string
	Threshold int64
	Progress  int64
	// Times is how many times this member has completed it. 0 or 1 while
	// achievements are restricted to one_off rewards.
	Times  int
	State  AchievementState
	Hidden bool
	// EarnedAt is when the completion was recorded. Zero unless earned, so a
	// caller can print an em dash rather than the epoch.
	EarnedAt time.Time
}

// Earned reports whether this member holds the achievement, regardless of
// whether its payment has settled yet.
func (a Achievement) Earned() bool { return a.State != AchievementLocked }

// AchievementsFunc is the extension's type.
type AchievementsFunc func(ctx context.Context, userID int64) ([]Achievement, error)

// achievementRow is the query's shape.
type achievementRow struct {
	ID          int64          `db:"id"`
	Slug        string         `db:"slug"`
	Name        string         `db:"name"`
	Description string         `db:"description"`
	Metric      string         `db:"metric"`
	Threshold   int64          `db:"threshold"`
	Hidden      bool           `db:"hidden"`
	Progress    sql.NullInt64  `db:"progress"`
	Times       sql.NullInt32  `db:"times"`
	CompletedAt sql.NullTime   `db:"completed_at"`
	GrantState  sql.NullString `db:"grant_state"`
}

func (r achievementRow) achievement() Achievement {
	a := Achievement{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		Metric: r.Metric, Threshold: r.Threshold, Hidden: r.Hidden,
		Progress: r.Progress.Int64, Times: int(r.Times.Int32),
		State: AchievementLocked,
	}
	if r.CompletedAt.Valid {
		a.EarnedAt = r.CompletedAt.Time
		// The completion is the achievement's own fact; the grant's state says
		// whether the PAYMENT landed. A member who earned something and has
		// not claimed it has earned it — reporting that as locked would tell
		// them they had not.
		a.State = AchievementUnlocked
		if r.GrantState.String == string(StatePending) {
			a.State = AchievementPending
		}
	}
	return a
}

// Achievements lists what this member can earn and where they stand.
//
// One query, not one per achievement: the page shows the whole catalogue, and
// a per-row progress lookup is how a 50-badge page becomes 51 round trips.
//
// Hidden achievements are withheld until earned — that is what makes them
// secret. Disabled ones are excluded UNLESS this member already completed one:
// a retired achievement is not something anyone can still earn, so listing it
// as merely locked would be an invitation that no longer exists, but dropping
// it from the shelf of someone who did earn it rewrites their history to make
// the catalogue tidy.
func (s *PGStore) Achievements(ctx context.Context, userID int64) ([]Achievement, error) {
	var rows []achievementRow
	err := s.sel(ctx, &rows, `
		SELECT a.id, a.slug, a.name, a.description, a.metric, a.threshold, a.hidden,
		       ua.progress, ua.times, ua.completed_at,
		       g.state AS grant_state
		  FROM achievements a
		  LEFT JOIN user_achievements ua
		         ON ua.achievement_id = a.id AND ua.user_id = $1
		  LEFT JOIN reward_grants g ON g.id = ua.grant_id
		 WHERE (a.enabled OR ua.completed_at IS NOT NULL)
		   AND (NOT a.hidden OR ua.completed_at IS NOT NULL)
		 ORDER BY a.ordinal, a.name`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Achievement, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.achievement())
	}
	return out, nil
}

// AchievementDef is an achievement as configured, without any member's
// standing. The evaluator reads these; the admin page edits them.
type AchievementDef struct {
	ID          int64  `db:"id"`
	Slug        string `db:"slug"`
	Name        string `db:"name"`
	Description string `db:"description"`
	RewardID    int64  `db:"reward_id"`
	Metric      string `db:"metric"`
	Threshold   int64  `db:"threshold"`
	Trigger     string `db:"trigger"`
	Ordinal     int    `db:"ordinal"`
	Hidden      bool   `db:"hidden"`
	Enabled     bool   `db:"enabled"`
}

// AchievementDefsByTrigger returns the enabled achievements a surface can
// move. Empty trigger returns those with no trigger set — the job's set,
// which no surface announces.
func (s *PGStore) AchievementDefsByTrigger(ctx context.Context, trigger string) ([]AchievementDef, error) {
	var defs []AchievementDef
	err := s.sel(ctx, &defs, `
		SELECT id, slug, name, description, reward_id, metric, threshold,
		       trigger, ordinal, hidden, enabled
		  FROM achievements
		 WHERE enabled AND trigger = $1
		 ORDER BY ordinal, name`, trigger)
	return defs, err
}

// AchievementDefsByMetric returns every enabled achievement scored by one
// metric, so the job evaluates a metric's whole set from one counter read.
func (s *PGStore) AchievementDefsByMetric(ctx context.Context, metric string) ([]AchievementDef, error) {
	var defs []AchievementDef
	err := s.sel(ctx, &defs, `
		SELECT id, slug, name, description, reward_id, metric, threshold,
		       trigger, ordinal, hidden, enabled
		  FROM achievements
		 WHERE enabled AND metric = $1
		 ORDER BY threshold`, metric)
	return defs, err
}

// ListAchievementDefs returns every achievement, enabled or not — the admin
// page and the validator both need to see the disabled ones.
func (s *PGStore) ListAchievementDefs(ctx context.Context) ([]AchievementDef, error) {
	var defs []AchievementDef
	err := s.sel(ctx, &defs, `
		SELECT id, slug, name, description, reward_id, metric, threshold,
		       trigger, ordinal, hidden, enabled
		  FROM achievements
		 ORDER BY ordinal, name`)
	return defs, err
}

// metricNames is the set of metrics a source is registered for, which the
// validator needs to tell "scored by something" from "inert".
func (p *Plugin) metricNames() map[string]bool {
	out := make(map[string]bool, len(p.metrics))
	for name := range p.metrics {
		out[name] = true
	}
	return out
}

// RecordProgress stores a member's current value for an achievement WITHOUT
// completing it, and reports whether the threshold is now met.
//
// Separate from completion on purpose: most evaluations move a number and
// nothing else, and that path must not open a transaction or touch the grant
// tables. Progress on an already-completed achievement is still recorded — the
// page says "100 / 100", and for a future recurring achievement the count
// keeps climbing toward the next completion.
func (s *PGStore) RecordProgress(ctx context.Context, achievementID, userID, value int64) (reached bool, err error) {
	err = s.get(ctx, &reached, `
		INSERT INTO user_achievements (achievement_id, user_id, progress, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (achievement_id, user_id) DO UPDATE
		   SET progress = EXCLUDED.progress, updated_at = now()
		RETURNING (
		    SELECT $3 >= a.threshold AND user_achievements.completed_at IS NULL
		      FROM achievements a WHERE a.id = $1
		)`, achievementID, userID, value)
	return reached, err
}

// IncrementProgress adds to a member's progress and reports whether the
// threshold is now met.
//
// Separate from RecordProgress, which SETS an absolute value, because the two
// sources of progress are different in kind. An event says "one more happened"
// and can only add; a MetricSource says "the total is 613" and can only set.
// Collapsing them would mean an event handler had to read the total first —
// a round trip per event on a hot path, and a race with every other event for
// the same member.
//
// The reached flag is computed in the same statement as the write, so it
// cannot be answered from a value that has already moved.
func (s *PGStore) IncrementProgress(ctx context.Context, achievementID, userID, delta int64) (reached bool, err error) {
	if delta <= 0 {
		return false, nil
	}
	err = s.get(ctx, &reached, `
		INSERT INTO user_achievements (achievement_id, user_id, progress, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (achievement_id, user_id) DO UPDATE
		   SET progress = user_achievements.progress + $3, updated_at = now()
		RETURNING (
		    SELECT user_achievements.progress >= a.threshold
		           AND user_achievements.completed_at IS NULL
		      FROM achievements a WHERE a.id = $1
		)`, achievementID, userID, delta)
	return reached, err
}

// CompleteAchievement records a completion and creates its reward grant in ONE
// transaction.
//
// This is the operator's requirement and the reason it is not two writes.
// Neither half may happen alone: a completion with no grant is an achievement
// that paid nothing, and a grant with no completion pays again on the next
// evaluation because nothing records that it already fired. The schema carries
// the same rule as a CHECK, so a future writer that sets one without the other
// is rejected rather than merely wrong.
//
// What actually arbitrates a race is the engine's
// UNIQUE (reward_id, user_id, reference) — verified by deleting the
// `completed_at IS NULL` clause below and re-running the race test, which
// still passed: the loser's grant insert violates the constraint and the whole
// transaction rolls back, leaving the completion unwritten.
//
// The conditional is kept anyway, as an early-out rather than the guarantee:
// it costs nothing and stops the common repeat case before it touches the
// grant tables. Saying which of the two is load-bearing matters, because a
// comment claiming this clause is the arbiter would invite someone to keep it
// and drop the constraint.
func (s *PGStore) CompleteAchievement(ctx context.Context, achievementID int64, g Grant, payouts []Payout) (Grant, error) {
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var uaID int64
		err := tx.QueryRowContext(ctx, `
			UPDATE user_achievements
			   SET times = times + 1, updated_at = now()
			 WHERE achievement_id = $1 AND user_id = $2 AND completed_at IS NULL
			RETURNING achievement_id`, achievementID, g.UserID).Scan(&uaID)
		if err == sql.ErrNoRows {
			// Either no progress row yet, or somebody else completed it first.
			// Both mean "not ours to complete", and the caller treats them the
			// same way it treats a duplicate grant.
			return ErrAlreadyGranted
		}
		if err != nil {
			return fmt.Errorf("mark completed: %w", err)
		}

		if err := insertGrantTx(ctx, tx, &g, payouts); err != nil {
			return err
		}

		// Only now can grant_id be set — the CHECK requires completed_at and
		// grant_id to arrive together, so this statement carries both.
		if _, err := tx.ExecContext(ctx, `
			UPDATE user_achievements
			   SET completed_at = now(), grant_id = $3
			 WHERE achievement_id = $1 AND user_id = $2`,
			achievementID, g.UserID, g.ID); err != nil {
			return fmt.Errorf("link grant: %w", err)
		}
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	return g, nil
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

// registerAchievements publishes the per-member read. Split out so Provision
// reads as a list of what this plugin offers rather than a wall of closures.
func (p *Plugin) registerAchievements(c *core.Core) error {
	st, ok := p.store.(*PGStore)
	if !ok {
		// A memory store backs the tests; there is no page there to serve.
		return nil
	}
	return c.RegisterDef(core.ExtensionDef{
		Name:    AchievementsExtension,
		Summary: "one member's standing on every achievement: progress, state, earned-at",
		Kind:    core.ExtService,
		// Not stable: the trigger path and the job that complete an
		// achievement are not built, so the states this can return are not
		// yet the full set. Saying so is kinder than letting a host find out.
		Stable: false,
	}, AchievementsFunc(st.Achievements))
}
