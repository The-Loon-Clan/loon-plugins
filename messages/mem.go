package messages

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemStore is an in-memory Store: the demo host's backing, and what makes the
// handlers testable without a database.
//
// It reproduces the PG implementation's OBSERVABLE behaviour, not its SQL —
// canonical thread pairing, symmetric blocks, per-side soft deletes that a new
// message undoes, per-recipient read stamps, and the announcement targeting
// rules. Where the two could drift, mem_test.go pins the behaviour on this
// side and the same expectations can be run against Postgres.
//
// Names come from the Users map rather than a join, because the row shapes the
// handlers render carry the counterparty's identity inline.
type MemStore struct {
	mu sync.Mutex

	// Users maps id → username so the view rows can be built. A host or test
	// populates it; an unknown id renders as "user N", which keeps a missing
	// entry visible rather than blank.
	Users map[int]string

	threads  []*DMThread
	messages []*DMMessage
	blocks   map[[2]int]time.Time

	announcements []*Announcement
	// reads is keyed (announcement, user) — read stamp and dismissal, the
	// message_reads row.
	reads map[[2]int64]*annRead

	nextThread int64
	nextMsg    int64
	nextAnn    int64
}

type annRead struct {
	readAt    *time.Time
	dismissed bool
}

func NewMemStore() *MemStore {
	return &MemStore{
		Users:  map[int]string{},
		blocks: map[[2]int]time.Time{},
		reads:  map[[2]int64]*annRead{},
	}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) name(id int) string {
	if n, ok := m.Users[id]; ok && n != "" {
		return n
	}
	return fmt.Sprintf("user %d", id)
}

// ── Direct messages ────────────────────────────────────────────────────

