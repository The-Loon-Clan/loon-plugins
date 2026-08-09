package roadmap

// PGFlowStore is the Postgres implementation of FlowStore — the
// collaborative node-graph editor at /flow (nodes, edges, comments,
// revisions, snapshots, proposal queue + mockup index). Moved wholesale
// from the ameNZB host's pkg/storage/postgres/flow.go: the host had no
// other caller, so the flow domain is plugin-private in fact and now in
// form. The tables stay in the host's migrations.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type PGFlowStore struct {
	db *sqlx.DB
}

func NewPGFlowStore(db *sqlx.DB) *PGFlowStore {
	return &PGFlowStore{db: db}
}

var _ FlowStore = (*PGFlowStore)(nil)

// ── Flow graph (collaborative node-graph editor at /flow) ───────────────────

// GetFlowGraph returns every alive node + edge as a single payload —
// what the editor loads on page open and what new WebSocket clients
// receive on connect. Node order is by ID so the canonical seeds (id<0)
// land first and the layout is deterministic between users.
func (r *PGFlowStore) GetFlowGraph(ctx context.Context) (*FlowGraph, error) {
	g := &FlowGraph{}
	if err := r.db.SelectContext(ctx, &g.Nodes, `
		SELECT id, kind, label, description, x, y, data_json, created_by,
		       locked, created_at, updated_at, deleted_at,
		       parent_node_id, merged_into_id, vote_count
		  FROM flow_nodes
		 WHERE deleted_at IS NULL
		 ORDER BY id`); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &g.Edges, `
		SELECT e.id, e.source_id, e.target_id, e.source_port, e.target_port,
		       e.label, e.kind, e.created_by, e.locked, e.created_at
		  FROM flow_edges e
		  JOIN flow_nodes ns ON ns.id = e.source_id AND ns.deleted_at IS NULL
		  JOIN flow_nodes nt ON nt.id = e.target_id AND nt.deleted_at IS NULL
		 ORDER BY e.id`); err != nil {
		return nil, err
	}
	return g, nil
}

// GetMockupNodes returns every alive mockup-kind node in the graph
// for the discovery / index page. Ordered server-side per the sort
// arg ("newest" | "votes"); anything else falls back to newest.
//
// HTML is included so the index page can render thumbnail iframes
// inline instead of issuing N follow-up requests. The blob is
// already capped at whatever a user typed into the inspector, and
// the iframe sandbox keeps it safe to render — same security model
// as the canvas.
//
// LEFT JOIN against the parent so a mockup proposing an edit to a
// canonical page surfaces "🖼 Mockup → 🔐 Login" on the card without
// a round-trip per row.
func (r *PGFlowStore) GetMockupNodes(ctx context.Context, sortBy string) ([]*MockupSummary, error) {
	order := `ORDER BY n.created_at DESC`
	if sortBy == "votes" {
		order = `ORDER BY n.vote_count DESC, n.created_at DESC`
	}
	var rows []*MockupSummary
	err := r.db.SelectContext(ctx, &rows, `
		SELECT n.id, n.label,
		       COALESCE(n.data_json->>'html', '') AS html,
		       n.vote_count, n.created_at, n.updated_at,
		       n.created_by,
		       COALESCE(u.username, '')           AS author_name,
		       n.parent_node_id,
		       COALESCE(p.label, '')              AS parent_label,
		       n.locked
		  FROM flow_nodes n
		  LEFT JOIN users u      ON u.id = n.created_by
		  LEFT JOIN flow_nodes p ON p.id = n.parent_node_id AND p.deleted_at IS NULL
		 WHERE n.kind = 'mockup'
		   AND n.deleted_at IS NULL
		`+order /* no params */)
	return rows, err
}

