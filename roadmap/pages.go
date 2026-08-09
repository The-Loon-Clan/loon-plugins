package roadmap

// Public /help/roadmap (tabbed: Roadmap + Changelog) page, plus
// admin CRUD (/admin/roadmap and /admin/changelog).
//
// /help/changelog is kept as a 301 redirect to /help/roadmap?tab=changelog
// so external links still resolve.
//
// Both surfaces are DB-backed as of migration 232. The previous
// code-baked roadmap slice has been retired — admins now edit via
// the admin UI, and the bootstrap of the existing list happens via
// one-time SQL INSERTs against the live DB.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lib/pq"
)

// RoadmapPage renders GET /help/roadmap — a single tabbed page that
// shows BOTH forward-looking roadmap items (in_flight + backlog) AND
// the shipped changelog. The active tab is driven by ?tab=changelog;
// anything else defaults to the roadmap tab.
//
// Public, anonymous-accessible so visitors can see the project's
// direction before signing up. Changelog entries are paginated within
// the changelog tab (?page=N).
func (h *Handlers) RoadmapPage(c *gin.Context) {
	ctx := c.Request.Context()

	items, err := h.store.ListRoadmapItems(ctx, false)
	if err != nil {
		h.errs.Report(ctx, "help/roadmap-list", err)
	}
	var inFlight, backlog []*RoadmapItem
	for _, r := range items {
		switch r.Status {
		case RoadmapStatusInFlight:
			inFlight = append(inFlight, r)
		case RoadmapStatusBacklog:
			backlog = append(backlog, r)
		}
	}

	const pageSize = 100
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	entries, total, err := h.store.ListChangelogEntries(ctx, pageSize, offset)
	if err != nil {
		h.errs.Report(ctx, "help/changelog-list", err)
	}

	// Bucket changelog by released_at so the template renders one date
	// header per group. entries is already sorted DESC.
	type dateBucket struct {
		Date    string
		Entries []*ChangelogEntry
	}
	var buckets []*dateBucket
	var current *dateBucket
	for _, e := range entries {
		key := e.ReleasedAt.Format("2006-01-02")
		if current == nil || current.Date != key {
			current = &dateBucket{Date: key}
			buckets = append(buckets, current)
		}
		current.Entries = append(current.Entries, e)
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	activeTab := "roadmap"
	if c.Query("tab") == "changelog" {
		activeTab = "changelog"
	}

	h.render(c, http.StatusOK, "Roadmap & Changelog", "help_roadmap.html", gin.H{
		"PageTitle":  "Roadmap & Changelog",
		"ActiveNav":  "support",
		"ActiveTab":  activeTab,
		"InFlight":   inFlight,
		"Backlog":    backlog,
		"Buckets":    buckets,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
	})
}

// ChangelogPage handles GET /help/changelog — kept as a permanent
// redirect to the combined tabbed page so existing external links
// (Discord, search engines) still land somewhere sensible. The
// ?page= query is preserved.
func (h *Handlers) ChangelogPage(c *gin.Context) {
	target := "/help/roadmap?tab=changelog"
	if p := strings.TrimSpace(c.Query("page")); p != "" {
		target += "&page=" + p
	}
	c.Redirect(http.StatusMovedPermanently, target)
}

// RoadmapGraphJSON powers the "Graph" view toggle on
// /help/roadmap. The page renders Cytoscape against this payload
// and draws every alive flow node that's linked to at least one
// roadmap item OR changelog entry, plus the edges between those
// nodes. Per-node `roadmap` / `changelog` counts drive the badge
// + size scaling so users can spot the most-active areas of the
// graph at a glance.
//
// Public — same audience as the parent page. No PII in the
// payload; labels and counts only.
func (h *Handlers) RoadmapGraphJSON(c *gin.Context) {
	ctx := c.Request.Context()
	g, err := h.flow.GetFlowGraph(ctx)
	if err != nil {
		h.errs.HandlerError(c, "help/roadmap-graph", err)
		return
	}
	counts, err := h.store.GetFlowNodeLinkCounts(ctx)
	if err != nil {
		h.errs.HandlerError(c, "help/roadmap-graph-counts", err)
		return
	}
	countMap := make(map[int64][2]int, len(counts))
	for _, r := range counts {
		countMap[r.NodeID] = [2]int{r.RoadmapCount, r.ChangelogCount}
	}
	type gNode struct {
		ID             int64   `json:"id"`
		Label          string  `json:"label"`
		Kind           string  `json:"kind"`
		Status         string  `json:"status"`
		Tag            string  `json:"tag"`
		X              float64 `json:"x"`
		Y              float64 `json:"y"`
		RoadmapCount   int     `json:"roadmap_count"`
		ChangelogCount int     `json:"changelog_count"`
	}
	type gEdge struct {
		ID       int64 `json:"id"`
		SourceID int64 `json:"source_id"`
		TargetID int64 `json:"target_id"`
	}
	// Return the FULL alive graph — every node, every edge — and
	// attach per-node counts. Unlinked nodes get rendered in a muted
	// style by the frontend, so the user can see the site topology
	// as a whole instead of a disconnected island of linked nodes.
	// "No linked nodes yet" is computed across the entire set, not
	// per-node.
	nodes := make([]gNode, 0, len(g.Nodes))
	totalLinks := 0
	for _, n := range g.Nodes {
		cnt := countMap[n.ID] // zero value [0,0] when unlinked
		nodes = append(nodes, gNode{
			ID: n.ID, Label: n.Label, Kind: n.Kind,
			Status: n.Status, Tag: n.Tag,
			X: n.X, Y: n.Y,
			RoadmapCount: cnt[0], ChangelogCount: cnt[1],
		})
		totalLinks += cnt[0] + cnt[1]
	}
	edges := make([]gEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, gEdge{ID: e.ID, SourceID: e.SourceID, TargetID: e.TargetID})
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"nodes":       nodes,
		"edges":       edges,
		"total_links": totalLinks,
	})
}

