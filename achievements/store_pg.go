package achievements

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// PGStore is the production Store.
//
// It holds the *core.SchemaDB, not the raw pool. Every statement below is
// unqualified, and the ONLY thing that makes `achievements` mean
// `achievements.achievements` is running inside SchemaDB.WithTx, which issues
// SET LOCAL search_path for that transaction. Unwrapping with .DB() yields a
// connection with no such scoping and every query then resolves against
// public — which is exactly what once shipped in rewards, because the test
// harness applied the schema unqualified and so had no separation to expose
// it.
type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// sel / get / exec are the scoped equivalents of sqlx's SelectContext,
// GetContext and ExecContext. They exist so that reaching for an unscoped
// connection is not something a caller can do by accident: there is no
// s.db.Select to reach for.
func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

func (s *PGStore) get(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, dest, q, args...) })
}

func (s *PGStore) exec(ctx context.Context, q string, args ...any) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// uniqueViolation is Postgres 23505. Checked structurally rather than by
// message text, which is localised and version-dependent.
func uniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// defCols is the definition column list, one declaration so the three def
// queries cannot drift on which columns a def needs.
const defCols = `id, slug, name, description, reward_slug, metric, threshold,
	trigger, icon, image_path, title_slug, description_slug,
	ordinal, hidden, enabled, backfilled_at`

// AchievementDefsByTrigger returns the enabled achievements a declared event
// completes. Empty trigger matches nothing useful — trigger-less rows are the
// metric job's set, which no event announces.
func (s *PGStore) AchievementDefsByTrigger(ctx context.Context, trigger string) ([]AchievementDef, error) {
	var defs []AchievementDef
	err := s.sel(ctx, &defs, `
		SELECT `+defCols+`
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
		SELECT `+defCols+`
		  FROM achievements
		 WHERE enabled AND metric = $1
		 ORDER BY threshold`, metric)
	return defs, err
}

// ListAchievementDefs returns every achievement, enabled or not — the admin
// page needs to see the disabled ones.
func (s *PGStore) ListAchievementDefs(ctx context.Context) ([]AchievementDef, error) {
	var defs []AchievementDef
	err := s.sel(ctx, &defs, `
		SELECT `+defCols+`
		  FROM achievements
		 ORDER BY ordinal, name`)
	return defs, err
}

