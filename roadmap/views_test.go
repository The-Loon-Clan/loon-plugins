package roadmap

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func testRender(t *testing.T, name string, data gin.H) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) { captured = body },
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return "<nav>pager</nav>"
		},
		CSRFToken:     func(c *gin.Context) string { return "test-csrf" },
		Viewer:        func(c *gin.Context) *Viewer { return &Viewer{ID: 7, Username: "tester", Mod: true} },
		RelativeTime:  func(v any) string { return "3 days ago" },
		SanitizeForum: func(s string) string { return s },
		RenderForumMarkdown: func(src string) template.HTML {
			return template.HTML(template.HTMLEscapeString(src))
		},
	})
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{deps: *deps}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/flow", nil)
	h.render(c, 200, "t", name, data)
	if captured == "" {
		var sb strings.Builder
		err := pageTmpl.ExecuteTemplate(&sb, name, data)
		t.Fatalf("%s rendered empty — template error: %v", name, err)
	}
	out := string(captured)
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return out
}

func sampleNode(id int64) *FlowNode {
	author := 7
	return &FlowNode{
		ID: id, Kind: "idea", Label: "Better search", Description: "make it faster",
		X: 10, Y: 20, CreatedBy: &author, VoteCount: 3, Tag: "feature", Status: "open",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func sampleProposal() *FlowProposal {
	return &FlowProposal{
		FlowNode: *sampleNode(1), Username: "tester",
		AvatarPath: "", UserRole: "user", CommentCount: 2,
	}
}

func TestPagesRender(t *testing.T) {
	out := testRender(t, "flow.html", gin.H{"ActiveNav": "community"})
	if !strings.Contains(out, "cytoscape") {
		t.Error("flow canvas lost its cytoscape script")
	}

	out = testRender(t, "flow_proposals.html", proposalsData())
	if !strings.Contains(out, "Better search") {
		t.Error("proposals page missing the proposal")
	}

	out = testRender(t, "flow_mockups.html", gin.H{
		"ActiveNav": "community",
		"Mockups": []*MockupSummary{{ID: 1, Label: "Login mock", HTML: "<div>m</div>",
			VoteCount: 1, CreatedAt: time.Now(), AuthorName: "tester"}},
		"Sort": "top", "Scope": "all",
	})
	if !strings.Contains(out, "Login mock") {
		t.Error("mockups page missing the card")
	}

	out = testRender(t, "flow_mockup_detail.html", gin.H{
		"ActiveNav": "community", "Node": sampleNode(2), "Parent": sampleNode(1),
		"Comments":   []*FlowComment{{ID: 1, UserID: 7, Username: "tester", Body: "nice", CreatedAt: time.Now()}},
		"Voted":      true,
		"MockupHTML": template.HTML("<div>mock</div>"), "MockupNotesHTML": template.HTML(""),
		"ParentHTML": template.HTML(""), "ParentNotesHTML": template.HTML(""),
	})
	if !strings.Contains(out, "nice") {
		t.Error("mockup detail missing the comment")
	}

	out = testRender(t, "help_roadmap.html", gin.H{
		"PageTitle": "Roadmap", "ActiveNav": "support", "ActiveTab": "roadmap",
		"InFlight": []*RoadmapItem{{ID: 1, Title: "Ship the tracker", Status: RoadmapStatusInFlight}},
		"Backlog":  []*RoadmapItem{{ID: 2, Title: "Passkeys", Status: RoadmapStatusBacklog}},
		"Buckets":  nil, "Total": 0, "Page": 1, "TotalPages": 1,
	})
	if !strings.Contains(out, "Ship the tracker") {
		t.Error("roadmap page missing the item")
	}

	out = testRender(t, "admin_roadmap.html", gin.H{
		"PageTitle": "x", "ActiveNav": "admin",
		"Items":     []*RoadmapItem{{ID: 1, Title: "Ship it", Status: RoadmapStatusInFlight, UpdatedAt: time.Now()}},
		"FlowNodes": []FlowNodePicker{{ID: 1, Label: "node", Kind: "idea", Tag: "feature"}},
		"Saved":     "",
	})
	if !strings.Contains(out, "Ship it") {
		t.Error("admin roadmap missing the item")
	}

	out = testRender(t, "admin_changelog.html", gin.H{
		"PageTitle": "x", "ActiveNav": "admin",
		"Entries": []*ChangelogEntry{{ID: 1, Title: "Fixed the thing",
			ReleasedAt: time.Now(), Category: ChangelogCategoryFix}},
		"FlowNodes": []FlowNodePicker{}, "Total": 1, "Page": 1, "TotalPages": 1, "Saved": "",
	})
	if !strings.Contains(out, "Fixed the thing") {
		t.Error("admin changelog missing the entry")
	}
}

// The two admin pages are the only form-posting surfaces (the /flow editor
// fetches with the X-CSRF-Token header instead).
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for name, data := range map[string]gin.H{
		"admin_roadmap.html": {
			"Items":     []*RoadmapItem{{ID: 1, Title: "Ship it", UpdatedAt: time.Now()}},
			"FlowNodes": []FlowNodePicker{},
		},
		"admin_changelog.html": {
			"Entries":   []*ChangelogEntry{{ID: 1, Title: "x", ReleasedAt: time.Now()}},
			"FlowNodes": []FlowNodePicker{}, "Total": 1, "Page": 1, "TotalPages": 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := testRender(t, name, data)
			forms := strings.Count(out, `method="POST"`)
			tokens := strings.Count(out, `name="_csrf" value="test-csrf"`)
			if forms == 0 {
				t.Fatalf("no POST form rendered — the branch under test did not open")
			}
			if tokens != forms {
				t.Errorf("%d POST forms but %d CSRF fields — a form would 403 on submit", forms, tokens)
			}
		})
	}
}

