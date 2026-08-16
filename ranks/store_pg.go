package ranks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// PGStore is the production Store over the plugin's own Postgres schema.
//
// Every query runs inside SchemaDB.WithTx, which sets
// `SET LOCAL search_path = "ranks", public` — that is what makes the
// unqualified table names below resolve to the plugin's schema.
//
// The groups tables are the ONLY copy now. Until ENTITLEMENTS.md Stage 3.4
// finished, every mutation also mirrored into the legacy public.user_ranks
// trio in the same transaction, because host components still read them; the
// last of those readers moved to the GroupDisplay / GroupAudit capabilities,
// the host's rank repository was deleted, and the mirror — along with the
// Reconcile pass that repaired it after a rolling deploy — went with them.
type PGStore struct {
	db *core.SchemaDB
}

// NewPGStore builds the store over the plugin's schema. It used to take the
// legacy schema name too, so the dual-write could be pointed at a scratch copy
// in tests; with the mirror gone there is nothing outside this schema to name.
func NewPGStore(db *core.SchemaDB) *PGStore {
	return &PGStore{db: db}
}

var _ Store = (*PGStore)(nil)

const groupCols = `id, slug, name, kind, visible, parent_id, depth, color,
                   title_color, icon, cost_points, duration_days, sort_order, created_at,
                   min_uploaded, min_ratio, min_age_days`

type groupRow struct {
	ID           int           `db:"id"`
	Slug         string        `db:"slug"`
	Name         string        `db:"name"`
	Kind         string        `db:"kind"`
	Visible      bool          `db:"visible"`
	ParentID     sql.NullInt64 `db:"parent_id"`
	Depth        int           `db:"depth"`
	Color        string        `db:"color"`
	TitleColor   string        `db:"title_color"`
	Icon         string        `db:"icon"`
	CostPoints   int           `db:"cost_points"`
	DurationDays int           `db:"duration_days"`
	SortOrder    int           `db:"sort_order"`
	CreatedAt    time.Time     `db:"created_at"`
	MinUploaded  int64         `db:"min_uploaded"`
	MinRatio     float64       `db:"min_ratio"`
	MinAgeDays   int           `db:"min_age_days"`
}

func (r groupRow) toGroup() Group {
	g := Group{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Kind: r.Kind, Visible: r.Visible,
		Depth: r.Depth, Color: r.Color, TitleColor: r.TitleColor, Icon: r.Icon,
		CostPoints: r.CostPoints, DurationDays: r.DurationDays, SortOrder: r.SortOrder,
		CreatedAt: r.CreatedAt, Grants: map[string]int64{},
		MinUploaded: r.MinUploaded, MinRatio: r.MinRatio, MinAgeDays: r.MinAgeDays,
	}
	if r.ParentID.Valid {
		p := int(r.ParentID.Int64)
		g.ParentID = &p
	}
	return g
}

// loadGrants fills the Grants map for the given groups in one query, rather
// than a query per group — the catalog page renders all of them.
func loadGrants(ctx context.Context, tx *sqlx.Tx, groups map[int]*Group) error {
	if len(groups) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT group_id, key, val FROM group_entitlements`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gid int
		var key string
		var val int64
		if err := rows.Scan(&gid, &key, &val); err != nil {
			return err
		}
		if g, ok := groups[gid]; ok {
			g.Grants[key] = val
		}
	}
	return rows.Err()
}

func (s *PGStore) Groups(ctx context.Context) ([]Group, error) {
	var out []Group
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var rows []groupRow
		if err := tx.SelectContext(ctx, &rows, `SELECT `+groupCols+` FROM groups ORDER BY sort_order, id`); err != nil {
			return err
		}
		byID := make(map[int]*Group, len(rows))
		out = make([]Group, len(rows))
		for i, r := range rows {
			out[i] = r.toGroup()
			byID[r.ID] = &out[i]
		}
		return loadGrants(ctx, tx, byID)
	})
	return out, err
}

func (s *PGStore) Group(ctx context.Context, id int) (*Group, error) {
	var g *Group
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var r groupRow
		if err := tx.GetContext(ctx, &r, `SELECT `+groupCols+` FROM groups WHERE id = $1`, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrGroupNotFound
			}
			return err
		}
		out := r.toGroup()
		g = &out
		return loadGrants(ctx, tx, map[int]*Group{out.ID: g})
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *PGStore) CreateGroup(ctx context.Context, g *Group) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO groups (slug, name, kind, visible, parent_id, color, title_color,
			                    icon, cost_points, duration_days, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id, depth, created_at`,
			g.Slug, g.Name, g.Kind, g.Visible, nullInt(g.ParentID), g.Color, g.TitleColor,
			g.Icon, g.CostPoints, g.DurationDays, g.SortOrder,
		).Scan(&g.ID, &g.Depth, &g.CreatedAt)
		if err != nil {
			return err
		}
		if err := writeGrants(ctx, tx, g); err != nil {
			return err
		}
		return nil
	})
}