// AchievementBySlug returns one definition, or nil if unknown.
func (s *PGStore) AchievementBySlug(ctx context.Context, slug string) (*AchievementDef, error) {
	var d AchievementDef
	err := s.get(ctx, &d, `
		SELECT `+defCols+`
		  FROM achievements
		 WHERE slug = $1`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("achievement by slug: %w", err)
	}
	return &d, nil
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
		       a.icon, a.image_path, a.title_slug, a.description_slug,
		       ua.progress, ua.times, ua.completed_at, ua.paid_at
		  FROM achievements a
		  LEFT JOIN user_achievements ua
		         ON ua.achievement_id = a.id AND ua.user_id = $1
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

// RecordProgress stores a member's current value for an achievement WITHOUT
// completing it, and reports whether the threshold is now met.
//
// Separate from completion on purpose: most evaluations move a number and
// nothing else, and that path must not open a completion or touch the payment
// machinery. Progress on an already-completed achievement is still recorded —
// the page says "100 / 100", and for a future recurring achievement the count
// keeps climbing toward the next completion.
//
// SET means SET, downwards included: the counter is the reconciling source,
// and replacing rather than maxing is what lets a purge or a recount correct
// drifted progress instead of compounding it. (Un-earning is not at stake —
// completed_at latches regardless of where the number goes afterwards.)
func (s *PGStore) RecordProgress(ctx context.Context, achievementID, userID, value int64) (reached bool, err error) {
	err = s.get(ctx, &reached, `
		INSERT INTO user_achievements (achievement_id, user_id, progress, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (achievement_id, user_id) DO UPDATE
		   SET progress = EXCLUDED.progress, updated_at = now()
		RETURNING (
		    SELECT $3 >= a.threshold AND a.threshold > 0
		           AND user_achievements.completed_at IS NULL
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
		    SELECT user_achievements.progress >= a.threshold AND a.threshold > 0
		           AND user_achievements.completed_at IS NULL
		      FROM achievements a WHERE a.id = $1
		)`, achievementID, userID, delta)
	return reached, err
}

// CompleteAchievement records a completion. Completion ONLY — no grant.
//
// The old CompleteAchievement wrote the completion and its reward grant in one
// transaction, and its comment took care to say that the engine's UNIQUE
// (reward_id, user_id, reference) was what actually arbitrated a race, with
// the conditional update kept only as an early-out. That division of labour is
// gone with the shared schema: there is no grant here to be unique about, so
// the `completed_at IS NULL` condition below IS the race arbiter now. One
// statement, atomic in itself — two evaluations racing both reach this upsert,
// exactly one finds completed_at NULL, and the loser gets no row back and is
// told ErrAlreadyCompleted.
//
// An upsert rather than a bare UPDATE because a trigger-driven achievement
// completes with no progress row to update: the declared event IS the
// criterion, so first contact with the member may be the completion itself.
//
// paid=true stamps paid_at in the same statement, for the two callers with
// nothing owed afterwards: a pure badge (no reward_slug), and a direct
// AchievementGranter award (which deliberately runs no reward).
func (s *PGStore) CompleteAchievement(ctx context.Context, achievementID, userID int64, paid bool) error {
	var id int64
	err := s.get(ctx, &id, `
		INSERT INTO user_achievements (achievement_id, user_id, progress, times, completed_at, paid_at, updated_at)
		VALUES ($1, $2, 0, 1, now(), CASE WHEN $3 THEN now() END, now())
		ON CONFLICT (achievement_id, user_id) DO UPDATE
		   SET times = user_achievements.times + 1,
		       completed_at = now(),
		       paid_at = CASE WHEN $3 THEN now() ELSE user_achievements.paid_at END,
		       updated_at = now()
		 WHERE user_achievements.completed_at IS NULL
		RETURNING achievement_id`, achievementID, userID, paid)
	if errors.Is(err, sql.ErrNoRows) {
		// Somebody else completed it first, or it was already done. The
		// condition arbitrated and the answer is "no".
		return ErrAlreadyCompleted
	}
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	return nil
}

// MarkPaid stamps paid_at on a completed row.
//
// Idempotent by construction: the WHERE makes a second stamp a no-op, and a
// row that is not completed cannot be paid — payment is a property of a
// completion, never of bare progress.
func (s *PGStore) MarkPaid(ctx context.Context, achievementID, userID int64) error {
	_, err := s.exec(ctx, `
		UPDATE user_achievements
		   SET paid_at = now(), updated_at = now()
		 WHERE achievement_id = $1 AND user_id = $2
		   AND completed_at IS NOT NULL AND paid_at IS NULL`, achievementID, userID)
	return err
}

// UnpaidCompletions returns completed rows naming a reward whose payment was
// never recorded.
//
// This is the crash window the idempotence design accepts on purpose: a
// process that died between the completion commit and the GrantOneOff call
// leaves exactly this shape behind, and the scoring job repairs it by calling
// the granter again — idempotent, so a payment that DID land and merely lost
// its stamp is not paid twice. Bounded per call; the next tick picks up the
// remainder.
//
// Rows with an empty reward_slug are not here and never will be: a pure badge
// stamps paid_at at completion, and an operator who blanks a reward_slug
// afterwards has said nothing further is owed.
func (s *PGStore) UnpaidCompletions(ctx context.Context, limit int) ([]UnpaidCompletion, error) {
	var rows []UnpaidCompletion
	err := s.sel(ctx, &rows, `
		SELECT ua.achievement_id, ua.user_id, a.slug, a.reward_slug
		  FROM user_achievements ua
		  JOIN achievements a ON a.id = ua.achievement_id
		 WHERE ua.completed_at IS NOT NULL AND ua.paid_at IS NULL
		   AND a.reward_slug <> ''
		 ORDER BY ua.updated_at
		 LIMIT $1`, limit)
	return rows, err
}

// MarkBackfilled stamps the end of an achievement's first scoring pass.
//
// Everything completed before this ran was earned before the achievement
// existed and was awarded silently; everything after is announced normally.
// Stamped once — the WHERE clause makes a second pass a no-op rather than
// resetting the mark and re-silencing a later cohort.
func (s *PGStore) MarkBackfilled(ctx context.Context, achievementID int64) error {
	_, err := s.exec(ctx, `
		UPDATE achievements SET backfilled_at = now()
		 WHERE id = $1 AND backfilled_at IS NULL`, achievementID)
	return err
}

// ── Definition CRUD ─────────────────────────────────────────────────────────
//
// The point of these over raw SQL is the VALIDATION — the admin form refuses
// with a sentence, and the store refuses independently so the form is a better
// message rather than the guarantee. What is deliberately NOT validated any
// more is the reward: reward_slug names a row in another plugin's tables, and
// reading them from here is the coupling this split removed. An unpayable slug
// surfaces lazily — in the scoring job's log, and as paid_at staying NULL.

// NewAchievement is the input to CreateAchievement. Separate from
// AchievementDef because an id and a backfill mark are outputs, not inputs,
// and a create form that offers them invites somebody to set them.
type NewAchievement struct {
	Slug        string
	Name        string
	Description string
	// RewardSlug is optional. Empty = a pure badge.
	RewardSlug string
	// Metric+Threshold OR Trigger — the criterion, one shape or the other.
	Metric    string
	Threshold int64
	Trigger   string
	Ordinal   int
	Hidden    bool
	// The look. Icon is a sprite symbol name; ImagePath the uploaded badge's
	// URL. Optional — empty defers to the host's state icons.
	Icon      string
	ImagePath string
	// Localization slugs (optional). When set, a host with a message
	// catalogue resolves these per viewer locale; Name/Description are the
	// fallback either way.
	TitleSlug       string
	DescriptionSlug string
}

// CreateAchievement validates and inserts one, DISABLED.
//
// Disabled on purpose, and for a sharper reason than a reward's draft state.
// Creating a metric achievement backfills it to everyone already past the
// threshold on the next job tick — so an agent able to create AND enable in
// one step could hand a badge to the whole membership from a single tool
// call. Enabling is a separate, deliberate act.
func (s *PGStore) CreateAchievement(ctx context.Context, a NewAchievement) (int64, error) {
	a.Slug = strings.TrimSpace(a.Slug)
	a.Name = strings.TrimSpace(a.Name)
	a.Metric = strings.TrimSpace(a.Metric)
	a.Trigger = strings.TrimSpace(a.Trigger)
	a.RewardSlug = strings.TrimSpace(a.RewardSlug)

	switch {
	case a.Slug == "":
		return 0, fmt.Errorf("slug is required")
	case a.Name == "":
		return 0, fmt.Errorf("name is required; a catalogue of achievement-3 helps nobody")
	case a.Metric == "" && a.Trigger == "":
		return 0, fmt.Errorf("a criterion is required: a metric with a threshold, or a trigger event")
	case a.Metric != "" && a.Threshold <= 0:
		return 0, fmt.Errorf("threshold must be positive")
	}
	// The schema CHECK carries the same rule — one is the message, the other
	// is the guarantee.

	var id int64
	err := s.get(ctx, &id, `
		INSERT INTO achievements
		    (slug, name, description, reward_slug, metric, threshold, trigger, icon, image_path,
		     title_slug, description_slug, ordinal, hidden, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, FALSE)
		RETURNING id`,
		a.Slug, a.Name, a.Description, a.RewardSlug, a.Metric, a.Threshold,
		a.Trigger, a.Icon, a.ImagePath, a.TitleSlug, a.DescriptionSlug, a.Ordinal, a.Hidden)
	if uniqueViolation(err) {
		return 0, fmt.Errorf("an achievement with slug %q already exists", a.Slug)
	}
	return id, err
}

// SetAchievementEnabled turns one on or off.
//
// Enabling is the moment the backfill happens, so it is deliberately its own
// call rather than a flag on create. The first scoring pass afterwards awards
// everyone already past the threshold — silently, see MarkBackfilled — and
// nothing about that is undoable.
func (s *PGStore) SetAchievementEnabled(ctx context.Context, id int64, on bool) error {
	n, err := s.exec(ctx, `UPDATE achievements SET enabled = $2 WHERE id = $1`, id, on)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("achievement %d does not exist", id)
	}
	return nil
}
