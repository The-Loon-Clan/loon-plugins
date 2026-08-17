package achievements

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// errAchievementUnknown stands in for the foreign key. A mock that invented a
// progress row for an achievement that does not exist would hide a caller
// passing a stale id — which is exactly the bug a stale id causes in
// production.
var errAchievementUnknown = errors.New("achievements: no such achievement")

// MemStore is the in-memory Store for tests.
//
// The rule this follows is the tracker MemStore's, carried over from the
// rewards double it grew out of: reproduce the invariants the schema
// enforces, because a double that is MORE PERMISSIVE than production is a
// test that passes on code Postgres rejects. Concretely, held to here:
//
//   - completing twice is ErrAlreadyCompleted, not a second completion — the
//     completed_at latch, which is now the race arbiter itself
//   - completing with NO progress row creates one, because a trigger-driven
//     achievement's first contact with a member may be the completion
//   - progress rows are per (achievement, member)
//   - paid_at only ever exists on a completed row, and stamps once
//   - RecordProgress SETS (downwards included — the counter reconciles) while
//     IncrementProgress ADDS; the old double refused to move progress
//     backwards and claimed the SQL used GREATEST, which the SQL never did —
//     the integration tests assert the reconcile, so the double now follows
//     the store rather than the belief
//
// What it deliberately does NOT claim to prove is atomicity under
// concurrency. A mutex around a Go map is not a transaction, and pretending
// otherwise is how a suite goes green on SQL that tears.
type MemStore struct {
	mu sync.Mutex

	achDefs     map[int64]AchievementDef
	achProgress map[memAchKey]*memProgress
	nextID      int64

	Now time.Time
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{
		achDefs:     map[int64]AchievementDef{},
		achProgress: map[memAchKey]*memProgress{},
		Now:         time.Now(),
	}
}

func (m *MemStore) id() int64 { m.nextID++; return m.nextID }

func (m *MemStore) now() time.Time {
	if m.Now.IsZero() {
		return time.Now()
	}
	return m.Now
}

// memProgress is one member's standing on one achievement.
type memProgress struct {
	value       int64
	times       int
	completedAt *time.Time
	paidAt      *time.Time
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
	if d.ID == 0 {
		d.ID = m.id()
	}
	m.achDefs[d.ID] = d
	return d
}

// SetBackfilled is the test-side companion to MarkBackfilled, for scenarios
// that need an achievement already past its first pass (so completions
// announce).
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

// ProgressOf exposes a member's raw progress so a test can assert on it
// without reaching through the read path (which reports state, not the
// counter).
func (m *MemStore) ProgressOf(achievementID, userID int64) (value int64, times int, completed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.achProgress[memAchKey{achievementID, userID}]
	if !ok {
		return 0, 0, false
	}
	return p.value, p.times, p.completedAt != nil
}

// Paid exposes whether paid_at is stamped, for the tests about the payment
// half of a completion.
func (m *MemStore) Paid(achievementID, userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.achProgress[memAchKey{achievementID, userID}]
	return ok && p.paidAt != nil
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
	// Stable order. The PG queries ORDER BY, and a map-ordered mock would
	// make a test that depends on evaluation order pass or fail at random.
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
	// Enabled AND disabled, matching the PG query the admin page uses.
	return m.defsWhere(func(AchievementDef) bool { return true }), nil
}

func (m *MemStore) AchievementBySlug(ctx context.Context, slug string) (*AchievementDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.achDefs {
		if d.Slug == slug {
			out := d
			return &out, nil
		}
	}
	return nil, nil
}

// RecordProgress SETS an absolute value, mirroring a metric read — downwards
// included, because the counter is the reconciling source (see the parity
// note on the type).
func (m *MemStore) RecordProgress(ctx context.Context, achievementID, userID, value int64) (bool, error) {
	return m.setProgress(achievementID, userID, value, false)
}

// IncrementProgress ADDS a delta, mirroring an event.
func (m *MemStore) IncrementProgress(ctx context.Context, achievementID, userID, delta int64) (bool, error) {
	if delta <= 0 {
		return false, nil
	}
	return m.setProgress(achievementID, userID, delta, true)
}