func (s *PGStore) UpdateGroup(ctx context.Context, g *Group) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE groups SET slug=$2, name=$3, kind=$4, visible=$5, color=$6,
			                  title_color=$7, icon=$8, cost_points=$9,
			                  duration_days=$10, sort_order=$11
			WHERE id = $1`,
			g.ID, g.Slug, g.Name, g.Kind, g.Visible, g.Color, g.TitleColor,
			g.Icon, g.CostPoints, g.DurationDays, g.SortOrder)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrGroupNotFound
		}
		if err := writeGrants(ctx, tx, g); err != nil {
			return err
		}
		return nil
	})
}

func (s *PGStore) DeleteGroup(ctx context.Context, id int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// group_members and group_member_history cascade from the FK.
		_, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, id)
		return err
	})
}

// groupsParentLockID serialises catalog re-parenting. The depth trigger reads
// the proposed parent's depth, so two concurrent re-parents can each read a
// value the other is about to invalidate and leave a cycle the CHECK never
// sees. One writer at a time is what makes the descendant walk below
// authoritative. Advisory locks are cheap and this path runs at admin pace.
const groupsParentLockID = 6274_0725

// SetParent re-parents a group, rejecting cycles and over-deep chains, then
// re-stamps the moved subtree's depths.
//
// The BEFORE trigger maintains child.depth = parent.depth + 1 for the row being
// written, which makes a cycle structurally impossible for a single insert. It
// is not enough for a MOVE: re-parenting a group under its own descendant reads
// a depth that is about to change, so the walk here is the real guard.
func (s *PGStore) SetParent(ctx context.Context, id int, parentID *int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, groupsParentLockID); err != nil {
			return err
		}
		type node struct {
			parent sql.NullInt64
			depth  int
		}
		cat := map[int]node{}
		rows, err := tx.QueryContext(ctx, `SELECT id, parent_id, depth FROM groups`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var gid int
			var n node
			if err := rows.Scan(&gid, &n.parent, &n.depth); err != nil {
				rows.Close()
				return err
			}
			cat[gid] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, ok := cat[id]; !ok {
			return ErrGroupNotFound
		}

		newDepth := 0
		if parentID != nil {
			if *parentID == id {
				return ErrParentCycle
			}
			p, ok := cat[*parentID]
			if !ok {
				return ErrGroupNotFound
			}
			for cur, at := *parentID, 0; ; at++ {
				if cur == id {
					return ErrParentCycle
				}
				n := cat[cur]
				if !n.parent.Valid || at > len(cat) {
					break
				}
				cur = int(n.parent.Int64)
			}
			newDepth = p.depth + 1
		}
		// Height of the subtree being moved: rejecting on the moved group alone
		// would let a deep child land past the limit and fail on the CHECK with
		// a constraint error instead of a usable message.
		height := 0
		var walk func(int, int)
		walk = func(at, d int) {
			if d > height {
				height = d
			}
			for cid, n := range cat {
				if n.parent.Valid && int(n.parent.Int64) == at {
					walk(cid, d+1)
				}
			}
		}
		walk(id, 0)
		if newDepth+height > maxGroupDepth {
			return ErrParentTooDeep
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE groups SET parent_id = $2 WHERE id = $1`, id, nullInt(parentID)); err != nil {
			return err
		}
		// The trigger only fires for the row it was given, so the descendants
		// need re-stamping explicitly.
		_, err = tx.ExecContext(ctx, `
			WITH RECURSIVE sub AS (
			    SELECT id, depth FROM groups WHERE id = $1
			    UNION ALL
			    SELECT g.id, (sub.depth + 1)::smallint FROM groups g JOIN sub ON g.parent_id = sub.id
			)
			UPDATE groups SET depth = sub.depth FROM sub
			WHERE groups.id = sub.id AND groups.depth IS DISTINCT FROM sub.depth`, id)
		return err
	})
}