// GetFlowNode fetches one node (including soft-deleted, for ownership
// checks before a hard-delete). Returns sql.ErrNoRows if not present.
func (r *PGFlowStore) GetFlowNode(ctx context.Context, id int64) (*FlowNode, error) {
	n := &FlowNode{}
	err := r.db.GetContext(ctx, n, `
		SELECT id, kind, label, description, x, y, data_json, created_by,
		       locked, created_at, updated_at, deleted_at,
		       parent_node_id, merged_into_id, vote_count,
		       COALESCE(tag, '') AS tag, COALESCE(status, 'open') AS status
		  FROM flow_nodes WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// CreateFlowNode inserts a user-added node. Locked is forced false here
// — only mods can promote to locked via UpdateFlowNode. The seed rows
// in migration 178 set locked=true directly.
func (r *PGFlowStore) CreateFlowNode(ctx context.Context, in *FlowNode) (*FlowNode, error) {
	if in.Kind == "" {
		in.Kind = "user-proposal"
	}
	if len(in.DataJSON) == 0 {
		in.DataJSON = []byte(`{}`)
	}
	out := &FlowNode{}
	// Tag defaults to '' (untagged); allowlist enforcement is the
	// handler's job. Status defaults to 'open' at the column level.
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO flow_nodes (kind, label, description, x, y, data_json, created_by, locked, parent_node_id, tag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE, $8, $9)
		RETURNING id, kind, label, description, x, y, data_json, created_by,
		          locked, created_at, updated_at, deleted_at,
		          parent_node_id, merged_into_id, vote_count,
		          COALESCE(tag, '') AS tag, COALESCE(status, 'open') AS status`,
		in.Kind, in.Label, in.Description, in.X, in.Y, in.DataJSON, in.CreatedBy, in.ParentNodeID, in.Tag,
	).Scan(&out.ID, &out.Kind, &out.Label, &out.Description, &out.X, &out.Y,
		&out.DataJSON, &out.CreatedBy, &out.Locked, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		&out.ParentNodeID, &out.MergedIntoID, &out.VoteCount, &out.Tag, &out.Status)
	return out, err
}

// ForkFlowNode creates a wiki-style "edit proposal" against an existing
// canonical node. Pre-fills label / description / data_json from the
// parent so the user starts editing what's already there instead of a
// blank slate. Position offset slightly so the fork doesn't sit
// directly on top of the original.
//
// Anyone can fork any node — including locked canonical seeds — but the
// fork itself is always a regular user-proposal (locked=false), and the
// proposer's user id is captured in created_by. Promotion (merging the
// fork's content back onto the canonical) is mod-gated downstream.
func (r *PGFlowStore) ForkFlowNode(ctx context.Context, parentID int64, userID int) (*FlowNode, error) {
	parent, err := r.GetFlowNode(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent.DeletedAt != nil {
		return nil, sql.ErrNoRows
	}
	uid := userID
	in := &FlowNode{
		Kind:         "user-proposal",
		Label:        parent.Label,
		Description:  parent.Description,
		X:            parent.X + 220, // sit to the right of the parent
		Y:            parent.Y + 60,
		DataJSON:     parent.DataJSON,
		CreatedBy:    &uid,
		ParentNodeID: &parent.ID,
	}
	return r.CreateFlowNode(ctx, in)
}

// VoteForFlowNode toggles a single user's vote on a node and keeps the
// denormalised flow_nodes.vote_count in sync. Returns (newCount, added):
// added=true means the call inserted a vote, false means it removed
// one. Mirrors VoteForRequest's semantics so the inspector vote UI is
// the same shape as the requests page.
func (r *PGFlowStore) VoteForFlowNode(ctx context.Context, nodeID int64, userID int) (int, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var existed bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM flow_votes WHERE node_id=$1 AND user_id=$2)`,
		nodeID, userID).Scan(&existed); err != nil {
		return 0, false, err
	}

	added := !existed
	if existed {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM flow_votes WHERE node_id=$1 AND user_id=$2`, nodeID, userID); err != nil {
			return 0, false, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE flow_nodes SET vote_count = GREATEST(vote_count - 1, 0) WHERE id=$1`, nodeID); err != nil {
			return 0, false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO flow_votes (node_id, user_id) VALUES ($1, $2)`, nodeID, userID); err != nil {
			return 0, false, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE flow_nodes SET vote_count = vote_count + 1 WHERE id=$1`, nodeID); err != nil {
			return 0, false, err
		}
	}

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT vote_count FROM flow_nodes WHERE id=$1`, nodeID).Scan(&count); err != nil {
		return 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return count, added, nil
}

