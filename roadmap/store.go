package roadmap

import (
	"context"
)

// FlowNodePicker is the skinny row shape admin dropdowns use to
// populate the "link to flow node" select.
type FlowNodePicker struct {
	ID    int64  `db:"id"`
	Label string `db:"label"`
	Kind  string `db:"kind"`
	Tag   string `db:"tag"`
}

// FlowNodeLinkCount is one row in the GetFlowNodeLinkCounts result —
// total roadmap + changelog references pointing at the node.
type FlowNodeLinkCount struct {
	NodeID         int64 `db:"node_id"`
	RoadmapCount   int   `db:"roadmap_count"`
	ChangelogCount int   `db:"changelog_count"`
}

// RoadmapRepository is the per-domain interface for the roadmap +
// changelog admin surfaces, plus the flow-node picker / link-count
// helpers shared by both. Phase 3 extraction.
type Store interface {
	ListRoadmapItems(ctx context.Context, includeArchived bool) ([]*RoadmapItem, error)
	CreateRoadmapItem(ctx context.Context, r *RoadmapItem) (int64, error)
	UpdateRoadmapItem(ctx context.Context, r *RoadmapItem) error
	DeleteRoadmapItem(ctx context.Context, id int64) error
	ListChangelogEntries(ctx context.Context, limit, offset int) ([]*ChangelogEntry, int, error)
	CreateChangelogEntry(ctx context.Context, e *ChangelogEntry) (int64, error)
	UpdateChangelogEntry(ctx context.Context, e *ChangelogEntry) error
	DeleteChangelogEntry(ctx context.Context, id int64) error
	ListFlowNodesForPicker(ctx context.Context) ([]FlowNodePicker, error)
	ListRoadmapItemsByFlowNode(ctx context.Context, flowNodeID int64) ([]*RoadmapItem, error)
	ListChangelogEntriesByFlowNode(ctx context.Context, flowNodeID int64) ([]*ChangelogEntry, error)
	GetFlowNodeLinkCounts(ctx context.Context) ([]*FlowNodeLinkCount, error)
}
