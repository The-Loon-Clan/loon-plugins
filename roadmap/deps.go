package roadmap

import (
	"context"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/blob"
)

// FlowStore is the flow domain's store — plugin-owned (PGFlowStore over the
// host's DB handle), interface kept so tests can fake it.
type FlowStore interface {
	GetFlowGraph(ctx context.Context) (*FlowGraph, error)
	GetMockupNodes(ctx context.Context, sortBy string) ([]*MockupSummary, error)
	GetFlowNode(ctx context.Context, id int64) (*FlowNode, error)
	CreateFlowNode(ctx context.Context, in *FlowNode) (*FlowNode, error)
	ForkFlowNode(ctx context.Context, parentID int64, userID int) (*FlowNode, error)
	VoteForFlowNode(ctx context.Context, nodeID int64, userID int) (int, bool, error)
	HasVotedForFlowNode(ctx context.Context, nodeID int64, userID int) (bool, error)
	MergeFlowProposalIntoParent(ctx context.Context, proposalID int64, actorUserID *int) error
	GetFlowNodeRevisions(ctx context.Context, nodeID int64, limit int) ([]*FlowNodeRevision, error)
	UpdateFlowNode(ctx context.Context, id int64, label, description *string, x, y *float64, dataJSON []byte, locked *bool) error
	DeleteFlowNode(ctx context.Context, id int64) (int64, error)
	CreateFlowEdge(ctx context.Context, in *FlowEdge) (*FlowEdge, error)
	GetFlowEdge(ctx context.Context, id int64) (*FlowEdge, error)
	DeleteFlowEdge(ctx context.Context, id int64) error
	AddFlowComment(ctx context.Context, nodeID int64, userID int, username, body string) (*FlowComment, error)
	GetFlowComments(ctx context.Context, nodeID int64) ([]*FlowComment, error)
	CreateFlowSnapshot(ctx context.Context, payload []byte, nodeCount, edgeCount int, reason string) (*FlowSnapshot, error)
	PruneFlowSnapshots(ctx context.Context, olderThan time.Time) (int64, error)
	ListFlowProposals(ctx context.Context, f FlowProposalFilter) ([]*FlowProposal, int, error)
	CountFlowProposalFacets(ctx context.Context) (*FlowProposalFacets, error)
	SearchSimilarProposals(ctx context.Context, query string, limit int) ([]*FlowProposal, error)
	SetFlowNodeTag(ctx context.Context, id int64, tag string) error
	SetFlowNodeStatus(ctx context.Context, id int64, status string) error
	PromoteFlowNode(ctx context.Context, id int64, actorUserID *int) error
}

// Deps are the host seams — chrome, the viewer, and the sanitising
// renderers. Everything data-shaped is plugin-owned, so this is the whole
// contract.
type Deps struct {
	// RenderPage wraps a rendered fragment in the site chrome.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)
	// RenderPagination renders the host's pager from totals; the base URL
	// arrives ready-suffixed with ? or &.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML
	// CSRFToken feeds the two admin pages' POST forms (the /flow editor's
	// fetches read the csrf_tok cookie client-side and need no seam).
	CSRFToken func(c *gin.Context) string
	// Viewer is who is looking; nil for anonymous.
	Viewer func(c *gin.Context) *Viewer
	// RelativeTime is the site's time wording.
	RelativeTime func(v any) string
	// SanitizeForum is the host's forum-policy sanitiser, applied to the
	// plugin's own goldmark output (mockup notes, proposal descriptions).
	// It crosses so there is exactly one allow-list.
	SanitizeForum func(html string) string
	// RenderForumMarkdown is the host's markdown+sanitise pipeline for
	// comment bodies.
	RenderForumMarkdown func(src string) template.HTML
	// Files is where images attached to a request are stored, under the
	// "proposal-uploads/" namespace. OPTIONAL: nil disables attachments
	// entirely — the upload control is not rendered and the endpoint
	// refuses. It is optional because it is the only dep that grants
	// members write access to storage, so a host should have to opt in
	// rather than inherit it by upgrading.
	Files blob.Store
}

func (d *Deps) ok() bool {
	return d != nil &&
		d.RenderPage != nil && d.RenderPagination != nil && d.CSRFToken != nil &&
		d.Viewer != nil && d.RelativeTime != nil &&
		d.SanitizeForum != nil && d.RenderForumMarkdown != nil
}

var deps *Deps

// SetDeps hands the plugin its host seams. Called once, before core.Boot,
// in web/all processes.
func SetDeps(d Deps) { deps = &d }