func (m *MemStore) EnsureDMThread(_ context.Context, userA, userB int) (int64, bool, error) {
	if userA == userB {
		return 0, false, errors.New("dm: cannot thread with self")
	}
	lo, hi := userA, userB
	if hi < lo {
		lo, hi = hi, lo
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.threads {
		if t.UserLoID == lo && t.UserHiID == hi {
			return t.ID, false, nil
		}
	}
	m.nextThread++
	now := time.Now()
	m.threads = append(m.threads, &DMThread{
		ID: m.nextThread, UserLoID: lo, UserHiID: hi,
		LastMessageAt: now, CreatedAt: now,
	})
	return m.nextThread, true, nil
}

// IsDMBlocked is symmetric on purpose: a blocker must not be able to block
// someone and keep talking at them.
func (m *MemStore) IsDMBlocked(_ context.Context, userA, userB int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ab := m.blocks[[2]int{userA, userB}]
	_, ba := m.blocks[[2]int{userB, userA}]
	return ab || ba, nil
}

func (m *MemStore) CreateDMMessage(_ context.Context, threadID int64, senderID int, body string) (int64, error) {
	// Same refusals as the SQL store, in the same order: a handler test that
	// passes here and fails against Postgres is worse than no test.
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, errors.New("dm: empty body")
	}
	if len(body) > dmBodyMax {
		return 0, errors.New("dm: body too long (max 8000 chars)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.threadByID(threadID)
	if t == nil {
		return 0, errors.New("dm: no such thread")
	}
	now := time.Now()
	m.nextMsg++
	// read_at stays NULL, exactly as the SQL leaves it. The sender is excluded
	// from their own unread count by the sender_id predicate, not by a stamp —
	// stamping here would mark the message read for the RECIPIENT too, since
	// there is one row per message and not one per side.
	m.messages = append(m.messages, &DMMessage{
		ID: m.nextMsg, ThreadID: threadID, SenderID: senderID,
		Body: body, CreatedAt: now,
	})
	t.LastMessageAt = now
	// Clear the RECIPIENT's soft delete: a re-opened conversation must
	// reappear for them, or "delete" would read as "block" to the sender.
	if t.UserLoID == senderID {
		t.HiDeletedAt = nil
	} else {
		t.LoDeletedAt = nil
	}
	return m.nextMsg, nil
}

func (m *MemStore) ListDMThreadsForUser(_ context.Context, userID int) ([]*DMThreadView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*DMThreadView
	for _, t := range m.threads {
		if !t.has(userID) || t.deletedFor(userID) {
			continue
		}
		cp := t.other(userID)
		v := &DMThreadView{
			ThreadID: t.ID, CounterpartyID: cp,
			CounterpartyUsername: m.name(cp),
			LastMessageAt:        t.LastMessageAt,
		}
		for _, msg := range m.messages {
			if msg.ThreadID != t.ID {
				continue
			}
			if msg.CreatedAt.After(v.LastMessageAt) || v.LastMessageBody == "" {
				v.LastMessageBody, v.LastMessageSenderID = msg.Body, msg.SenderID
			}
			if msg.SenderID != userID && msg.ReadAt == nil {
				v.UnreadCount++
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastMessageAt.After(out[j].LastMessageAt) })
	return out, nil
}

// GetDMThreadForUser answers nil+nil for "no such thread" AND "not yours", so
// a 404 can never be used to probe whether a conversation exists.
func (m *MemStore) GetDMThreadForUser(_ context.Context, threadID int64, userID int) (*DMThread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.threadByID(threadID)
	if t == nil || !t.has(userID) {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *MemStore) ListDMMessagesForThread(_ context.Context, threadID int64) ([]*DMMessageView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*DMMessageView
	for _, msg := range m.messages {
		if msg.ThreadID != threadID {
			continue
		}
		out = append(out, &DMMessageView{
			ID: msg.ID, ThreadID: msg.ThreadID, SenderID: msg.SenderID,
			SenderUsername: m.name(msg.SenderID), Body: msg.Body,
			ReadAt: msg.ReadAt, CreatedAt: msg.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) MarkDMThreadRead(_ context.Context, threadID int64, viewerID int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var n int64
	for _, msg := range m.messages {
		// Only what the viewer RECEIVED: marking your own sent messages read
		// would be a no-op that still inflated the returned count.
		if msg.ThreadID == threadID && msg.SenderID != viewerID && msg.ReadAt == nil {
			r := now
			msg.ReadAt = &r
			n++
		}
	}
	return n, nil
}

func (m *MemStore) SoftDeleteDMThreadForUser(_ context.Context, threadID int64, userID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.threadByID(threadID)
	if t == nil || !t.has(userID) {
		return nil
	}
	now := time.Now()
	if t.UserLoID == userID {
		t.LoDeletedAt = &now
	} else {
		t.HiDeletedAt = &now
	}
	return nil
}

func (m *MemStore) CountUnreadDMs(_ context.Context, userID int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, msg := range m.messages {
		if msg.SenderID == userID || msg.ReadAt != nil {
			continue
		}
		t := m.threadByID(msg.ThreadID)
		if t != nil && t.has(userID) && !t.deletedFor(userID) {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) CreateDMBlock(_ context.Context, blockerID, blockedID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blocks[[2]int{blockerID, blockedID}]; !ok {
		m.blocks[[2]int{blockerID, blockedID}] = time.Now()
	}
	return nil
}

func (m *MemStore) RemoveDMBlock(_ context.Context, blockerID, blockedID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blocks, [2]int{blockerID, blockedID})
	return nil
}

func (m *MemStore) ListDMBlocks(_ context.Context, blockerID int) ([]*DMBlockView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*DMBlockView
	for k, at := range m.blocks {
		if k[0] == blockerID {
			out = append(out, &DMBlockView{
				BlockedID: k[1], BlockedUsername: m.name(k[1]), CreatedAt: at,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BlockedID < out[j].BlockedID })
	return out, nil
}

// ── Announcements ──────────────────────────────────────────────────────

func (m *MemStore) SendMessage(_ context.Context, fromName, title, body, target string, expiresAt *time.Time) (*Announcement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextAnn++
	a := &Announcement{
		ID: m.nextAnn, FromName: fromName, Title: title, Body: body,
		Target: target, ExpiresAt: expiresAt, CreatedAt: time.Now(),
	}
	m.announcements = append(m.announcements, a)
	cp := *a
	return &cp, nil
}

// visible mirrors the SQL predicate: addressed to everyone, to this user, or
// to admins when the viewer is one — and neither expired nor dismissed.
func (m *MemStore) visible(a *Announcement, userID int, isAdmin bool) bool {
	switch {
	case a.Target == "all":
	case a.Target == fmt.Sprintf("user:%d", userID):
	case isAdmin && a.Target == "admin":
	default:
		return false
	}
	if a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
		return false
	}
	if r := m.reads[[2]int64{a.ID, int64(userID)}]; r != nil && r.dismissed {
		return false
	}
	return true
}

func (m *MemStore) GetMessagesForUser(_ context.Context, userID int, isAdmin bool) ([]*Announcement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Announcement
	for _, a := range m.announcements {
		if !m.visible(a, userID, isAdmin) {
			continue
		}
		cp := *a
		if r := m.reads[[2]int64{a.ID, int64(userID)}]; r != nil {
			cp.ReadAt = r.readAt
		}
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) GetUnreadCount(_ context.Context, userID int, isAdmin bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.announcements {
		if !m.visible(a, userID, isAdmin) {
			continue
		}
		if r := m.reads[[2]int64{a.ID, int64(userID)}]; r == nil || r.readAt == nil {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) MarkMessageRead(_ context.Context, messageID int64, userID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.readRow(messageID, userID).readAt = &now
	return nil
}

func (m *MemStore) DismissMessage(_ context.Context, messageID int64, userID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readRow(messageID, userID).dismissed = true
	return nil
}

func (m *MemStore) GetAllMessages(_ context.Context, limit, offset int) ([]*Announcement, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]*Announcement, len(m.announcements))
	copy(all, m.announcements)
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	total := len(all)
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func (m *MemStore) DeleteMessage(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.announcements {
		if a.ID == id {
			m.announcements = append(m.announcements[:i], m.announcements[i+1:]...)
			break
		}
	}
	for k := range m.reads {
		if k[0] == id {
			delete(m.reads, k)
		}
	}
	return nil
}

// readRow returns the (announcement, user) row, creating it on first touch.
// Caller holds the lock.
func (m *MemStore) readRow(messageID int64, userID int) *annRead {
	k := [2]int64{messageID, int64(userID)}
	if r, ok := m.reads[k]; ok {
		return r
	}
	r := &annRead{}
	m.reads[k] = r
	return r
}

// threadByID; caller holds the lock.
func (m *MemStore) threadByID(id int64) *DMThread {
	for _, t := range m.threads {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (t *DMThread) has(userID int) bool { return t.UserLoID == userID || t.UserHiID == userID }

func (t *DMThread) other(userID int) int {
	if t.UserLoID == userID {
		return t.UserHiID
	}
	return t.UserLoID
}

func (t *DMThread) deletedFor(userID int) bool {
	if t.UserLoID == userID {
		return t.LoDeletedAt != nil
	}
	return t.HiDeletedAt != nil
}

// userTarget builds the addressed-to-one-person target string, so a caller
// never has to remember the "user:%d" convention.
func userTarget(userID int) string { return fmt.Sprintf("user:%d", userID) }

// TargetsUser reports whether a target string addresses this user directly.
// Exported for hosts composing their own announcements.
func TargetsUser(target string, userID int) bool {
	return strings.EqualFold(target, userTarget(userID))
}
