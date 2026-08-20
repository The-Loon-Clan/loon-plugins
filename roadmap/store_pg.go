package roadmap

// PGStore is the Postgres-backed implementation of
// PGStore. Extracted from *Storage in Phase 3.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

// ListRoadmapItems returns every roadmap item, ordered status-asc
// (in_flight first by alphabetical bucket value … actually we want
// in_flight on top and archived hidden by default — caller filters).
//
// includeArchived=false (the public page) skips archived rows;
// admin page passes true.
func (r *PGStore) ListRoadmapItems(ctx context.Context, includeArchived bool) ([]*RoadmapItem, error) {
	q := `SELECT ` + RoadmapCols + ` FROM roadmap_items
	      ORDER BY CASE status
	          WHEN 'in_flight' THEN 0
	          WHEN 'backlog'   THEN 1
	          WHEN 'archived'  THEN 2
	          ELSE 3 END,
	          sort_order ASC, id ASC`
	if !includeArchived {
		q = `SELECT ` + RoadmapCols + ` FROM roadmap_items
		      WHERE status <> 'archived'
		      ORDER BY CASE status
		          WHEN 'in_flight' THEN 0
		          WHEN 'backlog'   THEN 1
		          ELSE 2 END,
		          sort_order ASC, id ASC`
	}
	var rows []*RoadmapItem
	err := r.db.SelectContext(ctx, &rows, q)
	return rows, err
}

// CreateRoadmapItem inserts. Title uniqueness is enforced at the
// DB layer; caller surfaces the conflict error to the admin form.
func (r *PGStore) CreateRoadmapItem(ctx context.Context, item *RoadmapItem) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO roadmap_items (title, description, status, sort_order,
		                            flow_node_id, system_ids, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		strings.TrimSpace(item.Title), item.Description,
		item.Status, item.SortOrder, item.FlowNodeID,
		pq.Array(CoalesceInt64Array(item.SystemIDs)), item.CreatedBy,
	).Scan(&id)
	return id, err
}

// UpdateRoadmapItem overwrites the editable fields and bumps
// updated_at. Caller is responsible for verifying admin permission.
func (r *PGStore) UpdateRoadmapItem(ctx context.Context, item *RoadmapItem) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE roadmap_items
		   SET title = $2, description = $3, status = $4,
		       sort_order = $5, flow_node_id = $6,
		       system_ids = $7,
		       updated_at = NOW()
		 WHERE id = $1`,
		item.ID, strings.TrimSpace(item.Title), item.Description,
		item.Status, item.SortOrder, item.FlowNodeID,
		pq.Array(CoalesceInt64Array(item.SystemIDs)))
	return err
}

// DeleteRoadmapItem removes one row. Hard delete by design — the
// admin page exposes "archived" as the soft-hide state. True delete
// is for typos and accidental creations.
func (r *PGStore) DeleteRoadmapItem(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM roadmap_items WHERE id = $1`, id)
	return err
}

// ListChangelogEntries returns the newest entries first. limit/
// offset for pagination on the public page; offset=0 limit=0
// returns all (for admin export). category is the shelf: "" = the
// site changelog (everything but agent release notes), "agent" =
// only those, "all" = no filter.
func (r *PGStore) ListChangelogEntries(ctx context.Context, category string, limit, offset int) ([]*ChangelogEntry, int, error) {
	where, args := "", []any{}
	switch category {
	case "all":
		// no filter
	case "":
		where = ` WHERE category <> 'agent'`
	default:
		where = ` WHERE category = $1`
		args = append(args, category)
	}
	var total int
	// sqllint:allow where is one of three literal fragments above; the one value flows through $1.
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM changelog_entries`+where, args...); err != nil {
		return nil, 0, err
	}
	// sqllint:allow same three literal fragments; pagination values are parameterized.
	q := `SELECT ` + ChangelogCols + ` FROM changelog_entries` + where + `
	      ORDER BY released_at DESC, id DESC`
	if limit > 0 {
		// Placeholder numbers depend on whether the shelf bound a value.
		if len(args) == 1 {
			q += ` LIMIT $2 OFFSET $3`
		} else {
			q += ` LIMIT $1 OFFSET $2`
		}
		args = append(args, limit, offset)
	}
	var rows []*ChangelogEntry
	// q is assembled from literals only — the three `where` fragments above and
	// the two LIMIT/OFFSET forms; every value reaches the driver as a $N.
	// sqllint:allow literal fragments only; see above
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// CreateChangelogEntry inserts. (title, released_at) uniqueness
// at the DB layer keeps the backfill idempotent.
func (r *PGStore) CreateChangelogEntry(ctx context.Context, entry *ChangelogEntry) (int64, error) {
	if entry.ReleasedAt.IsZero() {
		entry.ReleasedAt = time.Now()
	}
	if entry.Category == "" {
		entry.Category = ChangelogCategoryFeature
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO changelog_entries (title, description, released_at, category,
		                                roadmap_item_id, flow_node_id,
		                                system_ids, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (title, released_at) DO NOTHING
		RETURNING id`,
		strings.TrimSpace(entry.Title), entry.Description,
		entry.ReleasedAt, entry.Category,
		entry.RoadmapItemID, entry.FlowNodeID,
		pq.Array(CoalesceInt64Array(entry.SystemIDs)), entry.CreatedBy,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // dedup hit — caller treats 0 as "already existed"
	}
	return id, err
}