func (m *MemStore) setProgress(achievementID, userID, n int64, add bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.achDefs[achievementID]
	if !ok {
		// The FK would refuse this. A mock that invented a progress row for
		// an achievement that does not exist would hide a caller passing a
		// stale id.
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
	} else {
		p.value = n
	}
	// Already completed: still record progress, but do not report a fresh
	// crossing. Otherwise every later event re-reports "reached" and the
	// caller attempts a completion that can only fail. The threshold>0 guard
	// matches the SQL: a trigger-only achievement (threshold 0) must never
	// be completed by the progress paths.
	if p.completedAt != nil || d.Threshold <= 0 {
		return false, nil
	}
	return p.value >= d.Threshold, nil
}

func (m *MemStore) CompleteAchievement(ctx context.Context, achievementID, userID int64, paid bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.achDefs[achievementID]; !ok {
		return errAchievementUnknown
	}
	k := memAchKey{achievementID, userID}
	p := m.achProgress[k]
	if p == nil {
		// A trigger completion's first contact with the member — the SQL's
		// upsert inserts the row, so the mock does too.
		p = &memProgress{}
		m.achProgress[k] = p
	}
	if p.completedAt != nil {
		// The completed_at latch arbitrated: already held.
		return ErrAlreadyCompleted
	}
	now := m.now()
	p.times++
	p.completedAt = &now
	if paid {
		paidAt := now
		p.paidAt = &paidAt
	}
	return nil
}

func (m *MemStore) MarkPaid(ctx context.Context, achievementID, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.achProgress[memAchKey{achievementID, userID}]
	// Same contract as the SQL UPDATE: no row, not completed, or already
	// stamped are all zero-row no-ops rather than errors.
	if p == nil || p.completedAt == nil || p.paidAt != nil {
		return nil
	}
	now := m.now()
	p.paidAt = &now
	return nil
}

func (m *MemStore) UnpaidCompletions(ctx context.Context, limit int) ([]UnpaidCompletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UnpaidCompletion
	for k, p := range m.achProgress {
		if p.completedAt == nil || p.paidAt != nil {
			continue
		}
		d, ok := m.achDefs[k.achievementID]
		if !ok || d.RewardSlug == "" {
			// A pure badge owes nothing; same filter as the SQL.
			continue
		}
		out = append(out, UnpaidCompletion{
			AchievementID: k.achievementID, UserID: k.userID,
			Slug: d.Slug, RewardSlug: d.RewardSlug,
		})
	}
	// Deterministic order; the SQL orders by updated_at, which the mock does
	// not track per row, so it orders by identity instead.
	sort.Slice(out, func(i, j int) bool {
		if out[i].AchievementID != out[j].AchievementID {
			return out[i].AchievementID < out[j].AchievementID
		}
		return out[i].UserID < out[j].UserID
	})
	if len(out) > limit {
		out = out[:limit]
	}
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
	now := m.now()
	d.BackfilledAt = &now
	m.achDefs[achievementID] = d
	return nil
}

// Achievements is the per-member read, assembled from the same parts the PG
// query joins — including the PG query's visibility rules (hidden withheld
// until earned; disabled kept only for members who completed one), which the
// old rewards double skipped and thereby tested a looser read than production
// served.
func (m *MemStore) Achievements(ctx context.Context, userID int64) ([]Achievement, error) {
	defs, err := m.ListAchievementDefs(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Achievement, 0, len(defs))
	for _, d := range defs {
		p := m.achProgress[memAchKey{d.ID, userID}]
		completed := p != nil && p.completedAt != nil
		if !d.Enabled && !completed {
			continue
		}
		if d.Hidden && !completed {
			continue
		}
		a := Achievement{
			ID: d.ID, Slug: d.Slug, Name: d.Name, Description: d.Description,
			Metric: d.Metric, Threshold: d.Threshold, Hidden: d.Hidden,
			Icon: d.Icon, ImagePath: d.ImagePath,
			State: AchievementLocked,
		}
		if p != nil {
			a.Progress, a.Times = p.value, p.times
			if p.completedAt != nil {
				a.EarnedAt = *p.completedAt
				// Earned but not yet paid is pending; settled is unlocked.
				// Derived from paid_at rather than assumed, because a
				// completion whose granter was down sits pending until the
				// repair sweep pays it.
				a.State = AchievementUnlocked
				if p.paidAt == nil {
					a.State = AchievementPending
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}