func TestPagesRenderEmpty(t *testing.T) {
	out := testRender(t, "flow_proposals.html", gin.H{
		"Proposals": []*FlowProposal{}, "Recent": []*FlowProposal{},
		"Pagination": template.HTML(""),
		"FilterSort": "newest", "TotalCount": 0, "CurrentUserID": 0,
		"Facets": &FlowProposalFacets{Tags: map[string]int{}, Statuses: map[string]int{}},
	})
	if out == "" {
		t.Error("empty proposals page did not render")
	}
	// The empty page is the one a member is most likely to be looking at
	// -- there is one request on it today -- so the form has to be the
	// thing they find, not a "no requests" notice.
	if !strings.Contains(out, "Create a new feature request") {
		t.Error("the empty page does not offer the form, which is the only thing to do on it")
	}
}

// proposalsData is one realistic render's worth of view model. Kept in one
// place because every test below asserts on a different part of the same
// page, and a field added to the handler has to reach all of them at once.
func proposalsData() gin.H {
	return gin.H{
		"ActiveNav":  "community",
		"Proposals":  []*FlowProposal{sampleProposal()},
		"Recent":     []*FlowProposal{sampleProposal()},
		"Pagination": template.HTML("<nav>pager</nav>"),
		"FilterTag":  "", "FilterStatus": "", "FilterSort": "newest",
		"FilterMine": false,
		"TotalCount": 1, "CurrentUserID": 7,
		"UploadsOn": true,
		"Facets": &FlowProposalFacets{
			Tags:     map[string]int{"bug": 4, "feature": 2},
			Statuses: map[string]int{"open": 5, "done": 1},
			Untagged: 1, Total: 6,
		},
	}
}

// Browsing and filing are separate tabs, and Requests is the one that opens.
//
// Both on one page put four steps of empty form above the requests on every
// load, so reading what other people had asked for meant scrolling past a
// blank form each time. Filing is the rarer act.
func TestRequestsTabOpensFirst(t *testing.T) {
	out := testRender(t, "flow_proposals.html", proposalsData())

	for _, want := range []string{`id="pane-requests"`, `id="pane-new"`, `id="tab-requests"`, `id="tab-new"`} {
		if !strings.Contains(out, want) {
			t.Errorf("tab scaffolding missing %s", want)
		}
	}
	// The Requests pane is the one carrying `show active`. Asserted by
	// position rather than presence, because both panes contain the word.
	reqAt := strings.Index(out, `id="pane-requests"`)
	newAt := strings.Index(out, `id="pane-new"`)
	reqActive := strings.Contains(out[max0(reqAt-120):reqAt], "show active")
	newActive := strings.Contains(out[max0(newAt-120):newAt], "show active")
	if !reqActive {
		t.Error("the Requests pane is not the one that opens -- a member landing " +
			"here sees an empty form instead of what has been asked for")
	}
	if newActive {
		t.Error("both panes opened active, which renders the form and the list at once")
	}
}

