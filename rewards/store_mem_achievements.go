package rewards

import (
	"context"
	"errors"
	"sort"
	"time"
)

// errAchievementUnknown stands in for the foreign key. A mock that invented a
// progress row for an achievement that does not exist would hide a caller
// passing a stale id — which is exactly the bug a stale id causes in production.
var errAchievementUnknown = errors.New("rewards: no such achievement")

// The MemStore's achievements half.
//
// These methods existed only on *PGStore, which meant the evaluation path was
// reachable solely through a type assertion and every test of it needed a
// database — including the ones about pure logic, like which achievements a
// trigger selects or whether a completion should be announced. A real database
// does not save you writing the fixture data; it just makes writing it slower.
//
// The rule this follows is the tracker MemStore's: reproduce the invariants the
// schema enforces, because a double that is MORE PERMISSIVE than production is a
// test that passes on code Postgres rejects. Concretely, held to here:
//
//   - a completion needs an existing progress row (the UPDATE ... WHERE
//     completed_at IS NULL that returns no rows otherwise)
//   - completing twice is ErrAlreadyGranted, not a second grant
//   - completed_at and grant_id arrive together, never one alone (the CHECK)
//   - progress rows are per (achievement, member)
//
// What it deliberately does NOT claim to prove is atomicity under concurrency.
// A mutex around a Go map is not a transaction, and pretending otherwise is how a
// suite goes green on SQL that tears. That property stays with
// achievements_pg_test.go, which tests it by racing two goroutines at a real
// UNIQUE constraint. The two are complementary: the mock covers the logic
// exhaustively and fast, the database covers what only a database can answer.

// memProgress is one member's standing on one achievement.
type memProgress struct {
	value       int64
	times       int
	completedAt *time.Time
	grantID     *int64
}

type memAchKey struct {
	achievementID int64
	userID        int64
}

// SeedAchievement registers a definition for tests. Exported because a test
// building a scenario is the only caller — there is no admin CRUD on the mock.
func (m *MemStore) SeedAchievement(d AchievementDef) AchievementDef {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.achDefs == nil {
		m.achDefs = map[int64]AchievementDef{}
	}
	if d.ID == 0 {
		d.ID = m.id()
	}
	m.achDefs[d.ID] = d
	return d
}

// SetBackfilled is the test-side companion to MarkBackfilled, for scenarios that
// need an achievement already past its first pass (so completions announce).
func (m *MemStore) SetBackfilled(achievementID int64, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.achDefs[achievementID]
	if !ok {
		return
	}
	d.BackfilledAt = &at
	m.achDefs[achievementID] = d
}

// ProgressOf exposes a member's raw progress so a test can assert on it without
// reaching through the read path (which reports state, not the counter).
func (m *MemStore) ProgressOf(achievementID, userID int64) (value int64, times int, completed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.achProgress[memAchKey{achievementID, userID}]
	if !ok {
		return 0, 0, false
	}
	return p.value, p.times, p.completedAt != nil
}