// MembersOfGroups returns live memberships for the given groups, in one query.
func (s *PGStore) MembersOfGroups(ctx context.Context, groupIDs []int) ([]Member, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	var out []Member
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT user_id, group_id, granted_at, expires_at, source
			FROM group_members
			WHERE group_id = ANY($1) AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY user_id, group_id`, pq.Array(groupIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			var exp sql.NullTime
			if err := rows.Scan(&m.UserID, &m.GroupID, &m.GrantedAt, &exp, &m.Source); err != nil {
				return err
			}
			if exp.Valid {
				m.ExpiresAt = &exp.Time
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// MembershipsOfUsers returns live memberships for the given users, in one
// query — the batch shape the display capability needs so a page rendering
// many authors does not become an N+1.
func (s *PGStore) MembershipsOfUsers(ctx context.Context, userIDs []int) ([]Member, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var out []Member
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT user_id, group_id, granted_at, expires_at, source
			FROM group_members
			WHERE user_id = ANY($1) AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY user_id, group_id`, pq.Array(userIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			var exp sql.NullTime
			if err := rows.Scan(&m.UserID, &m.GroupID, &m.GrantedAt, &exp, &m.Source); err != nil {
				return err
			}
			if exp.Valid {
				m.ExpiresAt = &exp.Time
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// BadgeData reads the memberships and the catalog in ONE transaction, and
// skips loadGrants. See the Store interface for why this is a separate method
// rather than two calls: the per-read `SET LOCAL search_path` transaction is
// the dominant cost at this domain's size, so halving the transactions matters
// more than either SELECT does, and the entitlement values loadGrants fetches
// are dead weight on a path that only draws badges.
func (s *PGStore) BadgeData(ctx context.Context, userIDs []int) ([]Member, []Group, error) {
	var members []Member
	var catalog []Group
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if len(userIDs) == 0 {
			// Catalog-only: what badges exist, nobody's membership of them.
			// Still worth going through here rather than Groups, to keep the
			// grants-free property.
			return s.scanCatalog(ctx, tx, &catalog)
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT user_id, group_id, granted_at, expires_at, source
			FROM group_members
			WHERE user_id = ANY($1) AND (expires_at IS NULL OR expires_at > NOW())
			ORDER BY user_id, group_id`, pq.Array(userIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			var exp sql.NullTime
			if err := rows.Scan(&m.UserID, &m.GroupID, &m.GrantedAt, &exp, &m.Source); err != nil {
				return err
			}
			if exp.Valid {
				m.ExpiresAt = &exp.Time
			}
			members = append(members, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		return s.scanCatalog(ctx, tx, &catalog)
	})
	if err != nil {
		return nil, nil, err
	}
	return members, catalog, nil
}

// scanCatalog reads the groups WITHOUT their grants. Shared by BadgeData's two
// shapes so neither can drift into loading them.
func (s *PGStore) scanCatalog(ctx context.Context, tx *sqlx.Tx, into *[]Group) error {
	var rows []groupRow
	if err := tx.SelectContext(ctx, &rows,
		`SELECT `+groupCols+` FROM groups ORDER BY sort_order, id`); err != nil {
		return err
	}
	out := make([]Group, len(rows))
	for i, r := range rows {
		out[i] = r.toGroup()
	}
	*into = out
	return nil
}

// MemberHistory serves the GroupAudit capability. LEFT JOIN because the
// history FK is ON DELETE SET NULL — a deleted group must not erase the record
// of what it once granted.
func (s *PGStore) MemberHistory(ctx context.Context, userID, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	out := []HistoryEntry{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT h.created_at, h.action, h.details,
			       COALESCE(g.name, ''), COALESCE(g.slug, '')
			FROM group_member_history h
			LEFT JOIN groups g ON g.id = h.group_id
			WHERE h.user_id = $1
			ORDER BY h.created_at DESC, h.id DESC
			LIMIT $2`, userID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e HistoryEntry
			if err := rows.Scan(&e.At, &e.Action, &e.Details, &e.GroupName, &e.GroupSlug); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MemberCounts is the admin catalog's live-membership column: one grouped
// query rather than a count per row.
func (s *PGStore) MemberCounts(ctx context.Context) (map[int]int, error) {
	out := map[int]int{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT group_id, count(*) FROM group_members
			WHERE expires_at IS NULL OR expires_at > NOW()
			GROUP BY group_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var gid, n int
			if err := rows.Scan(&gid, &n); err != nil {
				return err
			}
			out[gid] = n
		}
		return rows.Err()
	})
	return out, err
}

// writeGrants replaces a group's own entitlement rows.
func writeGrants(ctx context.Context, tx *sqlx.Tx, g *Group) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_entitlements WHERE group_id = $1`, g.ID); err != nil {
		return err
	}
	for key, val := range g.Grants {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO group_entitlements (group_id, key, val) VALUES ($1,$2,$3)`,
			g.ID, key, val); err != nil {
			return err
		}
	}
	return nil
}