// HasVotedForFlowNode is the per-user gate for rendering the vote
// button as toggled vs untoggled.
func (r *PGFlowStore) HasVotedForFlowNode(ctx context.Context, nodeID int64, userID int) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	var v bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM flow_votes WHERE node_id=$1 AND user_id=$2)`,
		nodeID, userID).Scan(&v)
	return v, err
}

// MergeFlowProposalIntoParent copies a proposal's editable fields onto
// its parent (label, description, data_json — but not position, which
// stays where the canonical sits) and soft-deletes the proposal with
// merged_into_id pointing at the parent. Mods only — caller gates.
//
// Idempotent in the no-op sense: a proposal with no parent_node_id is
// rejected; a proposal already merged is rejected. The parent is
// updated with locked=true preserved (a canonical stays canonical).
//
// Audit: a snapshot of the parent's pre-merge state is appended to
// flow_node_revisions inside the same transaction, so the history
// view can render "what this canonical looked like before merge #N".
// actorUserID is the user that triggered the merge (whoever clicked
// Merge in the UI); nil for system-driven merges.
func (r *PGFlowStore) MergeFlowProposalIntoParent(ctx context.Context, proposalID int64, actorUserID *int) error {
	proposal, err := r.GetFlowNode(ctx, proposalID)
	if err != nil {
		return err
	}
	if proposal.DeletedAt != nil {
		return fmt.Errorf("proposal already deleted/merged")
	}
	if proposal.ParentNodeID == nil || *proposal.ParentNodeID <= 0 {
		return fmt.Errorf("proposal has no parent to merge into")
	}

	// Snapshot the parent BEFORE we mutate it — the history viewer
	// needs the pre-merge state, not the post-merge one.
	parent, err := r.GetFlowNode(ctx, *proposal.ParentNodeID)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Append-only audit row. Captures the parent's pre-merge state.
	// Summary line is human-readable for the history list.
	snapshot, _ := json.Marshal(parent)
	summary := fmt.Sprintf("merged proposal #%d (%q) by %s", proposal.ID, proposal.Label, formatActor(proposal.CreatedBy))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO flow_node_revisions (node_id, snapshot_json, action_kind, actor_user_id, summary)
		VALUES ($1, $2, 'pre_merge', $3, $4)`,
		parent.ID, snapshot, actorUserID, summary); err != nil {
		return err
	}

	// Copy editable fields from the proposal onto the parent.
	if _, err := tx.ExecContext(ctx, `
		UPDATE flow_nodes
		   SET label       = $2,
		       description = $3,
		       data_json   = $4,
		       updated_at  = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`,
		*proposal.ParentNodeID, proposal.Label, proposal.Description, proposal.DataJSON); err != nil {
		return err
	}

	// Soft-delete the proposal + record where its content went.
	if _, err := tx.ExecContext(ctx, `
		UPDATE flow_nodes
		   SET deleted_at     = NOW(),
		       merged_into_id = $2
		 WHERE id = $1`, proposalID, *proposal.ParentNodeID); err != nil {
		return err
	}

	return tx.Commit()
}

// formatActor renders a *int user id as "user #N" or "anon" for the
// summary string. Names aren't joined here because we only have the
// id; the history viewer can resolve via GetUserByID if it wants the
// real username.
func formatActor(uid *int) string {
	if uid == nil || *uid <= 0 {
		return "anon"
	}
	return fmt.Sprintf("user #%d", *uid)
}

