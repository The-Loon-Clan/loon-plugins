// The /flow collaborative node-graph editor.
//
// Phase 1 surface: server-rendered HTML page that loads the current
// graph via GET /flow/data, plus per-element CRUD endpoints the
// front-end calls on edit. Phase 2 will layer a WebSocket endpoint on
// top and broadcast the same ops across connected clients; the REST
// shape here is the same op vocabulary so the front-end can seamlessly
// switch from "fetch + redraw" to "ws push" without changing payloads.
//
// Permission model:
//   - View: any logged-in user (anonymous in public mode redirected at
//     middleware layer).
//   - Add node / add edge / move own nodes: any logged-in user.
//   - Edit / delete a node:
//     owner of that node          → allowed
//     node.locked == true         → only RoleMod+
//     else (someone else's node)  → only RoleMod+
//   - Same rules for edges.
//
// Locked nodes are the hand-seeded canonical pipeline (migration 178);
// users add proposal nodes around them.
package roadmap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/yuin/goldmark"
	gmext "github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// flowMD renders user-authored markdown for mockup-node Notes through
// the same goldmark + sanitize.Forum pipeline forum posts use. Same
// allowlist (inline formatting, lists, code, blockquote, links — no
// raw HTML, no images, no headings) so a stray <script> in someone's
// Notes can't slip past the sanitizer.
var flowMD = goldmark.New(
	goldmark.WithExtensions(gmext.GFM),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

func renderFlowMarkdown(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := flowMD.Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return sanitizeForum(buf.String())
}

// processMockupData rewrites a mockup node's incoming data_json with a
// pre-rendered `markdown_html` field alongside the source `markdown`,
// so the frontend can drop the rendered HTML into the inspector
// without shipping a markdown library to the client. Non-mockup kinds
// are returned untouched. A bad / missing JSON object also returns
// untouched — the storage layer accepts any opaque blob.
func processMockupData(kind string, raw json.RawMessage) json.RawMessage {
	if kind != "mockup" || len(raw) == 0 {
		return raw
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	mdSrc, _ := obj["markdown"].(string)
	obj["markdown_html"] = renderFlowMarkdown(mdSrc)
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// Page — GET /flow
//
// Renders the canvas shell. The actual graph data is fetched async via
// GET /flow/data so the editor can re-load after a WebSocket reconnect
// without a full page redraw.
func (h *Handlers) Page(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	h.render(c, http.StatusOK, "Feature Requests", "flow.html", gin.H{
		"ActiveNav": "community",
	})
}

// GraphData — GET /flow/data
//
// Returns the entire alive graph in one shot. Cached briefly via
// Cache-Control: private,max-age=5 so a quick double-load doesn't hit
// the DB twice; ops applied in between will arrive via WebSocket
// (phase 2) so the small staleness window is acceptable.
func (h *Handlers) GraphData(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	g, err := h.flow.GetFlowGraph(c.Request.Context())
	if err != nil {
		h.errs.HandlerError(c, "flow/get-graph", err)
		return
	}
	c.Header("Cache-Control", "private,max-age=5")
	c.JSON(http.StatusOK, g)
}

// nodePayload mirrors the JSON the editor sends on node add/update.
// Pointers distinguish "field omitted" from "field set to zero" — the
// fill-if-set semantics in storage.UpdateFlowNode rely on that.
type nodePayload struct {
	Kind        string          `json:"kind"`
	Label       *string         `json:"label"`
	Description *string         `json:"description"`
	X           *float64        `json:"x"`
	Y           *float64        `json:"y"`
	Data        json.RawMessage `json:"data"`
	Locked      *bool           `json:"locked"` // mod-only; ignored from non-mod requests
	// Tag — submitter-chosen category. Allowlist enforced in
	// CreateNode/UpdateNode against proposalTagAllowlist.
	Tag *string `json:"tag"`
}

// proposalTagAllowlist limits the tag column to a known set, so the
// listing's filter pills stay short. Stored as ”/empty when no tag
// is selected. Update both this list and the template's pill row
// together if a new category is added.
var proposalTagAllowlist = map[string]bool{
	"":            true,
	"ui":          true,
	"bug":         true,
	"feature":     true,
	"performance": true,
	"other":       true,
	// Not a tag: the filter value meaning "has no tag". Requests filed
	// before the category picker existed carry an empty tag, and "" is
	// already spoken for as "any tag" — so without a distinct word for it
	// those rows are reachable from no pill at all.
	"untagged": true,
}

// statusLabel turns a stored status into what a member reads.
//
// The statuses are stored snake_case, and the page used to print them raw
// with one inline special case for in_progress — so every other chip read
// as a database value ("declined" in lower case beside "In Progress"). One
// function means the listing, the sidebar and the filter pills cannot drift
// from each other.
func statusLabel(s string) string {
	switch s {
	case "in_progress":
		return "In Progress"
	case "open":
		return "Open"
	case "planned":
		return "Planned"
	case "done":
		return "Done"
	case "declined":
		return "Declined"
	case "":
		return "Open" // the column's default; a blank chip reads as broken
	}
	return s
}

// tagLabel is the same idea for categories. UI is an initialism and looks
// wrong title-cased, which is the whole reason this is not strings.Title.
func tagLabel(s string) string {
	switch s {
	case "ui":
		return "UI"
	case "bug":
		return "Bug"
	case "feature":
		return "Feature"
	case "performance":
		return "Performance"
	case "other":
		return "Other"
	}
	return s
}

// sidebarRecentCount is how many requests the "Recent Feature Requests"
// panel shows beside the form. Short on purpose: it is there to show the
// page is alive and what a good request looks like, not to be browsed --
// the full, filterable list is directly below it.
const sidebarRecentCount = 5

// proposalStatusAllowlist limits the status column to a known set.
// 'open' is the default for new rows; the rest are mod-managed.
var proposalStatusAllowlist = map[string]bool{
	"open":        true,
	"planned":     true,
	"in_progress": true,
	"done":        true,
	"declined":    true,
}

// CreateNode — POST /flow/node
func (h *Handlers) CreateNode(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	var p nodePayload
	if err := c.BindJSON(&p); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid JSON")
		return
	}
	uid := user.ID
	in := &FlowNode{
		Kind:      p.Kind,
		CreatedBy: &uid,
	}
	if p.Label != nil {
		in.Label = *p.Label
	}
	if p.Description != nil {
		in.Description = *p.Description
	}
	if p.X != nil {
		in.X = *p.X
	}
	if p.Y != nil {
		in.Y = *p.Y
	}
	if len(p.Data) > 0 {
		in.DataJSON = []byte(processMockupData(in.Kind, p.Data))
	}
	if p.Tag != nil {
		t := strings.ToLower(strings.TrimSpace(*p.Tag))
		if !proposalTagAllowlist[t] {
			jsonError(c, http.StatusBadRequest, "invalid tag")
			return
		}
		in.Tag = t
	}
	out, err := h.flow.CreateFlowNode(c.Request.Context(), in)
	if err != nil {
		h.errs.HandlerError(c, "flow/create-node", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "node": out})
}

// UpdateNode — PATCH /flow/node/:id
//
// Partial update. Move-only and rename-only ops are the common case
// and they hit a single column each.
func (h *Handlers) UpdateNode(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := h.flow.GetFlowNode(c.Request.Context(), id)
	if err != nil {
		jsonError(c, http.StatusNotFound, "node not found")
		return
	}
	if !canModifyFlowNode(user, existing) {
		jsonError(c, http.StatusForbidden, "not allowed (locked or not yours)")
		return
	}
	var p nodePayload
	if err := c.BindJSON(&p); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Locked toggle is mod-only — the schema doesn't enforce this
	// (locked is a plain bool), so we strip the field from non-mod
	// requests rather than returning 403, since most clients don't
	// send it at all.
	if p.Locked != nil && !user.Mod {
		p.Locked = nil
	}
	var dataBytes []byte
	if len(p.Data) > 0 {
		// existing.Kind is the authoritative kind here — clients can
		// PATCH mockup data on a non-mockup node by mistake; the kind
		// gate prevents us from polluting that node's data_json with a
		// rendered Notes blob.
		dataBytes = []byte(processMockupData(existing.Kind, p.Data))
	}
	if err := h.flow.UpdateFlowNode(c.Request.Context(), id, p.Label, p.Description, p.X, p.Y, dataBytes, p.Locked); err != nil {
		h.errs.HandlerError(c, "flow/update-node", err)
		return
	}
	updated, _ := h.flow.GetFlowNode(c.Request.Context(), id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "node": updated})
}

// DeleteNode — DELETE /flow/node/:id
func (h *Handlers) DeleteNode(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := h.flow.GetFlowNode(c.Request.Context(), id)
	if err != nil {
		jsonError(c, http.StatusNotFound, "node not found")
		return
	}
	if !canModifyFlowNode(user, existing) {
		jsonError(c, http.StatusForbidden, "not allowed (locked or not yours)")
		return
	}
	edgeCount, err := h.flow.DeleteFlowNode(c.Request.Context(), id)
	if err != nil {
		h.errs.HandlerError(c, "flow/delete-node", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted_edges": edgeCount})
}

// edgePayload mirrors the JSON for add/delete-edge.
//
// SourcePort / TargetPort name the specific ports on each end. Empty
// string means "default port" (no port discipline) so legacy clients
// that don't send the field still work — see migration 186 for the
// data-model rationale.
type edgePayload struct {
	SourceID   int64  `json:"source_id"`
	TargetID   int64  `json:"target_id"`
	SourcePort string `json:"source_port"`
	TargetPort string `json:"target_port"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
}

// CreateEdge — POST /flow/edge
func (h *Handlers) CreateEdge(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	var p edgePayload
	if err := c.BindJSON(&p); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.SourceID == 0 || p.TargetID == 0 {
		jsonError(c, http.StatusBadRequest, "source and target required")
		return
	}
	if p.SourceID == p.TargetID {
		jsonError(c, http.StatusBadRequest, "self-loop not allowed")
		return
	}
	uid := user.ID
	in := &FlowEdge{
		SourceID:   p.SourceID,
		TargetID:   p.TargetID,
		SourcePort: p.SourcePort,
		TargetPort: p.TargetPort,
		Label:      p.Label,
		Kind:       p.Kind,
		CreatedBy:  &uid,
	}
	out, err := h.flow.CreateFlowEdge(c.Request.Context(), in)
	if err != nil {
		h.errs.HandlerError(c, "flow/create-edge", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "edge": out})
}

// DeleteEdge — DELETE /flow/edge/:id
func (h *Handlers) DeleteEdge(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := h.flow.GetFlowEdge(c.Request.Context(), id)
	if err != nil {
		jsonError(c, http.StatusNotFound, "edge not found")
		return
	}
	if !canModifyFlowEdge(user, existing) {
		jsonError(c, http.StatusForbidden, "not allowed (locked or not yours)")
		return
	}
	if err := h.flow.DeleteFlowEdge(c.Request.Context(), id); err != nil {
		h.errs.HandlerError(c, "flow/delete-edge", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// canModifyFlowNode encodes the permission rule used by every write
// endpoint: mods can touch anything; ordinary users can only touch
// nodes they created, and only if the node isn't locked.
func canModifyFlowNode(user *Viewer, n *FlowNode) bool {
	if user == nil {
		return false
	}
	if user.Mod {
		return true
	}
	if n.Locked {
		return false
	}
	return n.CreatedBy != nil && *n.CreatedBy == user.ID
}

// canModifyFlowEdge mirrors canModifyFlowNode for edges.
func canModifyFlowEdge(user *Viewer, e *FlowEdge) bool {
	if user == nil {
		return false
	}
	if user.Mod {
		return true
	}
	if e.Locked {
		return false
	}
	return e.CreatedBy != nil && *e.CreatedBy == user.ID
}

// RecentProposals — GET /flow/proposals
//
// Renders the Feature Requests page: forum-style listing with tag +
// status filters, sort, and pagination. Reads ?tag=, ?status=,
// ?sort=, ?page= from the query and falls back to defaults when
// missing. Same data shape regardless of viewer role; the template
// gates mod-only controls (status setter, retag, promote) on
// .IsAdmin.
func (h *Handlers) RecentProposals(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	tag := strings.ToLower(strings.TrimSpace(c.Query("tag")))
	if !proposalTagAllowlist[tag] {
		tag = ""
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	// '' = all, otherwise must be in the allowlist; bad input falls
	// back to no-filter rather than 400, since the value comes from a
	// pill click and a mismatch usually means stale tab.
	if status != "" && !proposalStatusAllowlist[status] {
		status = ""
	}
	sort := strings.ToLower(strings.TrimSpace(c.Query("sort")))
	switch sort {
	case "top", "active", "newest":
		// keep as-is
	default:
		sort = "newest"
	}
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize := 20
	// "My Requests". Scoped to the viewer's own id from the session rather
	// than to a user id in the query string -- ?mine=<someone else> would
	// otherwise be an author filter for any account, which is not what the
	// control says it does.
	mine := 0
	if c.Query("mine") != "" {
		if u := h.deps.Viewer(c); u != nil {
			mine = u.ID
		}
	}
	rows, total, err := h.flow.ListFlowProposals(c.Request.Context(), FlowProposalFilter{
		Tag:      tag,
		Status:   status,
		Sort:     sort,
		Mine:     mine,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		h.errs.HandlerError(c, "flow/proposals", err)
		return
	}
	if c.GetHeader("Accept") == "application/json" || c.Query("format") == "json" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "proposals": rows, "total": total})
		return
	}
	if page <= 0 {
		page = 1
	}
	// Carry filters through pagination links so clicking page 2 keeps
	// the active tag/status/sort. The base URL gets the query string
	// appended before the host renders the pager.
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if status != "" {
		q.Set("status", status)
	}
	if sort != "newest" {
		q.Set("sort", sort)
	}
	if mine != 0 {
		q.Set("mine", "1")
	}
	baseURL := "/flow/proposals?"
	if encoded := q.Encode(); encoded != "" {
		baseURL = "/flow/proposals?" + encoded + "&"
	}
	pagination := h.deps.RenderPagination(page, pageSize, total, baseURL)
	currentUserID := 0
	if u := h.deps.Viewer(c); u != nil {
		currentUserID = u.ID
	}
	// Facet counts drive the filter strip. Best-effort: the strip degrades
	// to pills without counts, which is what it was before, and that is not
	// worth failing a page over.
	facets, ferr := h.flow.CountFlowProposalFacets(c.Request.Context())
	if ferr != nil {
		facets = &FlowProposalFacets{Tags: map[string]int{}, Statuses: map[string]int{}}
	}
	// The sidebar's "Recent Feature Requests" is deliberately unfiltered --
	// it is orientation for somebody composing a request, showing what the
	// place looks like, so narrowing it by the filters they happen to have
	// clicked would defeat the point (and show nothing on an empty filter).
	recent := rows
	if tag != "" || status != "" || mine != 0 || sort != "newest" {
		if r, _, err := h.flow.ListFlowProposals(c.Request.Context(), FlowProposalFilter{
			Sort: "newest", Page: 1, PageSize: sidebarRecentCount,
		}); err == nil {
			recent = r
		}
	}
	if len(recent) > sidebarRecentCount {
		recent = recent[:sidebarRecentCount]
	}
	h.render(c, http.StatusOK, "Feature Requests", "flow_proposals.html", gin.H{
		"ActiveNav":     "community",
		"Proposals":     rows,
		"Recent":        recent,
		"Pagination":    pagination,
		"FilterTag":     tag,
		"FilterStatus":  status,
		"FilterSort":    sort,
		"FilterMine":    mine != 0,
		"Facets":        facets,
		"TotalCount":    total,
		"CurrentUserID": currentUserID,
		"UploadsOn":     deps.Files != nil,
	})
}

// SimilarProposals — GET /flow/proposals/similar?q=<title>
//
// Powers the duplicate-detection hint on the new-request form. JS
// debounces typing and asks here while the user is composing the
// title; the response is a small JSON array of matching rows so the
// form can show "do you mean…" before the user clicks Submit.
func (h *Handlers) SimilarProposals(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	if len(q) < 3 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "matches": []any{}})
		return
	}
	rows, err := h.flow.SearchSimilarProposals(c.Request.Context(), q, 5)
	if err != nil {
		h.errs.HandlerError(c, "flow/proposals/similar", err)
		return
	}
	type slim struct {
		ID        int64  `json:"id"`
		Label     string `json:"label"`
		Tag       string `json:"tag"`
		Status    string `json:"status"`
		VoteCount int    `json:"vote_count"`
		Username  string `json:"username"`
	}
	out := make([]slim, 0, len(rows))
	for _, r := range rows {
		out = append(out, slim{
			ID: r.ID, Label: r.Label, Tag: r.Tag, Status: r.Status,
			VoteCount: r.VoteCount, Username: r.Username,
		})
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "matches": out})
}

// ProposalDetails — GET /flow/proposals/:id/details
//
// Returns the full payload for one proposal: rendered description
// HTML (markdown → goldmark → sanitize.Forum) and the comment thread.
// The listing template hits this when a row is expanded inline so we
// don't have to ship 1000 descriptions in the initial page payload.
func (h *Handlers) ProposalDetails(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	node, err := h.flow.GetFlowNode(c.Request.Context(), id)
	if err != nil || node == nil || node.DeletedAt != nil {
		jsonError(c, http.StatusNotFound, "not found")
		return
	}
	comments, _ := h.flow.GetFlowComments(c.Request.Context(), id)
	type renderedComment struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		BodyHTML  string `json:"body_html"`
		CreatedAt string `json:"created_at"`
	}
	rcs := make([]renderedComment, 0, len(comments))
	for _, cm := range comments {
		rcs = append(rcs, renderedComment{
			ID:        cm.ID,
			Username:  cm.Username,
			BodyHTML:  string(h.deps.RenderForumMarkdown(cm.Body)),
			CreatedAt: cm.CreatedAt.Format("Jan 02, 15:04"),
		})
	}
	// Cross-links to roadmap items + changelog entries that
	// reference this node (migration 232's flow_node_id columns).
	// The expanded panel renders these as "Tracked on roadmap as
	// 'X' (in flight)" / "Shipped as 'Y' on 2026-05-10" lines so a
	// graph viewer can click through to the public roadmap /
	// changelog surfaces.
	type linkedRoadmap struct {
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	type linkedChangelog struct {
		ID         int64  `json:"id"`
		Title      string `json:"title"`
		Category   string `json:"category"`
		ReleasedAt string `json:"released_at"`
	}
	var roadmap []linkedRoadmap
	if items, err := h.store.ListRoadmapItemsByFlowNode(c.Request.Context(), id); err == nil {
		for _, r := range items {
			roadmap = append(roadmap, linkedRoadmap{ID: r.ID, Title: r.Title, Status: r.Status})
		}
	}
	var changelog []linkedChangelog
	if entries, err := h.store.ListChangelogEntriesByFlowNode(c.Request.Context(), id); err == nil {
		for _, e := range entries {
			changelog = append(changelog, linkedChangelog{
				ID:         e.ID,
				Title:      e.Title,
				Category:   e.Category,
				ReleasedAt: e.ReleasedAt.Format("2006-01-02"),
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":               true,
		"id":               node.ID,
		"label":            node.Label,
		"tag":              node.Tag,
		"status":           node.Status,
		"description_html": string(h.deps.RenderForumMarkdown(node.Description)),
		"comments":         rcs,
		"roadmap":          roadmap,
		"changelog":        changelog,
	})
}

// SetNodeTag — POST /flow/node/:id/tag — mod-only retag.
func (h *Handlers) SetNodeTag(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "mod only"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	tag := strings.ToLower(strings.TrimSpace(c.PostForm("tag")))
	if !proposalTagAllowlist[tag] {
		jsonError(c, http.StatusBadRequest, "invalid tag")
		return
	}
	if err := h.flow.SetFlowNodeTag(c.Request.Context(), id, tag); err != nil {
		h.errs.HandlerError(c, "flow/set-tag", err)
		return
	}
	jsonOK(c, gin.H{"tag": tag})
}

// SetNodeStatus — POST /flow/node/:id/status — mod-only lifecycle update.
func (h *Handlers) SetNodeStatus(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "mod only"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	status := strings.ToLower(strings.TrimSpace(c.PostForm("status")))
	if !proposalStatusAllowlist[status] {
		jsonError(c, http.StatusBadRequest, "invalid status")
		return
	}
	if err := h.flow.SetFlowNodeStatus(c.Request.Context(), id, status); err != nil {
		h.errs.HandlerError(c, "flow/set-status", err)
		return
	}
	jsonOK(c, gin.H{"status": status})
}

// MockupsIndex — GET /flow/mockups
//
// Wiki-style discovery page for every UI-mockup node in the graph.
// Each card shows a sandboxed iframe thumbnail, label, vote count,
// author, and (when set) the canonical page the mockup is proposing
// to change. Sortable by newest or by votes via the ?sort= query
// arg; filterable to "edits a canonical" vs "stand-alone" via
// ?scope=. Useful when the canvas has dozens of mockups and you want
// the top-voted ones up front without scrolling around.
//
// Index is the heavy-lift surface; per-mockup detail lives at
// /flow/mockup/:id (MockupDetail handler).
func (h *Handlers) MockupsIndex(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	sort := c.DefaultQuery("sort", "newest")
	if sort != "newest" && sort != "votes" {
		sort = "newest"
	}
	scope := c.DefaultQuery("scope", "all")
	if scope != "all" && scope != "edits" && scope != "standalone" {
		scope = "all"
	}
	rows, err := h.flow.GetMockupNodes(c.Request.Context(), sort)
	if err != nil {
		h.errs.HandlerError(c, "flow/mockups", err)
		return
	}
	// Server-side scope filter, since we already have the rows in
	// memory and the dataset is small. The DB-side query stays
	// simple and one-shot; we slice down here.
	filtered := rows
	if scope != "all" {
		filtered = filtered[:0]
		for _, r := range rows {
			isEdit := r.ParentNodeID != nil && *r.ParentNodeID != 0
			if scope == "edits" && isEdit {
				filtered = append(filtered, r)
			} else if scope == "standalone" && !isEdit {
				filtered = append(filtered, r)
			}
		}
	}
	h.render(c, http.StatusOK, "UI Mockups", "flow_mockups.html", gin.H{
		"ActiveNav": "community",
		"Mockups":   filtered,
		"Sort":      sort,
		"Scope":     scope,
	})
}

// MockupDetail — GET /flow/mockup/:id
//
// Permalink + standalone view for a single mockup. Renders the HTML
// at full size, the rendered Notes (markdown), the comment thread,
// the linked-canonical card with side-by-side iframe diff if this
// mockup is proposing an edit, plus an "Open in graph" link that
// jumps back to /flow#node-<id> with the canvas auto-centered.
func (h *Handlers) MockupDetail(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()
	node, err := h.flow.GetFlowNode(ctx, id)
	if err != nil || node == nil || node.DeletedAt != nil {
		c.String(http.StatusNotFound, "mockup not found")
		return
	}
	if node.Kind != "mockup" {
		c.String(http.StatusNotFound, "not a mockup")
		return
	}

	// Pull the linked canonical (if any) so the template can render
	// a parent card + side-by-side iframe diff.
	var parent *FlowNode
	if node.ParentNodeID != nil && *node.ParentNodeID != 0 {
		parent, _ = h.flow.GetFlowNode(ctx, *node.ParentNodeID)
		if parent != nil && parent.DeletedAt != nil {
			parent = nil // soft-deleted parent shouldn't appear
		}
	}

	comments, _ := h.flow.GetFlowComments(ctx, id)

	user := h.deps.Viewer(c)
	var voted bool
	if user != nil {
		voted, _ = h.flow.HasVotedForFlowNode(ctx, id, user.ID)
	}

	// Pull HTML / notes_html out of data_json so the template doesn't
	// need a template func to crack the JSONB blob. Notes already
	// arrive sanitized via processMockupData (goldmark + sanitize.Forum)
	// so it's safe to render as HTML; the live HTML is rendered in a
	// sandbox="" iframe further down.
	mockupHTML, _, mockupNotesHTML := extractMockupBits(node)
	parentHTML := ""
	parentNotes := ""
	if parent != nil {
		parentHTML, _, parentNotes = extractMockupBits(parent)
	}
	h.render(c, http.StatusOK, fmt.Sprintf("Mockup #%d — %s", node.ID, node.Label), "flow_mockup_detail.html", gin.H{
		"ActiveNav":       "community",
		"Node":            node,
		"Parent":          parent,
		"Comments":        comments,
		"Voted":           voted,
		"MockupHTML":      template.HTML(mockupHTML),
		"MockupNotesHTML": template.HTML(mockupNotesHTML),
		"ParentHTML":      template.HTML(parentHTML),
		"ParentNotesHTML": template.HTML(parentNotes),
	})
}

// extractMockupBits cracks open a flow node's data_json blob and
// returns its mockup HTML source, raw markdown source, and the
// pre-rendered + sanitized markdown HTML. Empty strings on any
// missing field. Used by the permalink page so the template doesn't
// need to know about JSONB.
func extractMockupBits(n *FlowNode) (html string, markdown string, markdownHTML string) {
	if n == nil || len(n.DataJSON) == 0 {
		return "", "", ""
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(n.DataJSON, &obj); err != nil {
		return "", "", ""
	}
	if v, ok := obj["html"].(string); ok {
		html = v
	}
	if v, ok := obj["markdown"].(string); ok {
		markdown = v
	}
	if v, ok := obj["markdown_html"].(string); ok {
		markdownHTML = v
	}
	return
}

// NodeHistory — GET /flow/node/:id/history
//
// Returns the audit trail of pre-merge / pre-promote snapshots for
// this node, newest first. Used by the inspector's "History" button
// to render a timeline of past states. The snapshot blobs are
// included so the client can render an inline diff against the
// current row without a follow-up request per revision.
func (h *Handlers) NodeHistory(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	rows, err := h.flow.GetFlowNodeRevisions(c.Request.Context(), id, 50)
	if err != nil {
		h.errs.HandlerError(c, "flow/node-history", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "revisions": rows})
}

// ListNodeComments — GET /flow/node/:id/comments
//
// Returns the thread on one node, oldest first. Used by the editor's
// inspector when the user opens a node, so they see the existing
// comments without needing a WebSocket.
func (h *Handlers) ListNodeComments(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	rows, err := h.flow.GetFlowComments(c.Request.Context(), id)
	if err != nil {
		h.errs.HandlerError(c, "flow/list-comments", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "comments": rows})
}

// AddNodeComment — POST /flow/node/:id/comment
//
// Body: {"body": "..."}. 2000-char cap mirrors the existing
// nzb_comments contract. Auth required; the user_id and username are
// taken from the session, never the request body.
func (h *Handlers) AddNodeComment(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var p struct {
		Body string `json:"body"`
	}
	if err := c.BindJSON(&p); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid JSON")
		return
	}
	body := strings.TrimSpace(p.Body)
	if body == "" {
		jsonError(c, http.StatusBadRequest, "comment body required")
		return
	}
	if len(body) > 2000 {
		body = body[:2000]
	}
	// Confirm the node exists (and isn't soft-deleted) before accepting
	// the comment — same guard the WS hub used.
	if _, err := h.flow.GetFlowNode(c.Request.Context(), id); err != nil {
		jsonError(c, http.StatusNotFound, "node not found")
		return
	}
	cmt, err := h.flow.AddFlowComment(c.Request.Context(), id, user.ID, user.Username, body)
	if err != nil {
		h.errs.HandlerError(c, "flow/add-comment", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "comment": cmt})
}

// PromoteNode — POST /flow/node/:id/promote
//
// Mod-only. Two paths depending on whether the proposal is targeting
// an existing canonical node:
//
//   - parent_node_id IS NULL → flip the proposal to locked=true. It
//     becomes its own canonical entry. Same behaviour as before.
//
//   - parent_node_id IS set → MERGE the proposal's content onto its
//     parent (label, description, data_json copied), soft-delete the
//     proposal with merged_into_id pointing at the canonical it was
//     absorbed into. Wiki-style "your edit was accepted" outcome.
//
// The fork itself never has its own children to worry about (forks
// of forks aren't supported in this iteration); merge always lands
// on the original canonical.
func (h *Handlers) PromoteNode(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		jsonError(c, http.StatusForbidden, "mod required")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}

	ctx := c.Request.Context()
	node, err := h.flow.GetFlowNode(ctx, id)
	if err != nil {
		jsonError(c, http.StatusNotFound, "node not found")
		return
	}
	uid := user.ID
	if node.ParentNodeID != nil && *node.ParentNodeID > 0 {
		if err := h.flow.MergeFlowProposalIntoParent(ctx, id, &uid); err != nil {
			h.errs.HandlerError(c, "flow/promote/merge", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "merged_into": *node.ParentNodeID})
		return
	}
	if err := h.flow.PromoteFlowNode(ctx, id, &uid); err != nil {
		h.errs.HandlerError(c, "flow/promote", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ProposeChange — POST /flow/node/:id/propose-change
//
// Forks an existing node into a user-owned proposal. The fork is a
// fresh row with parent_node_id pointing at the original; the inspector
// renders it with a side-by-side diff against the parent so reviewers
// can see what changed. Promotion (mod-merge) copies the fork's
// content onto the parent.
//
// Anyone signed in can fork any node, including locked canonical
// seeds. Locked seeds are exactly what most edit-proposals will
// target (rename a page, tweak a hover-hint, propose a new port).
func (h *Handlers) ProposeChange(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	fork, err := h.flow.ForkFlowNode(c.Request.Context(), id, user.ID)
	if err != nil {
		h.errs.HandlerError(c, "flow/propose-change", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "node": fork})
}

// VoteNode — POST /flow/node/:id/vote
//
// Toggles the signed-in user's vote on a node and returns the new
// vote count + whether the call inserted (added=true) or removed
// (added=false) the vote. Mirrors VoteForRequest's response shape so
// the frontend can reuse the same toggle UI pattern.
//
// Authors can't vote on their own requests — that's an obvious
// gaming vector and a confusing affordance besides. The frontend
// also greys the button out, but the server check is the load-bearing
// one.
func (h *Handlers) VoteNode(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	node, err := h.flow.GetFlowNode(c.Request.Context(), id)
	if err != nil || node == nil || node.DeletedAt != nil {
		jsonError(c, http.StatusNotFound, "node not found")
		return
	}
	if node.CreatedBy != nil && *node.CreatedBy == user.ID {
		jsonError(c, http.StatusBadRequest, "you can't vote on your own request")
		return
	}
	count, added, err := h.flow.VoteForFlowNode(c.Request.Context(), id, user.ID)
	if err != nil {
		h.errs.HandlerError(c, "flow/vote", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": count, "added": added})
}
