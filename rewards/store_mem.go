package rewards

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests.
//
// It enforces the UNIQUE (reward, user, reference) constraint itself. That is
// the one thing it MUST reproduce faithfully: the entire idempotency model
// rests on that constraint, so a double-book that a mock lets through is a
// double-book nobody notices until production.
type MemStore struct {
	mu sync.Mutex

	Rewards []Reward

	grants     []Grant
	grantLines map[int64][]Payout // grant id -> frozen lines
	baselines  map[[2]int64]int64 // (reward, user) -> where counting starts
	nextID     int64

	// The achievements half (defs + per-member progress) lived here too; it
	// moved to the achievements plugin's own MemStore with the feature.

	Now time.Time
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{
		grantLines: map[int64][]Payout{}, baselines: map[[2]int64]int64{},
		Now: time.Now(),
	}
}

// PreviousMark mirrors the SQL's GREATEST(max grant reference, baseline): a
// mock that returned only one of the two would let a test pass while
// production paid history over again.
func (m *MemStore) PreviousMark(ctx context.Context, rewardID, userID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mark := m.baselines[[2]int64{rewardID, userID}]
	for _, g := range m.grants {
		// HighWater, not Reference. They were the same field until the column
		// split, and reading the reference here now would compare a name.
		if g.RewardID == rewardID && g.UserID == userID && g.HighWater > mark {
			mark = g.HighWater
		}
	}
	return mark, nil
}

// PreviousMarks is the batch form, delegating so the two cannot disagree.
func (m *MemStore) PreviousMarks(ctx context.Context, rewardID int64, userIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(userIDs))
	for _, id := range userIDs {
		mark, err := m.PreviousMark(ctx, rewardID, id)
		if err != nil {
			return nil, err
		}
		out[id] = mark
	}
	return out, nil
}

// SetBaseline never lowers an existing one — same rule as the SQL's GREATEST.
func (m *MemStore) SetBaseline(ctx context.Context, rewardID, userID, value int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]int64{rewardID, userID}
	if cur, ok := m.baselines[k]; !ok || value > cur {
		m.baselines[k] = value
	}
	return nil
}

func (m *MemStore) id() int64 { m.nextID++; return m.nextID }

func (m *MemStore) RewardsByTrigger(ctx context.Context, trigger string) ([]Reward, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Reward
	for _, r := range m.Rewards {
		if r.Enabled && r.Trigger == trigger {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MemStore) RewardBySlug(ctx context.Context, slug string) (*Reward, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Rewards {
		if m.Rewards[i].Slug == slug {
			r := m.Rewards[i]
			return &r, nil
		}
	}
	return nil, nil
}

func (m *MemStore) RewardByID(ctx context.Context, id int64) (*Reward, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Rewards {
		if m.Rewards[i].ID == id {
			r := m.Rewards[i]
			return &r, nil
		}
	}
	return nil, nil
}

func (m *MemStore) GrantsForUser(ctx context.Context, userID int64, rewardIDs []int64) (map[int64]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]bool{}
	for _, id := range rewardIDs {
		want[id] = true
	}
	sorted := append([]Grant(nil), m.grants...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID > sorted[j].ID })
	out := map[int64]Grant{}
	for _, g := range sorted {
		if g.UserID != userID || !want[g.RewardID] {
			continue
		}
		if _, seen := out[g.RewardID]; !seen {
			out[g.RewardID] = g
		}
	}
	return out, nil
}

func (m *MemStore) CreateGrant(ctx context.Context, g Grant, payouts []Payout) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ex := range m.grants {
		if ex.RewardID == g.RewardID && ex.UserID == g.UserID && ex.Reference == g.Reference {
			return Grant{}, ErrAlreadyGranted
		}
	}
	g.ID = m.id()
	g.CreatedAt = m.Now
	lines := make([]Payout, 0, len(payouts))
	for _, p := range payouts {
		p.ID = m.id()
		lines = append(lines, p)
	}
	m.grantLines[g.ID] = lines
	g.Payouts = lines
	m.grants = append(m.grants, g)
	return g, nil
}

func (m *MemStore) GrantByID(ctx context.Context, id int64) (*Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.grants {
		if m.grants[i].ID == id {
			g := m.grants[i]
			// Same contract as the SQL join: the slug rides along for handler
			// attribution, derived from the reward rather than stored.
			for _, r := range m.Rewards {
				if r.ID == g.RewardID {
					g.RewardSlug = r.Slug
					break
				}
			}
			// Unsettled lines only — same contract as the SQL, so a resume
			// test cannot pass here and fail in production.
			var pending []Payout
			for _, p := range m.grantLines[id] {
				if p.settled.IsZero() {
					pending = append(pending, p)
				}
			}
			g.Payouts = pending
			return &g, nil
		}
	}
	return nil, nil
}

func (m *MemStore) MarkPayoutSettled(ctx context.Context, payoutID int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for gid, lines := range m.grantLines {
		for i := range lines {
			if lines[i].ID == payoutID {
				lines[i].settled = at
				m.grantLines[gid] = lines
				return nil
			}
		}
	}
	return nil
}

func (m *MemStore) SettleGrant(ctx context.Context, grantID int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.grants {
		// Same contract as the SQL: any state but credited flips, so a settle
		// that raced the expiry sweep records the payment that DID happen.
		if m.grants[i].ID == grantID && m.grants[i].State != StateCredited {
			m.grants[i].State = StateCredited
			m.grants[i].SettledAt = &at
		}
	}
	return nil
}

func (m *MemStore) ExpireGrants(ctx context.Context, now time.Time, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for i := range m.grants {
		g := &m.grants[i]
		if n >= limit {
			break
		}
		if g.State == StatePending && g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
			// Same contract as the SQL: a grant with a settled line is
			// mid-delivery and belongs to its settle, not the sweep.
			settled := false
			for _, p := range m.grantLines[g.ID] {
				if !p.settled.IsZero() {
					settled = true
					break
				}
			}
			if settled {
				continue
			}
			g.State = StateExpired
			n++
		}
	}
	return n, nil
}

func (m *MemStore) PendingGrantsFor(ctx context.Context, userID int64, limit int) ([]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Grant
	for i := len(m.grants) - 1; i >= 0 && len(out) < limit; i-- {
		g := m.grants[i]
		if g.UserID != userID || g.State != StatePending {
			continue
		}
		// ALL frozen lines, settled or not -- this is what the member is
		// being offered, not what is left to execute.
		g.Payouts = append([]Payout(nil), m.grantLines[g.ID]...)
		out = append(out, g)
	}
	return out, nil
}

// Grants exposes the raw grant list for assertions.
func (m *MemStore) Grants() []Grant {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Grant(nil), m.grants...)
}
