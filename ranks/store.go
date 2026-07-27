package ranks

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// The groups model, owned by this plugin (ENTITLEMENTS.md Stage 2). These
// types deliberately duplicate nothing from the host: the plugin no longer
// imports pkg/models or pkg/storage, which is what lets it lift to
// loon-plugins once Stage 3 frees its last host dependency.

// Group is one catalog row. Grants holds the entitlement keys this group
// confers directly; a parent's keys are inherited at resolution time rather
// than copied in, so editing a parent takes effect everywhere at once.
type Group struct {
	ID           int
	Slug         string
	Name         string
	Kind         string // paid | earned | assigned
	Visible      bool
	ParentID     *int
	Depth        int
	Color        string // bootstrap badge class
	TitleColor   string // hex, username colour
	Icon         string
	CostPoints   int
	DurationDays int
	SortOrder    int
	CreatedAt    time.Time
	Grants       map[string]int64
}

// Member is one membership. ExpiresAt nil means permanent — only reachable
// for non-paid groups, since the legacy mirror's column is NOT NULL.
type Member struct {
	UserID    int
	GroupID   int
	GrantedAt time.Time
	ExpiresAt *time.Time
	Source    string
}

// HistoryEntry is one membership-history row, resolved against the catalog.
// GroupName / GroupSlug are empty when the group has since been deleted:
// group_member_history's FK is ON DELETE SET NULL, because an audit trail that
// vanished with the thing it audited would be worthless.
type HistoryEntry struct {
	At        time.Time
	Action    string
	GroupName string
	GroupSlug string
	Details   string
}

// Entitlement keys this plugin grants. They mirror the host's catalog in
// pkg/models/entitlement.go, which stays the authority for the site; the
// duplication is deliberate, because a plugin that imports host models cannot
// be lifted. Keep the two in step until the keys move into a shared contract.
const (
	entDownloadDaily = "download.daily"
	entAPIDaily      = "api.daily"
	entDMInitiate    = "dm.initiate"
)

// What the admin catalog shows for a group that grants no quota of its own.
// These existed for the legacy mirror's NOT NULL columns; that mirror is gone,
// but the form still needs a number to put in the box, and it should be the
// one the site actually applies. Mirrors the host's DailyDownloadLimit /
// DailyAPIRequestLimit — a duplication that only disappears when the plugin
// leaves the tree and the keys move into a shared contract.
const (
	defaultDownloadDaily = 100
	defaultAPIDaily      = 10000
)

// ErrGroupNotFound is returned by Group and by writes against a missing id.
var ErrGroupNotFound = errors.New("ranks: group not found")

// ErrParentCycle rejects a re-parent that would make a group its own ancestor.
var ErrParentCycle = errors.New("ranks: a group cannot be nested under itself or its own descendant")

// ErrParentTooDeep rejects a chain past the depth limit.
var ErrParentTooDeep = errors.New("ranks: group nesting is limited to 4 levels")

// maxGroupDepth mirrors the CHECK in the migration: depth 0..3, four levels.
const maxGroupDepth = 3

// defaultHistoryLimit is what MemberHistory uses when the caller passes none.
// 50 matches what the host's admin page has always shown.
const defaultHistoryLimit = 50

// limit reads one numeric grant, falling back to def.
func (g *Group) limit(key string, def int64) int64 {
	if v, ok := g.Grants[key]; ok {
		return v
	}
	return def
}