// GetFlowNodeRevisions returns the per-node history list, newest
// first. Cap baked in to keep the inspector popover snappy on
// long-lived nodes — the table grows append-only and a heavily-edited
// canonical could accumulate hundreds of rows over time.
func (r *PGFlowStore) GetFlowNodeRevisions(ctx context.Context, nodeID int64, limit int) ([]*FlowNodeRevision, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []*FlowNodeRevision
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, node_id, snapshot_json, action_kind, actor_user_id, summary, created_at
		  FROM flow_node_revisions
		 WHERE node_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`, nodeID, limit)
	return out, err
}

// UpdateFlowNode applies a partial update via fill-if-set semantics: a
// nil pointer means "leave that column alone". This is what the
// realtime op protocol needs — a "move" op only sets X+Y; a "rename"
// op only sets Label; etc. The handler hits this once per WebSocket op
// without having to rehydrate the full row first.
func (r *PGFlowStore) UpdateFlowNode(ctx context.Context, id int64, label, description *string, x, y *float64, dataJSON []byte, locked *bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE flow_nodes SET
			label       = COALESCE($2, label),
			description = COALESCE($3, description),
			x           = COALESCE($4, x),
			y           = COALESCE($5, y),
			data_json   = COALESCE($6, data_json),
			locked      = COALESCE($7, locked),
			updated_at  = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`,
		id, label, description, x, y, dataJSON, locked)
	return err
}

// DeleteFlowNode soft-deletes the node and hard-deletes any incident
// edges (FK cascade isn't quite right since the node row stays around;
// we want the edges gone immediately so the canvas doesn't render
// orphan lines). Returns the count of edges that were also removed,
// for the realtime broadcast payload.
func (r *PGFlowStore) DeleteFlowNode(ctx context.Context, id int64) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM flow_edges WHERE source_id = $1 OR target_id = $1`, id)
	if err != nil {
		return 0, err
	}
	edgeCount, _ := res.RowsAffected()
	if _, err := r.db.ExecContext(ctx,
		`UPDATE flow_nodes SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`,
		id); err != nil {
		return edgeCount, err
	}
	return edgeCount, nil
}

// CreateFlowEdge wires two existing nodes. The DB UNIQUE constraint on
// (source_id, target_id, kind) collapses duplicates. SourcePort /
// TargetPort default to ” when the caller doesn't specify them — the
// JS treats empty as "the default port" so legacy callers still work.
func (r *PGFlowStore) CreateFlowEdge(ctx context.Context, in *FlowEdge) (*FlowEdge, error) {
	if in.Kind == "" {
		in.Kind = "default"
	}
	out := &FlowEdge{}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO flow_edges (source_id, target_id, source_port, target_port, label, kind, created_by, locked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE)
		ON CONFLICT (source_id, target_id, kind) DO UPDATE SET
		    label       = EXCLUDED.label,
		    source_port = EXCLUDED.source_port,
		    target_port = EXCLUDED.target_port
		RETURNING id, source_id, target_id, source_port, target_port, label, kind, created_by, locked, created_at`,
		in.SourceID, in.TargetID, in.SourcePort, in.TargetPort, in.Label, in.Kind, in.CreatedBy,
	).Scan(&out.ID, &out.SourceID, &out.TargetID, &out.SourcePort, &out.TargetPort,
		&out.Label, &out.Kind, &out.CreatedBy, &out.Locked, &out.CreatedAt)
	return out, err
}

// GetFlowEdge fetches one edge for ownership/lock checks.
func (r *PGFlowStore) GetFlowEdge(ctx context.Context, id int64) (*FlowEdge, error) {
	e := &FlowEdge{}
	err := r.db.GetContext(ctx, e, `
		SELECT id, source_id, target_id, source_port, target_port,
		       label, kind, created_by, locked, created_at
		  FROM flow_edges WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// DeleteFlowEdge removes an edge. Hard delete (no soft-delete column —
// edges are cheap to recreate and the audit value is low).
func (r *PGFlowStore) DeleteFlowEdge(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM flow_edges WHERE id = $1`, id)
	return err
}

// ── Flow comments + snapshots (phase 3) ─────────────────────────────────────

