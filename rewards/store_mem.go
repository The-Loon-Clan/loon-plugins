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
	Events  []Event
	Windows []Window

	grants     []Grant
	grantLines map[int64][]Payout // grant id -> frozen lines
	baselines  map[[2]int64]int64 // (reward, user) -> where counting starts
	nextID     int64

	Now time.Time
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{grantLines: map[int64][]Payout{}, baselines: map[[2]int64]int64{}, Now: time.Now()}
}

// PreviousMark mirrors the SQL's GREATEST(max grant reference, baseline): a
// mock that returned only one of the two would let a test pass while
// production paid history over again.
func (m *MemStore) PreviousMark(ctx context.Context, rewardID, userID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mark := m.baselines[[2]int64{rewardID, userID}]
	for _, g := range m.grants {
		if g.RewardID == rewardID && g.UserID == userID && g.Reference > mark {
			mark = g.Reference
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

// OpenWindowsFor mirrors the SQL's half-open comparison exactly: starts_at <=
// at < ends_at. A mock that used <= on both ends would let a boundary-instant
// test pass while production granted twice.
func (m *MemStore) OpenWindowsFor(ctx context.Context, eventIDs []int64, at time.Time) (map[int64]Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]bool{}
	for _, id := range eventIDs {
		want[id] = true
	}
	out := map[int64]Window{}
	for _, w := range m.Windows {
		if !want[w.EventID] {
			continue
		}
		if !w.StartsAt.After(at) && w.EndsAt.After(at) {
			if cur, seen := out[w.EventID]; !seen || w.StartsAt.After(cur.StartsAt) {
				out[w.EventID] = w
			}
		}
	}
	return out, nil
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
		if m.grants[i].ID == grantID && m.grants[i].State == StatePending {
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
			g.State = StateExpired
			n++
		}
	}
	return n, nil
}

func (m *MemStore) EventsWithCron(ctx context.Context) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.Events {
		if e.Enabled && e.Cron != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemStore) LastWindowEnd(ctx context.Context, eventID int64) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, w := range m.Windows {
		if w.EventID == eventID && w.EndsAt.After(latest) {
			latest = w.EndsAt
		}
	}
	return latest, nil
}

func (m *MemStore) InsertWindows(ctx context.Context, ws []Window) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, w := range ws {
		var dup bool
		for _, ex := range m.Windows {
			if ex.EventID == w.EventID && ex.StartsAt.Equal(w.StartsAt) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		w.ID = m.id()
		m.Windows = append(m.Windows, w)
		n++
	}
	return n, nil
}

// Grants exposes the raw grant list for assertions.
func (m *MemStore) Grants() []Grant {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Grant(nil), m.grants...)
}