// Store is the plugin's data surface. The groups tables are the only copy:
// the legacy user_ranks mirror and the Reconcile pass that repaired it are
// gone, along with the last host reader (ENTITLEMENTS.md Stage 3.4).
type Store interface {
	// Groups returns the catalog, sort_order then id.
	Groups(ctx context.Context) ([]Group, error)
	// Group returns one row, or ErrGroupNotFound.
	Group(ctx context.Context, id int) (*Group, error)
	// CreateGroup inserts and writes the assigned id back into g.
	CreateGroup(ctx context.Context, g *Group) error
	// UpdateGroup overwrites by id, including the group's own grants.
	UpdateGroup(ctx context.Context, g *Group) error
	// DeleteGroup removes the group; memberships cascade.
	DeleteGroup(ctx context.Context, id int) error
	// SetParent re-parents a group (nil detaches it). Rejects a cycle and a
	// chain deeper than the depth limit, and recomputes the subtree's depths.
	SetParent(ctx context.Context, id int, parentID *int) error

	// ActiveMembership returns the user's highest-sort_order unexpired
	// membership, or nil when they hold none.
	ActiveMembership(ctx context.Context, userID int) (*Member, error)
	// AddMember joins userID to groupID for dur, extending an existing
	// membership from whichever is later: its current expiry or now.
	AddMember(ctx context.Context, userID, groupID int, dur time.Duration) error
	// ExpireMemberships removes every lapsed membership and returns them.
	// Permanent memberships (NULL expiry) are never swept.
	ExpireMemberships(ctx context.Context) ([]Member, error)

	// MembersOfGroups returns the live memberships of the given groups. Used
	// by the entitlement sync, which has to fan out to a group's members when
	// what the group confers changes.
	MembersOfGroups(ctx context.Context, groupIDs []int) ([]Member, error)

	// MembershipsOfUsers returns the live memberships of the given users. The
	// display capability resolves badges by USER, where the entitlement sync
	// fans out by group — different access patterns, so both exist.
	MembershipsOfUsers(ctx context.Context, userIDs []int) ([]Member, error)

	// BadgeData is MembershipsOfUsers + Groups for the display capability,
	// deliberately as ONE method rather than two calls.
	//
	// It exists for cost, not convenience. Every PGStore read opens a
	// transaction so it can `SET LOCAL search_path` (loon's SchemaDB has no
	// other way to reach the plugin schema), so composing badges from the two
	// separate readers costs two BEGIN/SET LOCAL/COMMIT round trips on top of
	// the two SELECTs. Worse, Groups also loads every group's entitlement
	// values, which a Badge never carries. Since Stage 3.4 puts this on a
	// release-page render — one call resolving every comment author — that
	// overhead is the difference between a defensible read and a regression
	// against the single legacy join it replaces.
	//
	// The returned groups therefore have EMPTY Grants: this is the display
	// path, and anything needing what a group confers must use Groups.
	BadgeData(ctx context.Context, userIDs []int) (members []Member, catalog []Group, err error)

	// MemberCounts returns live (unexpired) membership counts by group id,
	// for the admin catalog. One query, not one per row.
	MemberCounts(ctx context.Context) (map[int]int, error)

	// RecordHistory appends one audit row. Best-effort at the call sites.
	RecordHistory(ctx context.Context, userID int, groupID *int, action, details string) error

	// MemberHistory returns a user's audit rows, newest first, at most limit
	// of them (limit <= 0 means the implementation default). Serves the
	// GroupAudit capability, which is what the host's admin page reads.
	MemberHistory(ctx context.Context, userID, limit int) ([]HistoryEntry, error)
}

// MemStore is the in-memory Store for unit tests. It models the groups side
// only: the legacy mirror is a property of the SQL implementation, so
// dual-write is covered by the integration tests rather than restated here
// against a fake that could only agree with itself.
type MemStore struct {
	mu      sync.Mutex
	groups  map[int]*Group
	members map[[2]int]*Member
	history []HistoryEntry
	nextID  int
	now     func() time.Time
}

func NewMemStore() *MemStore {
	return &MemStore{
		groups:  map[int]*Group{},
		members: map[[2]int]*Member{},
		nextID:  1,
		now:     time.Now,
	}
}

var _ Store = (*MemStore)(nil)

// SetClock is the test-only knob for moving past an expiry without sleeping.
func (m *MemStore) SetClock(fn func() time.Time) {
	m.mu.Lock()
	m.now = fn
	m.mu.Unlock()
}

func cloneGroup(g *Group) Group {
	out := *g
	out.Grants = make(map[string]int64, len(g.Grants))
	for k, v := range g.Grants {
		out.Grants[k] = v
	}
	if g.ParentID != nil {
		p := *g.ParentID
		out.ParentID = &p
	}
	return out
}

func (m *MemStore) Groups(context.Context) ([]Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Group, 0, len(m.groups))
	for _, g := range m.groups {
		out = append(out, cloneGroup(g))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemStore) Group(_ context.Context, id int) (*Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	c := cloneGroup(g)
	return &c, nil
}

func (m *MemStore) CreateGroup(_ context.Context, g *Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g.ID = m.nextID
	m.nextID++
	g.CreatedAt = m.now()
	c := cloneGroup(g)
	m.groups[g.ID] = &c
	return nil
}

func (m *MemStore) UpdateGroup(_ context.Context, g *Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.groups[g.ID]; !ok {
		return ErrGroupNotFound
	}
	c := cloneGroup(g)
	m.groups[g.ID] = &c
	return nil
}

func (m *MemStore) DeleteGroup(_ context.Context, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, id)
	for k := range m.members {
		if k[1] == id {
			delete(m.members, k)
		}
	}
	return nil
}

func (m *MemStore) SetParent(_ context.Context, id int, parentID *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[id]
	if !ok {
		return ErrGroupNotFound
	}
	if parentID == nil {
		g.ParentID, g.Depth = nil, 0
		m.redepth(id, 0)
		return nil
	}
	if *parentID == id {
		return ErrParentCycle
	}
	p, ok := m.groups[*parentID]
	if !ok {
		return ErrGroupNotFound
	}
	// Walking UP from the proposed parent is what catches the cycle: if the
	// group appears among its own would-be ancestors, the link closes a loop.
	for cur := p; cur != nil; {
		if cur.ID == id {
			return ErrParentCycle
		}
		if cur.ParentID == nil {
			break
		}
		cur = m.groups[*cur.ParentID]
	}
	if p.Depth+1 > maxGroupDepth || p.Depth+1+m.subtreeHeight(id) > maxGroupDepth {
		return ErrParentTooDeep
	}
	pid := *parentID
	g.ParentID, g.Depth = &pid, p.Depth+1
	m.redepth(id, g.Depth)
	return nil
}

