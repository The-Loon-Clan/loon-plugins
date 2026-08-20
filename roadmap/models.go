package roadmap

import (
	"time"

	"github.com/lib/pq"
)

// RoadmapItem (migration 232) is one forward-looking entry on the
// public /help/roadmap page. Shipped work is NOT a roadmap item;
// shipped work lives in ChangelogEntry. Roadmap items are
// big-picture vision items ("Community-driven moderation"), not
// per-release granular changes.
//
// SystemIDs (migration 246) is the many-to-many extension of the
// single FlowNodeID FK. FlowNodeID stays as the display "primary"
// anchor; SystemIDs is the full set of flow nodes this item
// touches. The /help/roadmap graph view treats SystemIDs as the
// source of truth.
type RoadmapItem struct {
	ID          int64         `db:"id"`
	Title       string        `db:"title"`
	Description string        `db:"description"`
	Status      string        `db:"status"` // in_flight | backlog | archived
	SortOrder   int           `db:"sort_order"`
	FlowNodeID  *int64        `db:"flow_node_id"`
	SystemIDs   pq.Int64Array `db:"system_ids"`
	CreatedBy   *int          `db:"created_by"`
	CreatedAt   time.Time     `db:"created_at"`
	UpdatedAt   time.Time     `db:"updated_at"`
}

// ChangelogEntry (migration 232) is one row on the changelog tab of
// the public /help/roadmap page. Grouped by ReleasedAt for the
// time-bucketed render. Categories drive the colored badge on the
// public page. SystemIDs added in migration 246 — same purpose as
// on RoadmapItem.
type ChangelogEntry struct {
	ID            int64         `db:"id"`
	Title         string        `db:"title"`
	Description   string        `db:"description"`
	ReleasedAt    time.Time     `db:"released_at"`
	Category      string        `db:"category"` // feature|fix|perf|security|infra|docs
	RoadmapItemID *int64        `db:"roadmap_item_id"`
	FlowNodeID    *int64        `db:"flow_node_id"`
	SystemIDs     pq.Int64Array `db:"system_ids"`
	CreatedBy     *int          `db:"created_by"`
	CreatedAt     time.Time     `db:"created_at"`
	UpdatedAt     time.Time     `db:"updated_at"`
}

// Status / category enums centralised here so storage and template
// share the source of truth.
const (
	RoadmapStatusInFlight = "in_flight"
	RoadmapStatusBacklog  = "backlog"
	RoadmapStatusArchived = "archived"

	ChangelogCategoryFeature  = "feature"
	ChangelogCategoryFix      = "fix"
	ChangelogCategoryPerf     = "perf"
	ChangelogCategorySecurity = "security"
	ChangelogCategoryInfra    = "infra"
	ChangelogCategoryDocs     = "docs"
	// ChangelogCategoryAgent marks agent release notes — shown on their own
	// tab of /help/roadmap rather than mixed into the site changelog. Needs
	// the host's changelog_entries category CHECK to allow it (site
	// migration 322 on the origin deployment).
	ChangelogCategoryAgent = "agent"
)

// ── The flow domain, moved wholesale from the host ──────────────────────────
//
// pkg/models/flow.go + the storage DTOs came with the PGFlowStore: the host
// had no other caller of the flow repository, so the domain was plugin-
// private in fact and is now in form. The json tags are the /flow editor's
// wire format and the db tags are the (host-migrated) schema — both are
// contracts, copied exactly.

// FlowNode is one element on the /flow graph canvas.
//
// Locked = canonical seed (only mods may edit/delete). DataJSON is a
// free-form structured payload — the editor uses it for per-kind extras.
// ParentNodeID set ⇒ this row is an "edit proposal" against that canonical
// node; MergedIntoID set ⇒ the proposal was accepted and the row is
// soft-deleted, kept for audit. VoteCount is a denormalised tally.
type FlowNode struct {
	ID          int64      `db:"id"           json:"id"`
	Kind        string     `db:"kind"         json:"kind"`
	Label       string     `db:"label"        json:"label"`
	Description string     `db:"description"  json:"description"`
	X           float64    `db:"x"            json:"x"`
	Y           float64    `db:"y"            json:"y"`
	DataJSON    []byte     `db:"data_json"    json:"data,omitempty"`
	CreatedBy   *int       `db:"created_by"   json:"created_by"`
	Locked      bool       `db:"locked"       json:"locked"`
	CreatedAt   time.Time  `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"   json:"updated_at"`
	DeletedAt   *time.Time `db:"deleted_at"   json:"-"`

	ParentNodeID *int64 `db:"parent_node_id" json:"parent_node_id,omitempty"`
	MergedIntoID *int64 `db:"merged_into_id" json:"merged_into_id,omitempty"`
	VoteCount    int    `db:"vote_count"     json:"vote_count"`
	Tag          string `db:"tag"    json:"tag"`
	Status       string `db:"status" json:"status"`
}

