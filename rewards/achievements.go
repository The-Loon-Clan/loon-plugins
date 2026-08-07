package rewards

import (
	"context"
	"database/sql"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Achievements — the per-user read behind a host's achievements page.
//
// The DATA has always been here: an achievement is a reward carrying a payout
// of kind 'achievement', and reward_grants already records who was granted
// what and in which state. What was missing was a way to ASK, so a host could
// render the page without reading this plugin's tables directly.
//
// It is deliberately not on the Store interface. That interface is the
// engine's hot path — every method on it runs on login — and this runs once,
// when somebody opens a page. store_admin.go made the same split first.
//
// Note what this does NOT do: deliver an achievement. This plugin pays points
// and nothing else; roles, medals, achievements and username effects are the
// host's to hand over (engine.go). So the state below is the state of the
// GRANT, which is the honest thing to report — it says what the rewards engine
// decided, not what some other system did with it afterwards.

// AchievementsExtension is the registry key the read is published under.
// Absent means the plugin is not installed, which a host handles by not
// offering the page.
const AchievementsExtension = "rewards.achievements"

// AchievementState is where one member stands on one achievement.
type AchievementState string

const (
	// AchievementLocked — no grant, or one that expired unclaimed.
	AchievementLocked AchievementState = "locked"
	// AchievementPending — granted, awaiting claim or settlement.
	AchievementPending AchievementState = "pending"
	// AchievementUnlocked — granted and credited.
	AchievementUnlocked AchievementState = "unlocked"
)

// Achievement is one earnable badge and this member's standing on it.
//
// No icon or colour: this plugin owns the rules, the host owns the page. A
// presentation field here would be one the host could not override and the
// admin UI could not set.
type Achievement struct {
	// Slug is the REWARD's slug — the stable id a host can key on.
	Slug string
	Name string
	// Target is what the payout row names, i.e. which achievement it hands
	// over. Distinct from Slug because one reward may be named for the
	// occasion ("summer-2026-finisher") and pay a badge named for the deed.
	Target string
	State  AchievementState
	// EarnedAt is when the grant was made. Zero unless there is one, so a
	// caller can print "—" rather than the epoch.
	EarnedAt time.Time
}

// AchievementsFunc is the extension's type.
type AchievementsFunc func(ctx context.Context, userID int64) ([]Achievement, error)

// achievementRow is the query's shape.
type achievementRow struct {
	Slug     string         `db:"slug"`
	Name     string         `db:"name"`
	Target   string         `db:"target"`
	State    sql.NullString `db:"state"`
	EarnedAt sql.NullTime   `db:"earned_at"`
}

// Achievements lists every achievement this member could hold, with their
// standing on each.
//
// One query, not one per achievement: the page shows the whole catalogue, and
// a per-row grant lookup is how a 50-badge page becomes 51 round trips.
//
// The LATERAL takes the member's most recent grant per reward. A recurring
// reward can be granted repeatedly, and "do they have it" is answered by the
// latest one — an old expired grant must not mask a current credited one.
func (s *PGStore) Achievements(ctx context.Context, userID int64) ([]Achievement, error) {
	var rows []achievementRow
	// Disabled rewards are excluded UNLESS this member holds a grant for one.
	// A retired achievement is not something anyone can still earn, so listing
	// it as merely "locked" would be an invitation that no longer exists; but
	// someone who already earned it did earn it, and dropping it would rewrite
	// their history to make the catalogue tidy.
	err := s.sel(ctx, &rows, `
		SELECT r.slug,
		       COALESCE(NULLIF(r.name, ''), r.slug) AS name,
		       COALESCE(p.target, '')               AS target,
		       g.state,
		       g.created_at                         AS earned_at
		  FROM rewards r
		  JOIN reward_payouts p
		    ON p.reward_id = r.id AND p.kind = 'achievement'
		  LEFT JOIN LATERAL (
		       SELECT state, created_at
		         FROM reward_grants
		        WHERE reward_id = r.id AND user_id = $1
		        ORDER BY created_at DESC
		        LIMIT 1
		  ) g ON TRUE
		 WHERE r.enabled OR g.state IS NOT NULL
		 ORDER BY name, p.ordinal`, userID)
	if err != nil {
		return nil, err
	}

	out := make([]Achievement, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.achievement())
	}
	return out, nil
}

// achievement maps one row to the state a page renders. Split from the query
// so the mapping — the part with the decisions in it — is testable without a
// database, leaving the SQL to the integration tests where it belongs.
func (r achievementRow) achievement() Achievement {
	a := Achievement{Slug: r.Slug, Name: r.Name, Target: r.Target, State: AchievementLocked}
	if r.EarnedAt.Valid {
		a.EarnedAt = r.EarnedAt.Time
	}
	switch GrantState(r.State.String) {
	case StateCredited:
		a.State = AchievementUnlocked
	case StatePending:
		a.State = AchievementPending
	case StateExpired:
		// An expired grant is a badge that got away. Locked, and the date goes
		// with it — an "earned on" beside a badge they do not hold reads as a
		// bug, not as history.
		a.EarnedAt = time.Time{}
	}
	return a
}

// AchievementCounts summarises a member's standing for a statistics panel.
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

// registerAchievements publishes the read. Split out so Provision reads as a
// list of what this plugin offers rather than a wall of closures.
func (p *Plugin) registerAchievements(c *core.Core) error {
	st, ok := p.store.(*PGStore)
	if !ok {
		// A memory store backs the tests; there is no page there to serve.
		return nil
	}
	return c.Register(AchievementsExtension, AchievementsFunc(st.Achievements))
}
