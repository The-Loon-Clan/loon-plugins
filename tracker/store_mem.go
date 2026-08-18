package tracker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// MemStore is the in-memory Store, for tests and for a host with no database.
//
// It reproduces the invariants the schema enforces, because a double that is more
// permissive than production is a test that passes on code Postgres rejects. The
// ones that matter here:
//
//   - passkey is UNIQUE across members, so an announce can always say who it is
//     from. Storing a duplicate is refused rather than silently accepted.
//   - rotated_at is stamped only when the passkey actually CHANGES, matching the
//     CASE in the upsert.
//   - ApplyAnnounceDelta ADDS to uploaded/downloaded and REPLACES left_bytes, and
//     `completed` is sticky — the three behaviours the announce path depends on.
//   - deleting a torrent takes its stats (the ON DELETE CASCADE, by hand).
type MemStore struct {
	mu       sync.RWMutex
	torrents map[string]*Torrent
	stats    map[statKey]*UserStat
	passkeys map[int64]string

	// Now is injectable so a test can place last_seen relative to the activity
	// windows Totals uses, rather than racing the wall clock.
	Now time.Time
}

type statKey struct {
	userID   int64
	infoHash string
}

func NewMemStore() *MemStore {
	return &MemStore{
		torrents: map[string]*Torrent{},
		stats:    map[statKey]*UserStat{},
		passkeys: map[int64]string{},
	}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) now() time.Time {
	if !m.Now.IsZero() {
		return m.Now
	}
	return time.Now()
}

// ── Passkeys ────────────────────────────────────────────────────────────────