// FlowEdge connects two FlowNodes. Directional; self-loops and duplicates
// rejected. Empty ports mean "the default/only port".
type FlowEdge struct {
	ID         int64     `db:"id"           json:"id"`
	SourceID   int64     `db:"source_id"    json:"source_id"`
	TargetID   int64     `db:"target_id"    json:"target_id"`
	SourcePort string    `db:"source_port"  json:"source_port"`
	TargetPort string    `db:"target_port"  json:"target_port"`
	Label      string    `db:"label"        json:"label"`
	Kind       string    `db:"kind"         json:"kind"`
	CreatedBy  *int      `db:"created_by"   json:"created_by"`
	Locked     bool      `db:"locked"       json:"locked"`
	CreatedAt  time.Time `db:"created_at"   json:"created_at"`
}

// FlowGraph bundles the visible canvas state — what GET /flow/data returns.
type FlowGraph struct {
	Nodes []*FlowNode `json:"nodes"`
	Edges []*FlowEdge `json:"edges"`
}

// FlowComment is one thread post on a node.
type FlowComment struct {
	ID        int64     `db:"id"          json:"id"`
	NodeID    int64     `db:"node_id"     json:"node_id"`
	UserID    int       `db:"user_id"     json:"user_id"`
	Username  string    `db:"username"    json:"username"`
	Body      string    `db:"body"        json:"body"`
	CreatedAt time.Time `db:"created_at"  json:"created_at"`
}

// FlowNodeRevision is one per-node audit row captured before a wiki-style
// mutation (pre_merge / pre_promote).
type FlowNodeRevision struct {
	ID           int64     `db:"id"             json:"id"`
	NodeID       int64     `db:"node_id"        json:"node_id"`
	SnapshotJSON []byte    `db:"snapshot_json"  json:"snapshot,omitempty"`
	ActionKind   string    `db:"action_kind"    json:"action_kind"`
	ActorUserID  *int      `db:"actor_user_id"  json:"actor_user_id,omitempty"`
	Summary      string    `db:"summary"        json:"summary"`
	CreatedAt    time.Time `db:"created_at"     json:"created_at"`
}

// FlowSnapshot is a periodic dump of the whole alive graph for rollback.
type FlowSnapshot struct {
	ID        int64     `db:"id"           json:"id"`
	Payload   []byte    `db:"payload"      json:"-"`
	NodeCount int       `db:"node_count"   json:"node_count"`
	EdgeCount int       `db:"edge_count"   json:"edge_count"`
	CreatedAt time.Time `db:"created_at"   json:"created_at"`
	Reason    string    `db:"reason"       json:"reason"`
}

// MockupSummary is the projection rendered on the /flow/mockups index.
type MockupSummary struct {
	ID           int64     `db:"id"`
	Label        string    `db:"label"`
	HTML         string    `db:"html"`
	VoteCount    int       `db:"vote_count"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
	CreatedBy    *int      `db:"created_by"`
	AuthorName   string    `db:"author_name"`
	ParentNodeID *int64    `db:"parent_node_id"`
	ParentLabel  string    `db:"parent_label"`
	Locked       bool      `db:"locked"`
}

// FlowProposal is one row in the /flow/proposals queue — a user-authored
// node plus its author identity and comment count.
type FlowProposal struct {
	FlowNode
	Username     string `db:"username"`
	AvatarPath   string `db:"avatar_path"`
	UserRole     string `db:"user_role"`
	CommentCount int    `db:"comment_count"`
}

// FlowProposalFilter shapes the /flow/proposals list query.
type FlowProposalFilter struct {
	Tag    string
	Status string
	Sort   string
	// Mine carries the viewer's own id, and MineOnly says whether to restrict
	// the listing to it.
	//
	// TWO FIELDS rather than "zero means no restriction". That trick makes one
	// value mean both "everybody" and "the person who is not signed in", which
	// is the shape that let comments.Delete remove anybody's comment. Here it
	// was only a listing filter and the rows are public, so the cost was an
	// anonymous "My Requests" showing EVERY request instead of none — wrong
	// rather than dangerous. Same shape, so it goes the same way.
	Mine     int
	MineOnly bool
	Page     int
	PageSize int
}

// FlowProposalFacets counts the live proposals by tag and by status.
//
// The filter strip offered fifteen pills — six tags, six statuses, three
// sorts — with no indication of what was behind any of them. On a page
// holding a single request that meant almost every click landed on "no
// requests match the current filters", which reads as a broken page rather
// than an empty category.
//
// Counts turn each pill into information: a member can see there are four
// bugs and no performance requests without clicking either. The zero ones
// are then not worth rendering at all, which is what actually shrinks the
// strip back to the size of the content.
//
// Untagged is counted separately because it is not one of the tags. The one
// real request on the page has no tag, so without it the only request was
// unreachable from every pill in the Type row.
type FlowProposalFacets struct {
	Tags     map[string]int
	Statuses map[string]int
	Untagged int
	Total    int
}

// Viewer is who is looking at the page; nil for anonymous. Mod is the one
// authority level this surface gates on — what maps to it is the host's
// decision.
type Viewer struct {
	ID       int
	Username string
	Mod      bool
}