func (s *PGStore) ActiveMembership(ctx context.Context, userID int) (*Member, error) {
	var out *Member
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var m Member
		var exp sql.NullTime
		err := tx.QueryRowContext(ctx, `
			SELECT m.user_id, m.group_id, m.granted_at, m.expires_at, m.source
			FROM group_members m JOIN groups g ON g.id = m.group_id
			WHERE m.user_id = $1 AND (m.expires_at IS NULL OR m.expires_at > NOW())
			ORDER BY g.sort_order DESC LIMIT 1`, userID).
			Scan(&m.UserID, &m.GroupID, &m.GrantedAt, &exp, &m.Source)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if exp.Valid {
			m.ExpiresAt = &exp.Time
		}
		out = &m
		return nil
	})
	return out, err
}

func (s *PGStore) AddMember(ctx context.Context, userID, groupID int, dur time.Duration) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// The group is still read so a membership in a group that does not
		// exist fails here rather than inserting an orphan the FK would catch
		// with a far less obvious error.
		if _, err := groupInTx(ctx, tx, groupID); err != nil {
			return err
		}
		// Extending stacks from the later of now and the current expiry, which
		// is the semantics the legacy SubscribeToRank had.
		//
		// The CASE is not decoration: Postgres GREATEST IGNORES nulls, so a bare
		// GREATEST(expires_at, NOW()) would silently convert a PERMANENT
		// membership (NULL — a staff/assigned grant) into one that expires. A
		// permanent membership stays permanent no matter what is granted on top.
		var expires sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO group_members (user_id, group_id, expires_at, source)
			VALUES ($1, $2, NOW() + $3::interval, 'purchase')
			ON CONFLICT (user_id, group_id) DO UPDATE
			  SET expires_at = CASE
			        WHEN group_members.expires_at IS NULL THEN NULL
			        ELSE GREATEST(group_members.expires_at, NOW()) + $3::interval
			      END
			RETURNING expires_at`,
			userID, groupID, intervalArg(dur)).Scan(&expires); err != nil {
			return err
		}
		return nil
	})
}

func (s *PGStore) ExpireMemberships(ctx context.Context) ([]Member, error) {
	var out []Member
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// expires_at IS NULL means a permanent membership (staff / assigned
		// groups). Three-valued logic already excludes those — NULL <= NOW() is
		// NULL, not TRUE — so the IS NOT NULL is documentation plus a match for
		// the partial index idx_group_members_expires, NOT the guard. The real
		// guard is the comparison itself; see the permanent-membership test.
		rows, err := tx.QueryContext(ctx, `
			DELETE FROM group_members
			WHERE expires_at IS NOT NULL AND expires_at <= NOW()
			RETURNING user_id, group_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.UserID, &m.GroupID); err != nil {
				rows.Close()
				return err
			}
			out = append(out, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range out {
			gid := m.GroupID
			if err := s.recordHistory(ctx, tx, m.UserID, &gid, "expired", "membership lapsed"); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (s *PGStore) RecordHistory(ctx context.Context, userID int, groupID *int, action, details string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return s.recordHistory(ctx, tx, userID, groupID, action, details)
	})
}

// recordHistory writes the audit row. The admin user-detail page renders these
// as the per-user Rank History panel, through the GroupAudit capability.
func (s *PGStore) recordHistory(ctx context.Context, tx *sqlx.Tx, userID int, groupID *int, action, details string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO group_member_history (user_id, group_id, action, details) VALUES ($1,$2,$3,$4)`,
		userID, nullInt(groupID), action, details); err != nil {
		return err
	}
	return nil
}

func groupInTx(ctx context.Context, tx *sqlx.Tx, id int) (*Group, error) {
	var r groupRow
	if err := tx.GetContext(ctx, &r, `SELECT `+groupCols+` FROM groups WHERE id = $1`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	g := r.toGroup()
	if err := loadGrants(ctx, tx, map[int]*Group{g.ID: &g}); err != nil {
		return nil, err
	}
	return &g, nil
}

func nullInt(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// intervalArg renders a duration for Postgres' interval parser. Hours keeps it
// exact for the day-multiples every caller actually passes.
func intervalArg(d time.Duration) string {
	h := int(d.Hours())
	if h < 1 {
		h = 1
	}
	return fmt.Sprintf("%d hours", h)
}

// RemoveMember drops one membership. See the Store interface for why an absent
// row is success rather than an error.
func (s *PGStore) RemoveMember(ctx context.Context, userID, groupID int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM group_members WHERE user_id = $1 AND group_id = $2`, userID, groupID)
		return err
	})
}
