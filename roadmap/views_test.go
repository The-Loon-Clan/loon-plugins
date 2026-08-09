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

	out = testRender(t, "flow_proposals.html", gin.H{
		"ActiveNav": "community", "Proposals": []*FlowProposal{sampleProposal()},
		"Pagination": template.HTML("<nav>pager</nav>"),
		"FilterTag":  "", "FilterStatus": "", "FilterSort": "newest",
		"TotalCount": 1, "CurrentUserID": 7,
	})
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
		"Proposals": []*FlowProposal{}, "Pagination": template.HTML(""),
		"FilterSort": "newest", "TotalCount": 0, "CurrentUserID": 0,
	})
	if out == "" {
		t.Error("empty proposals page did not render")
	}
}