// redepth re-stamps a moved subtree. Callers hold the lock.
func (m *MemStore) redepth(id, depth int) {
	for _, c := range m.groups {
		if c.ParentID != nil && *c.ParentID == id {
			c.Depth = depth + 1
			m.redepth(c.ID, c.Depth)
		}
	}
}

// subtreeHeight is how many levels hang below id, so a move can be rejected
// before it pushes a descendant past the limit.
func (m *MemStore) subtreeHeight(id int) int {
	h := 0
	for _, c := range m.groups {
		if c.ParentID != nil && *c.ParentID == id {
			if ch := 1 + m.subtreeHeight(c.ID); ch > h {
				h = ch
			}
		}
	}
	return h
}

func (m *MemStore) ActiveMembership(_ context.Context, userID int) (*Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var best *Member
	var bestSort int
	for k, mem := range m.members {
		if k[0] != userID {
			continue
		}
		if mem.ExpiresAt != nil && !mem.ExpiresAt.After(now) {
			continue
		}
		g, ok := m.groups[mem.GroupID]
		if !ok {
			continue
		}
		if best == nil || g.SortOrder > bestSort {
			cp := *mem
			best, bestSort = &cp, g.SortOrder
		}
	}
	return best, nil
}

func (m *MemStore) AddMember(_ context.Context, userID, groupID int, dur time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.groups[groupID]; !ok {
		return ErrGroupNotFound
	}
	now := m.now()
	base := now
	cur, existing := m.members[[2]int{userID, groupID}]
	if existing {
		// A permanent membership (NULL expiry — an assigned/staff grant) stays
		// permanent no matter what is granted on top. PGStore.AddMember encodes
		// the same rule with a CASE, because Postgres GREATEST ignores NULLs;
		// getting it wrong here would teach every future unit test the opposite
		// semantics for paid access.
		if cur.ExpiresAt == nil {
			return nil
		}
		if cur.ExpiresAt.After(now) {
			base = *cur.ExpiresAt
		}
	}
	exp := base.Add(dur)
	m.members[[2]int{userID, groupID}] = &Member{
		UserID: userID, GroupID: groupID, GrantedAt: now, ExpiresAt: &exp, Source: "purchase",
	}
	return nil
}

func (m *MemStore) ExpireMemberships(context.Context) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []Member
	for k, mem := range m.members {
		if mem.ExpiresAt == nil || mem.ExpiresAt.After(now) {
			continue
		}
		out = append(out, *mem)
		delete(m.members, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (m *MemStore) MembersOfGroups(_ context.Context, groupIDs []int) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int]bool{}
	for _, id := range groupIDs {
		want[id] = true
	}
	now := m.now()
	var out []Member
	for _, mem := range m.members {
		if !want[mem.GroupID] {
			continue
		}
		if mem.ExpiresAt != nil && !mem.ExpiresAt.After(now) {
			continue
		}
		cp := *mem
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out, nil
}

func (m *MemStore) MembershipsOfUsers(_ context.Context, userIDs []int) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int]bool{}
	for _, id := range userIDs {
		want[id] = true
	}
	now := m.now()
	var out []Member
	for _, mem := range m.members {
		if !want[mem.UserID] {
			continue
		}
		if mem.ExpiresAt != nil && !mem.ExpiresAt.After(now) {
			continue
		}
		cp := *mem
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out, nil
}

// BadgeData composes the two readers, since in memory there is no transaction
// to save. It still blanks Grants: a double that hands back more than
// production does lets a consumer depend on data the real store withholds.
func (m *MemStore) BadgeData(ctx context.Context, userIDs []int) ([]Member, []Group, error) {
	members, err := m.MembershipsOfUsers(ctx, userIDs)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := m.Groups(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range catalog {
		catalog[i].Grants = map[string]int64{}
	}
	return members, catalog, nil
}

func (m *MemStore) MemberCounts(context.Context) (map[int]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := map[int]int{}
	for _, mem := range m.members {
		if mem.ExpiresAt == nil || mem.ExpiresAt.After(now) {
			out[mem.GroupID]++
		}
	}
	return out, nil
}

// RecordHistory keeps the whole row. It used to store the action alone, which
// was harmless while nothing read it back; MemberHistory reads it now, so a
// double that drops the group and details would let a consumer look correct
// against data production never returns.
func (m *MemStore) RecordHistory(_ context.Context, userID int, groupID *int, action, details string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := HistoryEntry{At: m.now(), Action: action, Details: details}
	if groupID != nil {
		if g, ok := m.groups[*groupID]; ok {
			e.GroupName, e.GroupSlug = g.Name, g.Slug
		}
	}
	m.history = append(m.history, e)
	return nil
}

// MemberHistory returns newest-first, mirroring the SQL's ORDER BY. The mock
// keeps one flat slice rather than indexing by user, which is fine at test
// sizes but means the filter happens here.
func (m *MemStore) MemberHistory(_ context.Context, userID, limit int) ([]HistoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	out := []HistoryEntry{}
	for i := len(m.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.history[i])
	}
	return out, nil
}