// RoadmapGraphNodeJSON powers the side-panel when a user clicks
// a node in the Graph view. Returns the linked roadmap items +
// changelog entries for that node so the panel can render two
// cross-link sections without a second round-trip.
func (h *Handlers) RoadmapGraphNodeJSON(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	// Canonical/seed flow nodes carry NEGATIVE ids (the /flow editor
	// allocates them that way to keep the wiki/proposal range positive).
	// Only reject id == 0 or a malformed param — both signs are valid.
	if err != nil || id == 0 {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()
	node, err := h.flow.GetFlowNode(ctx, id)
	if err != nil || node == nil || node.DeletedAt != nil {
		jsonError(c, http.StatusNotFound, "not found")
		return
	}
	// Only the canonical site-map nodes are intended for public
	// surfacing. User-proposed nodes (kind="user-proposal",
	// kind="comment", kind="mockup") and any proposal-as-edit row
	// (parent_node_id set) belong to the /flow editor's workflow
	// and may carry in-flight draft content. Hide their bodies on
	// this public surface — clicking a graph node that isn't a
	// site-system shows just a "not yet published" placeholder.
	publicKinds := map[string]bool{"system": true, "default": true, "": true}
	if !publicKinds[node.Kind] || node.ParentNodeID != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":          true,
			"id":          node.ID,
			"label":       node.Label,
			"description": "",
			"kind":        node.Kind,
			"tag":         node.Tag,
			"status":      node.Status,
			"roadmap":     []any{},
			"changelog":   []any{},
			"hidden":      true,
		})
		return
	}
	type lRoadmap struct {
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	type lChangelog struct {
		ID         int64  `json:"id"`
		Title      string `json:"title"`
		Category   string `json:"category"`
		ReleasedAt string `json:"released_at"`
	}
	var roadmap []lRoadmap
	if items, err := h.store.ListRoadmapItemsByFlowNode(ctx, id); err == nil {
		for _, r := range items {
			roadmap = append(roadmap, lRoadmap{ID: r.ID, Title: r.Title, Status: r.Status})
		}
	}
	var changelog []lChangelog
	if entries, err := h.store.ListChangelogEntriesByFlowNode(ctx, id); err == nil {
		for _, e := range entries {
			changelog = append(changelog, lChangelog{
				ID:         e.ID,
				Title:      e.Title,
				Category:   e.Category,
				ReleasedAt: e.ReleasedAt.Format("2006-01-02"),
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"id":          node.ID,
		"label":       node.Label,
		"description": node.Description,
		"kind":        node.Kind,
		"tag":         node.Tag,
		"status":      node.Status,
		"roadmap":     roadmap,
		"changelog":   changelog,
	})
}

// ─── Admin CRUD for roadmap ───────────────────────────────────────

// AdminRoadmapPage lists all roadmap items (including archived) for
// admin editing. Also fetches the flow-node picker list so each
// edit row can offer a "link to graph node" dropdown.
func (h *Handlers) AdminRoadmapPage(c *gin.Context) {
	items, err := h.store.ListRoadmapItems(c.Request.Context(), true)
	if err != nil {
		h.errs.Report(c.Request.Context(), "admin/roadmap-list", err)
	}
	nodes, _ := h.store.ListFlowNodesForPicker(c.Request.Context())
	h.render(c, http.StatusOK, "Admin · Roadmap", "admin_roadmap.html", gin.H{
		"PageTitle": "Roadmap",
		"ActiveNav": "admin",
		"Items":     items,
		"FlowNodes": nodes,
		"Saved":     c.Query("saved") == "1",
	})
}

// AdminRoadmapSave handles both create (id=0) and update (id>0).
// One POST endpoint, two paths inside.
func (h *Handlers) AdminRoadmapSave(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	ctx := c.Request.Context()
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		c.Redirect(http.StatusFound, "/admin/roadmap")
		return
	}
	status := c.PostForm("status")
	if status != RoadmapStatusInFlight &&
		status != RoadmapStatusBacklog &&
		status != RoadmapStatusArchived {
		status = RoadmapStatusBacklog
	}
	sortOrder, _ := strconv.Atoi(c.PostForm("sort_order"))
	if sortOrder == 0 {
		sortOrder = 100
	}
	item := &RoadmapItem{
		ID:          id,
		Title:       title,
		Description: c.PostForm("description"),
		Status:      status,
		SortOrder:   sortOrder,
	}
	// Optional flow-node link. Empty / "0" form value → nil pointer
	// (no link). Any positive int64 → set on the row; the DB FK
	// gracefully NULLs out if the referenced node later gets deleted
	// (ON DELETE SET NULL).
	if nid, _ := strconv.ParseInt(c.PostForm("flow_node_id"), 10, 64); nid > 0 {
		item.FlowNodeID = &nid
	}
	item.SystemIDs = parseSystemIDs(c.PostForm("system_ids"))
	if id == 0 {
		uid := user.ID
		item.CreatedBy = &uid
		if _, err := h.store.CreateRoadmapItem(ctx, item); err != nil {
			h.errs.HandlerError(c, "admin/roadmap-create", err)
			return
		}
	} else {
		if err := h.store.UpdateRoadmapItem(ctx, item); err != nil {
			h.errs.HandlerError(c, "admin/roadmap-update", err)
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/roadmap?saved=1")
}

// parseSystemIDs accepts a comma- or whitespace-separated list of
// flow-node IDs from a form field. Invalid tokens are skipped
// silently — the admin form is trusted but the parsing tolerates
// stray commas. Returns an empty slice when the input is blank so
// the storage NOT NULL DEFAULT '{}' stays satisfied.
func parseSystemIDs(raw string) pq.Int64Array {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pq.Int64Array{}
	}
	seen := make(map[int64]bool)
	out := pq.Int64Array{}
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		n, err := strconv.ParseInt(tok, 10, 64)
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// AdminRoadmapDelete hard-deletes one roadmap item. Admin-only.
func (h *Handlers) AdminRoadmapDelete(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.store.DeleteRoadmapItem(c.Request.Context(), id); err != nil {
		h.errs.HandlerError(c, "admin/roadmap-delete", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/roadmap?saved=1")
}

// ─── Admin CRUD for changelog ────────────────────────────────────

func (h *Handlers) AdminChangelogPage(c *gin.Context) {
	const pageSize = 50
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	entries, total, err := h.store.ListChangelogEntries(c.Request.Context(), pageSize, offset)
	if err != nil {
		h.errs.Report(c.Request.Context(), "admin/changelog-list", err)
	}
	nodes, _ := h.store.ListFlowNodesForPicker(c.Request.Context())
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	h.render(c, http.StatusOK, "Admin · Changelog", "admin_changelog.html", gin.H{
		"PageTitle":  "Changelog",
		"ActiveNav":  "admin",
		"Entries":    entries,
		"FlowNodes":  nodes,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"Saved":      c.Query("saved") == "1",
	})
}

func (h *Handlers) AdminChangelogSave(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	ctx := c.Request.Context()
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		c.Redirect(http.StatusFound, "/admin/changelog")
		return
	}
	released, err := time.Parse("2006-01-02", strings.TrimSpace(c.PostForm("released_at")))
	if err != nil {
		released = time.Now()
	}
	category := c.PostForm("category")
	switch category {
	case ChangelogCategoryFeature, ChangelogCategoryFix,
		ChangelogCategoryPerf, ChangelogCategorySecurity,
		ChangelogCategoryInfra, ChangelogCategoryDocs:
		// valid
	default:
		category = ChangelogCategoryFeature
	}
	entry := &ChangelogEntry{
		ID:          id,
		Title:       title,
		Description: c.PostForm("description"),
		ReleasedAt:  released,
		Category:    category,
		SystemIDs:   parseSystemIDs(c.PostForm("system_ids")),
	}
	if nid, _ := strconv.ParseInt(c.PostForm("flow_node_id"), 10, 64); nid > 0 {
		entry.FlowNodeID = &nid
	}
	if id == 0 {
		uid := user.ID
		entry.CreatedBy = &uid
		if _, err := h.store.CreateChangelogEntry(ctx, entry); err != nil {
			h.errs.HandlerError(c, "admin/changelog-create", err)
			return
		}
	} else {
		if err := h.store.UpdateChangelogEntry(ctx, entry); err != nil {
			h.errs.HandlerError(c, "admin/changelog-update", err)
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/changelog?saved=1")
}

func (h *Handlers) AdminChangelogDelete(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil || !user.Mod {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.store.DeleteChangelogEntry(c.Request.Context(), id); err != nil {
		h.errs.HandlerError(c, "admin/changelog-delete", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/changelog?saved=1")
}