// ...except when there is nothing to read, where the form IS the page.
func TestEmptyPageOpensOnTheForm(t *testing.T) {
	d := proposalsData()
	d["Proposals"] = []*FlowProposal{}
	d["TotalCount"] = 0
	out := testRender(t, "flow_proposals.html", d)
	newAt := strings.Index(out, `id="pane-new"`)
	if !strings.Contains(out[max0(newAt-120):newAt], "show active") {
		t.Error("with no requests to browse the page still opened on the empty " +
			"list, which is the one view with nothing on it")
	}
}

// ?new=1 opens on the form. This is the link handed to members who are being
// asked to file something, so it has to land on the form rather than on a
// list they then have to find their way out of.
func TestNewParamOpensTheForm(t *testing.T) {
	d := proposalsData()
	d["OpenNew"] = true
	out := testRender(t, "flow_proposals.html", d)
	newAt := strings.Index(out, `id="pane-new"`)
	if !strings.Contains(out[max0(newAt-120):newAt], "show active") {
		t.Error("?new=1 did not open the form tab")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// Filter pills carry their counts, and pills with nothing behind them are
// not rendered.
//
// The strip was fifteen pills with no counts over a page holding a single
// request, so nearly every click landed on "no requests match the current
// filters" -- which reads as a broken page rather than an empty category.
func TestFilterPillsShowCountsAndHideEmptyOnes(t *testing.T) {
	out := testRender(t, "flow_proposals.html", proposalsData())

	for _, want := range []string{"Bug", "Feature", "Open", "Done", "Untagged"} {
		if !strings.Contains(out, want) {
			t.Errorf("filter strip is missing the %q pill, which has requests behind it", want)
		}
	}
	// Nothing is tagged performance or ui, and nothing is planned or
	// declined -- those pills would be dead ends.
	for _, unwanted := range []string{"Performance", "Planned", "Declined"} {
		if strings.Contains(out, ">"+unwanted+" ") {
			t.Errorf("rendered a %q pill with no requests behind it -- clicking it "+
				"can only ever report an empty result", unwanted)
		}
	}
}

// An empty facet count must not take the page down with it.
//
// This is the shape that actually broke: the view model gained a field, a
// caller did not supply it, and `index` on a nil map aborted the render
// mid-write. html/template streams, so the reader gets a page that stops
// halfway with no error anywhere on it.
func TestProposalsSurvivesMissingFacets(t *testing.T) {
	d := proposalsData()
	d["Facets"] = &FlowProposalFacets{}
	out := testRender(t, "flow_proposals.html", d)
	if !strings.Contains(out, "All") {
		t.Error("a facet-less render lost the filter strip entirely")
	}
	if !strings.Contains(out, "Better search") {
		t.Error("render stopped before the listing -- a streamed abort, which " +
			"shows as a page that simply ends")
	}
}

// The category picker and the shared markdown editor are the two halves of
// the form that are wired by markup rather than by an id lookup, so nothing
// else would notice them going missing.
func TestFormCarriesItsPickerAndEditor(t *testing.T) {
	out := testRender(t, "flow_proposals.html", proposalsData())

	for _, tag := range []string{"ui", "bug", "feature", "performance", "other"} {
		if !strings.Contains(out, `data-tag="`+tag+`"`) {
			t.Errorf("category card %q missing -- the picker cannot select it", tag)
		}
	}
	// Emitting .md-editor is the entire integration: the host footer wires
	// every one on the page. Lose the class and the toolbar and preview
	// silently become a bare textarea.
	if !strings.Contains(out, `class="md-editor"`) {
		t.Error("the shared markdown editor markup is gone -- the toolbar and " +
			"Preview tab are wired by the host from this class alone")
	}
	if !strings.Contains(out, `class="md-textarea" id="new-desc"`) {
		t.Error("the description textarea lost the id the submit path reads it by")
	}
}

// Attachments are optional: a host that wired no blob store must not render
// an upload control that can only fail.
func TestUploadControlFollowsTheHostCapability(t *testing.T) {
	// Asserted on the MARKUP, not on the string "drop-zone": the script
	// looks the element up by that name unconditionally and guards on the
	// result, so a bare substring match is true either way and the test
	// would pass whatever the template did.
	on := testRender(t, "flow_proposals.html", proposalsData())
	if !strings.Contains(on, `id="drop-zone"`) {
		t.Error("uploads enabled but no drop zone rendered")
	}
	d := proposalsData()
	d["UploadsOn"] = false
	off := testRender(t, "flow_proposals.html", d)
	if strings.Contains(off, `id="drop-zone"`) {
		t.Error("rendered an upload control on a host with no file store -- " +
			"every drop would 501")
	}
}
