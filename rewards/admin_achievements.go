package rewards

import (
	"context"
	"fmt"
	"strings"
)

// Creating and editing definitions, for the admin page and the ops API.
//
// The point of these over raw SQL is the VALIDATION. Every mistake made while
// creating the site's first two achievements by hand is checked here and
// returned as a refusal an operator can read:
//
//   - a reward with no payout lines, which pays nothing while looking healthy
//     and, on the achievement path, writes an irreversible completion;
//   - a metric no source declares, which silently never moves;
//   - a metric that is an event but not a counter, so a threshold has nothing
//     to count;
//   - a reward of the wrong kind, which would pay per unit forever against a
//     criterion that latches once.
//
// All four were live in production at some point tonight. None of them raised
// an error anywhere; three were caught by the configuration validator after
// the fact and one by a test.

// NewAchievement is the input to CreateAchievement. Separate from
// AchievementDef because an id and a backfill mark are outputs, not inputs,
// and a create form that offers them invites somebody to set them.
type NewAchievement struct {
	Slug        string
	Name        string
	Description string
	RewardID    int64
	Metric      string
	Threshold   int64
	Trigger     string
	Ordinal     int
	Hidden      bool
}

// CreateAchievement validates and inserts one, DISABLED.
//
// Disabled on purpose, and for a sharper reason than the reward equivalent.
// Creating an achievement backfills it to everyone already past the threshold
// on the next job tick — so an agent able to create AND enable in one step
// could hand a badge to the whole membership from a single tool call. Enabling
// is a separate, deliberate act.
func (s *PGStore) CreateAchievement(ctx context.Context, a NewAchievement) (int64, error) {
	a.Slug = strings.TrimSpace(a.Slug)
	a.Name = strings.TrimSpace(a.Name)
	a.Metric = strings.TrimSpace(a.Metric)

	switch {
	case a.Slug == "":
		return 0, fmt.Errorf("slug is required")
	case a.Name == "":
		return 0, fmt.Errorf("name is required; a catalogue of achievement-3 helps nobody")
	case a.Threshold <= 0:
		return 0, fmt.Errorf("threshold must be positive")
	case a.Metric == "":
		return 0, fmt.Errorf("metric is required; without one nothing can ever score it")
	}

	// The reward has to be able to pay.
	r, err := s.RewardByID(ctx, a.RewardID)
	if err != nil {
		return 0, err
	}
	switch {
	case r == nil:
		return 0, fmt.Errorf("reward %d does not exist", a.RewardID)
	case !r.Enabled:
		return 0, fmt.Errorf("reward %q is disabled, so the achievement could be earned but not paid", r.Slug)
	case r.Kind != KindOneOff:
		return 0, fmt.Errorf("reward %q is %s; achievements need one_off, because a criterion "+
			"latches once and the other kinds keep paying", r.Slug, r.Kind)
	case len(r.Payouts) == 0:
		// The one that cost most: a completion is irreversible, so a member
		// would hold a badge that paid nothing and could never re-earn it.
		return 0, fmt.Errorf("reward %q has no payout lines, so it hands over nothing", r.Slug)
	}

	// The metric has to be something that exists and counts.
	cat, err := s.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	var src *SourceDef
	for i := range cat {
		if cat[i].Key == a.Metric {
			src = &cat[i]
			break
		}
	}
	switch {
	case src == nil:
		return 0, fmt.Errorf("metric %q is not in the source catalogue; pick a declared one "+
			"or add it first, because an undeclared metric never moves and says nothing", a.Metric)
	case !src.Counts:
		return 0, fmt.Errorf("source %q fires but does not count, so a threshold has nothing "+
			"to count — pick a counter", a.Metric)
	}

	var id int64
	err = s.get(ctx, &id, `
		INSERT INTO achievements
		    (slug, name, description, reward_id, metric, threshold, trigger, ordinal, hidden, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, FALSE)
		RETURNING id`,
		a.Slug, a.Name, a.Description, a.RewardID, a.Metric, a.Threshold,
		a.Trigger, a.Ordinal, a.Hidden)
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

// UpsertSource adds a catalogue entry or edits one in place.
//
// Keyed on the key, which is also what rewards.trigger and achievements.metric
// store — so this cannot rename, only create or edit. Renaming would orphan
// everything pointing at the old key, silently, which is why the table makes
// key its primary key rather than something renameable.
func (s *PGStore) UpsertSource(ctx context.Context, d SourceDef) error {
	if err := d.Valid(); err != nil {
		return err
	}
	_, err := s.exec(ctx, `
		INSERT INTO reward_sources (key, label, grp, fires, counts, unit, units, ordinal, stock)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, FALSE)
		ON CONFLICT (key) DO UPDATE
		   SET label = EXCLUDED.label, grp = EXCLUDED.grp,
		       fires = EXCLUDED.fires, counts = EXCLUDED.counts,
		       unit  = EXCLUDED.unit,  units  = EXCLUDED.units,
		       ordinal = EXCLUDED.ordinal`,
		d.Key, d.Label, d.Group, d.Fires, d.Counts, d.Unit, d.Units, 0)
	return err
}

// SetSourceEnabled turns a catalogue entry on or off.
//
// Disabling hides it from the pickers without deleting it, which is what an
// operator wants for a source this site does not have: deleting would orphan
// any reward or achievement already pointing at it.
func (s *PGStore) SetSourceEnabled(ctx context.Context, key string, on bool) error {
	n, err := s.exec(ctx, `UPDATE reward_sources SET enabled = $2 WHERE key = $1`, key, on)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("source %q does not exist", key)
	}
	return nil
}