// AddFlowComment inserts a comment on a node and returns the persisted
// row (with the server-stamped timestamp + auto-generated id). Username
// is denormalized so the hub doesn't need a join when echoing the
// comment to clients.
func (r *PGFlowStore) AddFlowComment(ctx context.Context, nodeID int64, userID int, username, body string) (*FlowComment, error) {
	c := &FlowComment{}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO flow_comments (node_id, user_id, username, body)
		VALUES ($1, $2, $3, $4)
		RETURNING id, node_id, user_id, username, body, created_at`,
		nodeID, userID, username, body,
	).Scan(&c.ID, &c.NodeID, &c.UserID, &c.Username, &c.Body, &c.CreatedAt)
	return c, err
}

// GetFlowComments returns the thread on one node, oldest first.
func (r *PGFlowStore) GetFlowComments(ctx context.Context, nodeID int64) ([]*FlowComment, error) {
	var out []*FlowComment
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, node_id, user_id, username, body, created_at
		  FROM flow_comments
		 WHERE node_id = $1
		 ORDER BY created_at ASC, id ASC`, nodeID)
	return out, err
}

// CreateFlowSnapshot dumps the current graph as JSONB and stores it
// with the node/edge counts pre-computed. Reason is one of "periodic",
// "pre-restore", "manual" — see migration 179 comment.
func (r *PGFlowStore) CreateFlowSnapshot(ctx context.Context, payload []byte, nodeCount, edgeCount int, reason string) (*FlowSnapshot, error) {
	snap := &FlowSnapshot{}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO flow_snapshots (payload, node_count, edge_count, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id, payload, node_count, edge_count, created_at, reason`,
		payload, nodeCount, edgeCount, reason,
	).Scan(&snap.ID, &snap.Payload, &snap.NodeCount, &snap.EdgeCount, &snap.CreatedAt, &snap.Reason)
	return snap, err
}

// PruneFlowSnapshots drops snapshots older than the cutoff. Called by
// FlowSnapshotService after it inserts a new one.
func (r *PGFlowStore) PruneFlowSnapshots(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM flow_snapshots WHERE created_at < $1 AND reason = 'periodic'`, olderThan)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListFlowProposals returns one page of user-added, not-locked,
// not-deleted nodes plus the total count for pagination. All filter
// fields are optional. The author's avatar path + role are joined so
// the listing template can render forum-style rows without a
// per-row fetch.
//
// "Active" sort ranks by the most recent comment timestamp, falling
// back to created_at when there are no comments — useful for finding
// requests where discussion is happening right now.
func (r *PGFlowStore) ListFlowProposals(ctx context.Context, f FlowProposalFilter) ([]*FlowProposal, int, error) {
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 20
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	offset := (f.Page - 1) * f.PageSize

	orderClause := `ORDER BY n.id DESC` // sqllint:allow newest sort, hardcoded.
	switch f.Sort {
	case "top":
		orderClause = `ORDER BY n.vote_count DESC, n.id DESC` // sqllint:allow top sort, hardcoded.
	case "active":
		orderClause = `ORDER BY COALESCE(cc.last_comment_at, n.created_at) DESC, n.id DESC` // sqllint:allow active sort, hardcoded.
	}

	// Total count first (cheap; same WHERE without ORDER/LIMIT/JOINs we
	// don't need for counting).
	var total int
	if err := r.db.GetContext(ctx, &total, `
		SELECT COUNT(*) FROM flow_nodes n
		 WHERE n.deleted_at IS NULL
		   AND n.locked = FALSE
		   AND n.created_by IS NOT NULL
		   AND ($1 = '' OR n.tag = $1)
		   AND ($2 = '' OR n.status = $2)`, f.Tag, f.Status); err != nil {
		return nil, 0, err
	}

	var out []*FlowProposal
	// sqllint:allow orderClause is one of three hardcoded SQL fragments above.
	err := r.db.SelectContext(ctx, &out, `
		SELECT n.id, n.kind, n.label, n.description, n.x, n.y, n.data_json,
		       n.created_by, n.locked, n.created_at, n.updated_at, n.deleted_at,
		       n.parent_node_id, n.merged_into_id, n.vote_count,
		       COALESCE(n.tag, '')              AS tag,
		       COALESCE(n.status, 'open')       AS status,
		       COALESCE(u.username, '')         AS username,
		       COALESCE(u.avatar_path, '')      AS avatar_path,
		       COALESCE(u.role, 'user')         AS user_role,
		       COALESCE(cc.comment_count, 0)::int AS comment_count
		  FROM flow_nodes n
		  LEFT JOIN users u  ON u.id = n.created_by
		  LEFT JOIN (SELECT node_id,
		                    COUNT(*)         AS comment_count,
		                    MAX(created_at)  AS last_comment_at
		               FROM flow_comments GROUP BY node_id) cc
		         ON cc.node_id = n.id
		 WHERE n.deleted_at IS NULL
		   AND n.locked = FALSE
		   AND n.created_by IS NOT NULL
		   AND ($1 = '' OR n.tag = $1)
		   AND ($2 = '' OR n.status = $2)
		 `+orderClause+`
		 LIMIT $3 OFFSET $4`, f.Tag, f.Status, f.PageSize, offset)
	return out, total, err
}

// SearchSimilarProposals powers the "did you mean…" hint on the new
// Feature Requests form. Returns up to N user-added rows whose label
// matches the query via case-insensitive ILIKE — duplicate detection
// at title-time, not after submit. We deliberately don't search
// description here (too noisy for live-typing).
func (r *PGFlowStore) SearchSimilarProposals(ctx context.Context, query string, limit int) ([]*FlowProposal, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	pattern := "%" + q + "%"
	var out []*FlowProposal
	err := r.db.SelectContext(ctx, &out, `
		SELECT n.id, n.kind, n.label, n.description, n.x, n.y, n.data_json,
		       n.created_by, n.locked, n.created_at, n.updated_at, n.deleted_at,
		       n.parent_node_id, n.merged_into_id, n.vote_count,
		       COALESCE(n.tag, '')              AS tag,
		       COALESCE(n.status, 'open')       AS status,
		       COALESCE(u.username, '')         AS username,
		       COALESCE(u.avatar_path, '')      AS avatar_path,
		       COALESCE(u.role, 'user')         AS user_role,
		       0::int                            AS comment_count
		  FROM flow_nodes n
		  LEFT JOIN users u ON u.id = n.created_by
		 WHERE n.deleted_at IS NULL
		   AND n.created_by IS NOT NULL
		   AND n.label ILIKE $1
		 ORDER BY n.vote_count DESC, n.id DESC
		 LIMIT $2`, pattern, limit)
	return out, err
}

// SetFlowNodeTag updates the submitter-chosen category. Allowlist
// enforcement is the handler's job — this writes whatever the caller
// passes (so a refactor that adds a new tag doesn't need a storage
// change too). Empty string clears the tag.
func (r *PGFlowStore) SetFlowNodeTag(ctx context.Context, id int64, tag string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE flow_nodes SET tag = $2, updated_at = NOW() WHERE id = $1`,
		id, tag)
	return err
}

// SetFlowNodeStatus updates the mod-managed lifecycle pointer.
// Allowlist enforcement is the handler's job (same rationale as
// SetFlowNodeTag).
func (r *PGFlowStore) SetFlowNodeStatus(ctx context.Context, id int64, status string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE flow_nodes SET status = $2, updated_at = NOW() WHERE id = $1`,
		id, status)
	return err
}

// PromoteFlowNode flips a user-added node to canonical (locked=true).
// Mod-only — handler enforces. The node's existing edges keep their
// own locked flag; the mod can promote them separately if they want
// the connection to be canonical too.
func (r *PGFlowStore) PromoteFlowNode(ctx context.Context, id int64, actorUserID *int) error {
	// Snapshot the pre-promotion row so the history viewer can show
	// "this used to be a user-proposal that got promoted on {date} by
	// user #N". Idempotent if the node is already locked — the
	// snapshot still records the moment of confirmation.
	pre, err := r.GetFlowNode(ctx, id)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, _ := json.Marshal(pre)
	summary := fmt.Sprintf("promoted to canonical (was user-proposal by %s)", formatActor(pre.CreatedBy))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO flow_node_revisions (node_id, snapshot_json, action_kind, actor_user_id, summary)
		VALUES ($1, $2, 'pre_promote', $3, $4)`,
		id, snapshot, actorUserID, summary); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE flow_nodes SET locked = TRUE, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}