func (m *MemStore) UserByPasskey(ctx context.Context, passkey string) (int64, bool, error) {
	if passkey == "" {
		return 0, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for uid, pk := range m.passkeys {
		if pk == passkey {
			return uid, true, nil
		}
	}
	return 0, false, nil
}

func (m *MemStore) SetPasskey(ctx context.Context, userID int64, passkey string) error {
	if passkey == "" {
		return errors.New("tracker: refusing to store an empty passkey")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// The UNIQUE constraint, by hand. Two members sharing a passkey would make an
	// announce ambiguous about who it is from, and a double that allows it lets a
	// test pass on code the database refuses.
	for uid, pk := range m.passkeys {
		if pk == passkey && uid != userID {
			return errors.New("tracker: passkey already belongs to another member")
		}
	}
	m.passkeys[userID] = passkey
	return nil
}

func (m *MemStore) Passkey(ctx context.Context, userID int64) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pk, ok := m.passkeys[userID]
	return pk, ok, nil
}

// ── The catalogue ───────────────────────────────────────────────────────────

func (m *MemStore) UpsertTorrent(ctx context.Context, t *Torrent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.torrents[t.InfoHash]; ok {
		// Matches the ON CONFLICT: the name is refreshed, provenance is filled in
		// only when absent, and info_bytes is never rewritten — the info_hash IS
		// its hash, so a conflict is the same torrent by definition.
		existing.Name = t.Name
		if existing.UploadedBy == nil {
			existing.UploadedBy = t.UploadedBy
		}
		if existing.NzbID == nil {
			existing.NzbID = t.NzbID
		}
		return nil
	}
	cp := *t
	if cp.AddedAt.IsZero() {
		cp.AddedAt = m.now()
	}
	m.torrents[cp.InfoHash] = &cp
	return nil
}

func (m *MemStore) TorrentByNzbID(ctx context.Context, nzbID int64) (*Torrent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var newest *Torrent
	for _, t := range m.torrents {
		if t.NzbID == nil || *t.NzbID != nzbID {
			continue
		}
		// Newest wins, matching the PG store's ORDER BY added_at DESC — a map
		// has no order, so picking the first match would make this store
		// disagree with the real one depending on iteration.
		if newest == nil || t.AddedAt.After(newest.AddedAt) {
			newest = t
		}
	}
	if newest == nil {
		return nil, nil
	}
	cp := *newest
	return &cp, nil
}

func (m *MemStore) TorrentsByNzbIDs(ctx context.Context, nzbIDs []int64) (map[int64]*Torrent, error) {
	out := map[int64]*Torrent{}
	if len(nzbIDs) == 0 {
		return out, nil
	}
	want := make(map[int64]bool, len(nzbIDs))
	for _, id := range nzbIDs {
		want[id] = true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, t := range m.torrents {
		if t.NzbID == nil || !want[*t.NzbID] {
			continue
		}
		// Newest wins, for the reason on TorrentByNzbID: the two lookups must
		// agree, and a map has no order to fall back on.
		if cur, ok := out[*t.NzbID]; ok && !t.AddedAt.After(cur.AddedAt) {
			continue
		}
		cp := *t
		out[*t.NzbID] = &cp
	}
	return out, nil
}

func (m *MemStore) Torrent(ctx context.Context, infoHash string) (*Torrent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.torrents[infoHash]
	if !ok {
		// nil, nil — absence is not an error, matching the PG store. An announce
		// for an unknown hash is an ordinary event the caller answers with a
		// bencoded failure.
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *MemStore) ListTorrents(ctx context.Context, limit, offset int) ([]*Torrent, int, error) {
	if limit <= 0 {
		limit = 100
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Torrent, 0, len(m.torrents))
	for _, t := range m.torrents {
		cp := *t
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.After(out[j].AddedAt) })
	total := len(out)
	if offset >= len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// ── The announce path ───────────────────────────────────────────────────────

func (m *MemStore) ApplyAnnounceDelta(ctx context.Context, userID int64, infoHash string,
	upDelta, downDelta, left int64, completed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.torrents[infoHash]; !ok {
		// The foreign key, by hand: PG would reject a stat row for an unknown
		// torrent, so the double must too.
		return errors.New("tracker: no such torrent")
	}
	k := statKey{userID, infoHash}
	s, ok := m.stats[k]
	if !ok {
		s = &UserStat{UserID: userID, InfoHash: infoHash}
		m.stats[k] = s
	}
	// ADD the byte deltas, REPLACE left_bytes, and make completed sticky — the
	// three behaviours the SQL's ON CONFLICT encodes and the announce path relies
	// on. Getting `completed` wrong here would let a re-announce undo a snatch.
	s.Uploaded += upDelta
	s.Downloaded += downDelta
	s.LeftBytes = left
	s.Completed = s.Completed || completed
	s.LastSeen = m.now()
	return nil
}

func (m *MemStore) IncrementSnatches(ctx context.Context, infoHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.torrents[infoHash]; ok {
		t.Snatches++
	}
	return nil
}

func (m *MemStore) SetSwarmCounts(ctx context.Context, infoHash string, seeders, leechers int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.torrents[infoHash]; ok {
		t.Seeders, t.Leechers = seeders, leechers
	}
	return nil
}

// ── Reads ───────────────────────────────────────────────────────────────────

// activeWindow mirrors the `last_seen > now() - interval '1 hour'` in the PG
// reads. One constant so the two cannot disagree about what "active" means.
const activeWindow = time.Hour

func (m *MemStore) Totals(ctx context.Context, userID int64) (Totals, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var t Totals
	cutoff := m.now().Add(-activeWindow)
	for k, s := range m.stats {
		if k.userID != userID {
			continue
		}
		t.Uploaded += s.Uploaded
		t.Downloaded += s.Downloaded
		if s.Completed {
			t.Snatched++
		}
		if s.LastSeen.After(cutoff) {
			if s.LeftBytes == 0 {
				t.Seeding++
			} else {
				t.Leeching++
			}
		}
	}
	return t, nil
}

func (m *MemStore) ListUserStats(ctx context.Context, userID int64, limit int) ([]*UserStat, error) {
	if limit <= 0 {
		limit = 100
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*UserStat
	for k, s := range m.stats {
		if k.userID != userID {
			continue
		}
		cp := *s
		// The join the PG read does in one round trip.
		if t, ok := m.torrents[k.infoHash]; ok {
			cp.Name, cp.TSize = t.Name, t.Size
		}
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListAggregates(ctx context.Context, sortBy string, limit, offset int) ([]*Aggregate, int, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cutoff := m.now().Add(-activeWindow)
	byUser := map[int64]*Aggregate{}
	for k, s := range m.stats {
		a, ok := byUser[k.userID]
		if !ok {
			a = &Aggregate{UserID: k.userID}
			byUser[k.userID] = a
		}
		a.Uploaded += s.Uploaded
		a.Downloaded += s.Downloaded
		a.TorrentCount++
		if s.Completed {
			a.SnatchedCount++
		}
		if s.LastSeen.After(cutoff) {
			a.ActiveCount++
		}
		if a.LastSeen == nil || s.LastSeen.After(*a.LastSeen) {
			seen := s.LastSeen
			a.LastSeen = &seen
		}
	}
	out := make([]*Aggregate, 0, len(byUser))
	for _, a := range byUser {
		out = append(out, a)
	}
	// Same allowlist semantics as the PG store, including the fallback: an unknown
	// sort shows a table rather than failing, because a mistyped URL should not be
	// an error page.
	switch sortBy {
	case "uploaded":
		sort.Slice(out, func(i, j int) bool { return out[i].Uploaded > out[j].Uploaded })
	case "downloaded":
		sort.Slice(out, func(i, j int) bool { return out[i].Downloaded > out[j].Downloaded })
	case "torrents":
		sort.Slice(out, func(i, j int) bool { return out[i].TorrentCount > out[j].TorrentCount })
	default:
		sort.Slice(out, func(i, j int) bool {
			if out[i].LastSeen == nil {
				return false
			}
			if out[j].LastSeen == nil {
				return true
			}
			return out[i].LastSeen.After(*out[j].LastSeen)
		})
	}
	total := len(out)
	if offset >= len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}