func (m *MemStore) defsWhere(match func(AchievementDef) bool) []AchievementDef {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AchievementDef
	for _, d := range m.achDefs {
		if match(d) {
			out = append(out, d)
		}
	}
	// Stable order. The PG queries ORDER BY, and a map-ordered mock would make
	// a test that depends on evaluation order pass or fail at random.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal != out[j].Ordinal {
			return out[i].Ordinal < out[j].Ordinal
		}
		if out[i].Threshold != out[j].Threshold {
			return out[i].Threshold < out[j].Threshold
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

func (m *MemStore) AchievementDefsByTrigger(ctx context.Context, trigger string) ([]AchievementDef, error) {
	return m.defsWhere(func(d AchievementDef) bool {
		return d.Enabled && d.Trigger == trigger
	}), nil
}

func (m *MemStore) AchievementDefsByMetric(ctx context.Context, metric string) ([]AchievementDef, error) {
	return m.defsWhere(func(d AchievementDef) bool {
		return d.Enabled && d.Metric == metric
	}), nil
}

func (m *MemStore) ListAchievementDefs(ctx context.Context) ([]AchievementDef, error) {
	// Enabled AND disabled, matching the PG query the admin page and validator use.
	return m.defsWhere(func(AchievementDef) bool { return true }), nil
}

// RecordProgress SETS an absolute value, mirroring a metric read.
//
// Never moves progress BACKWARDS: a counter that dips (a deleted upload) must not
// un-earn what it already reached, and the PG version's GREATEST does the same.
func (m *MemStore) RecordProgress(ctx context.Context, achievementID, userID, value int64) (bool, error) {
	return m.setProgress(achievementID, userID, value, false)
}

// IncrementProgress ADDS a delta, mirroring an event.
func (m *MemStore) IncrementProgress(ctx context.Context, achievementID, userID, delta int64) (bool, error) {
	return m.setProgress(achievementID, userID, delta, true)
}

func (m *MemStore) setProgress(achievementID, userID, n int64, add bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.achProgress == nil {
		m.achProgress = map[memAchKey]*memProgress{}
	}
	d, ok := m.achDefs[achievementID]
	if !ok {
		// The FK would refuse this. A mock that invented a progress row for an
		// achievement that does not exist would hide a caller passing a stale id.
		return false, errAchievementUnknown
	}
	k := memAchKey{achievementID, userID}
	p := m.achProgress[k]
	if p == nil {
		p = &memProgress{}
		m.achProgress[k] = p
	}
	if add {
		p.value += n
	} else if n > p.value {
		p.value = n
	}
	// Already completed: still record progress, but do not report a fresh
	// crossing. Otherwise every later event re-reports "reached" and the caller
	// attempts a completion that can only fail.
	if p.completedAt != nil {
		return false, nil
	}
	return p.value >= d.Threshold, nil
}

func (m *MemStore) CompleteAchievement(ctx context.Context, achievementID int64, g Grant, payouts []Payout) (Grant, error) {
	m.mu.Lock()
	p := m.achProgress[memAchKey{achievementID, g.UserID}]
	if p == nil || p.completedAt != nil {
		// No progress row, or already completed. Both mean "not ours to
		// complete" — the same two cases the SQL's RETURNING-no-rows collapses.
		m.mu.Unlock()
		return Grant{}, ErrAlreadyGranted
	}
	m.mu.Unlock()

	// The grant goes through the normal path so the UNIQUE (reward, user,
	// reference) the engine relies on is the same one arbitrating here. If it
	// refuses, the completion must NOT be stamped — that is the half-state the
	// schema's CHECK exists to make impossible.
	out, err := m.CreateGrant(ctx, g, payouts)
	if err != nil {
		return Grant{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now
	if now.IsZero() {
		now = time.Now()
	}
	p.times++
	p.completedAt = &now
	id := out.ID
	p.grantID = &id
	return out, nil
}

func (m *MemStore) MarkBackfilled(ctx context.Context, achievementID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.achDefs[achievementID]
	if !ok {
		return errAchievementUnknown
	}
	if d.BackfilledAt != nil {
		// Stamped once. The PG version's WHERE backfilled_at IS NULL makes a
		// second pass a no-op rather than re-silencing a later cohort.
		return nil
	}
	now := m.Now
	if now.IsZero() {
		now = time.Now()
	}
	d.BackfilledAt = &now
	m.achDefs[achievementID] = d
	return nil
}

// Achievements is the per-member read, assembled from the same parts the PG
// LATERAL query joins.
func (m *MemStore) Achievements(ctx context.Context, userID int64) ([]Achievement, error) {
	defs, err := m.ListAchievementDefs(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Achievement, 0, len(defs))
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		a := Achievement{
			ID: d.ID, Slug: d.Slug, Name: d.Name, Description: d.Description,
			Metric: d.Metric, Threshold: d.Threshold, Hidden: d.Hidden,
			State: AchievementLocked,
		}
		if p := m.achProgress[memAchKey{d.ID, userID}]; p != nil {
			a.Progress, a.Times = p.value, p.times
			if p.completedAt != nil {
				a.EarnedAt = *p.completedAt
				// Earned but not yet paid is pending; paid is unlocked. Derived
				// from the grant rather than assumed, because a claim-delivery
				// reward sits pending until the member claims it.
				a.State = AchievementUnlocked
				if p.grantID != nil {
					for _, g := range m.grants {
						if g.ID == *p.grantID && g.State == StatePending {
							a.State = AchievementPending
						}
					}
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}