// UpdateChangelogEntry overwrites editable fields.
func (r *PGStore) UpdateChangelogEntry(ctx context.Context, entry *ChangelogEntry) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE changelog_entries
		   SET title = $2, description = $3, released_at = $4, category = $5,
		       roadmap_item_id = $6, flow_node_id = $7,
		       system_ids = $8,
		       updated_at = NOW()
		 WHERE id = $1`,
		entry.ID, strings.TrimSpace(entry.Title), entry.Description,
		entry.ReleasedAt, entry.Category, entry.RoadmapItemID, entry.FlowNodeID,
		pq.Array(CoalesceInt64Array(entry.SystemIDs)))
	return err
}

// DeleteChangelogEntry — hard delete.
func (r *PGStore) DeleteChangelogEntry(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM changelog_entries WHERE id = $1`, id)
	return err
}

// ListFlowNodesForPicker returns every alive flow node, ordered
// alphabetically by label, so admin dropdowns on the roadmap +
// changelog edit forms can offer a link target. Caps at 500 rows
// to keep the option list manageable; the graph today has ~40 so
// we have lots of headroom.
func (r *PGStore) ListFlowNodesForPicker(ctx context.Context) ([]FlowNodePicker, error) {
	var rows []FlowNodePicker
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, COALESCE(label, '') AS label, kind, COALESCE(tag, '') AS tag
		  FROM flow_nodes
		 WHERE deleted_at IS NULL
		 ORDER BY label ASC, id ASC
		 LIMIT 500`)
	return rows, err
}

// ListRoadmapItemsByFlowNode returns roadmap items that reference
// the given flow_node_id either via the primary FK OR the
// many-to-many system_ids array (migration 246). Used by the
// /flow proposal detail JSON and the /help/roadmap graph side
// panel.
func (r *PGStore) ListRoadmapItemsByFlowNode(ctx context.Context, flowNodeID int64) ([]*RoadmapItem, error) {
	var rows []*RoadmapItem
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+RoadmapCols+` FROM roadmap_items
		 WHERE flow_node_id = $1
		    OR system_ids && ARRAY[$1]::BIGINT[]
		 ORDER BY CASE status
		     WHEN 'in_flight' THEN 0
		     WHEN 'backlog'   THEN 1
		     ELSE 2 END,
		     sort_order ASC, id ASC`, flowNodeID)
	return rows, err
}

// ListChangelogEntriesByFlowNode returns changelog entries that
// reference the given flow_node_id either via the primary FK OR
// the many-to-many system_ids array. Newest first.
func (r *PGStore) ListChangelogEntriesByFlowNode(ctx context.Context, flowNodeID int64) ([]*ChangelogEntry, error) {
	var rows []*ChangelogEntry
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+ChangelogCols+` FROM changelog_entries
		 WHERE flow_node_id = $1
		    OR system_ids && ARRAY[$1]::BIGINT[]
		 ORDER BY released_at DESC, id DESC`, flowNodeID)
	return rows, err
}

// GetFlowNodeLinkCounts returns one row per alive flow node that
// has at least one linked roadmap item OR changelog entry, via
// EITHER the legacy single flow_node_id FK OR the many-to-many
// system_ids[] array (migration 246). Single scan, single
// round-trip — the public graph endpoint stays cheap even as the
// graph grows.
//
// The CTE flattens system_ids[] via unnest so the LEFT JOIN treats
// every (node, row) link as a separate edge. COUNT(DISTINCT) folds
// rows that link a node via BOTH flow_node_id and system_ids[]
// back into a single count.
func (r *PGStore) GetFlowNodeLinkCounts(ctx context.Context) ([]*FlowNodeLinkCount, error) {
	var rows []*FlowNodeLinkCount
	err := r.db.SelectContext(ctx, &rows, `
		WITH r_links AS (
		    SELECT id, flow_node_id AS node_id FROM roadmap_items
		     WHERE flow_node_id IS NOT NULL
		    UNION ALL
		    SELECT id, unnest(system_ids) AS node_id FROM roadmap_items
		     WHERE system_ids <> '{}'::BIGINT[]
		),
		c_links AS (
		    SELECT id, flow_node_id AS node_id FROM changelog_entries
		     WHERE flow_node_id IS NOT NULL
		    UNION ALL
		    SELECT id, unnest(system_ids) AS node_id FROM changelog_entries
		     WHERE system_ids <> '{}'::BIGINT[]
		)
		SELECT n.id AS node_id,
		       COUNT(DISTINCT r.id) AS roadmap_count,
		       COUNT(DISTINCT c.id) AS changelog_count
		  FROM flow_nodes n
		  LEFT JOIN r_links r ON r.node_id = n.id
		  LEFT JOIN c_links c ON c.node_id = n.id
		 WHERE n.deleted_at IS NULL
		 GROUP BY n.id
		HAVING COUNT(DISTINCT r.id) > 0 OR COUNT(DISTINCT c.id) > 0`)
	return rows, err
}

// CoalesceInt64Array returns a non-nil slice — Postgres rejects
// nil arrays for NOT NULL columns even with a DEFAULT, so we
// normalise to an empty slice before binding.
func CoalesceInt64Array(a pq.Int64Array) pq.Int64Array {
	if a == nil {
		return pq.Int64Array{}
	}
	return a
}

// ─── Roadmap items (migration 232) ───────────────────────────────

const RoadmapCols = `id, title, description, status, sort_order,
		flow_node_id, system_ids, created_by, created_at, updated_at`

// ─── Changelog entries (migration 232) ───────────────────────────

const ChangelogCols = `id, title, description, released_at, category,
		roadmap_item_id, flow_node_id, system_ids, created_by, created_at, updated_at`

// ─── Flow-node cross-links (migration 232 columns) ───────────────

// FlowNodePicker moved to pkg/storage/roadmap.go (Phase 3).

// FlowNodeLinkCount is the per-node count of roadmap items +
