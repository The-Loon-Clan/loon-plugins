package forum

import (
	"html/template"

	"github.com/gin-gonic/gin"
	"strings"
	"testing"
	"time"
)

// 1,300 lines of lifted markup, so executing it is the only thing that proves
// the data these handlers pass still matches what it reads. html/template
// streams: a field the markup wants and the data lacks aborts the render part
// way through and returns half a page with nothing logged.
//
// The fixtures mirror the keys the handlers actually pass, read off the render
// calls rather than invented — a test built from guessed keys proves the
// template renders SOMETHING, which is not the question.

func withTemplates(t *testing.T) {
	t.Helper()
	SetDeps(Deps{RelativeTime: func(any) string { return "2 hours ago" }})
	parseTemplates()
	t.Cleanup(func() { deps = Deps{}; pageTmpl = nil })
}

func render1(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	withTemplates(t)
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := sb.String()
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	// Body content only — the host wrapper owns the document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	if strings.Contains(s, "bootstrap.bundle.min.js") {
		t.Errorf("%s owns the Bootstrap runtime; the host page wrapper must load it exactly once", name)
	}
	return s
}

func sampleCategory() *ForumCategory {
	return &ForumCategory{ID: 1, Name: "General", Description: "anything",
		Color: "blue", ThreadCount: 1, PostCount: 1}
}

func sampleThread() *ForumThread {
	return &ForumThread{ID: 5, CategoryID: 1, UserID: 42, Username: "kirisame",
		Title: "how do I", CreatedAt: time.Now()}
}

func samplePost() *forumPostView {
	return &forumPostView{
		ForumPost: &ForumPost{ID: 9, ThreadID: 5, UserID: 42, Username: "kirisame",
			Body: "raw", CreatedAt: time.Now()},
		BodyHTML:   template.HTML("<p>rendered</p>"),
		EditorHTML: template.HTML(`<div id="post-ed"></div>`),
	}
}

func threadData(viewer int, isAdmin bool) map[string]any {
	return map[string]any{
		"Thread": sampleThread(), "Posts": []*forumPostView{samplePost()},
		"Total": 1, "Page": 1, "TotalPages": 1,
		"CurrentUserID":   viewer,
		"IsAdmin":         isAdmin,
		"CSRFToken":       "test-csrf",
		"PaginationHTML":  template.HTML(`<nav id="pg"></nav>`),
		"ReplyEditorHTML": template.HTML(`<div id="reply-ed"></div>`),
		"ReportModalHTML": template.HTML(`<div id="report-modal"></div>`),
		"IsRecruitment":   false,
	}
}

// Every widget the page borrows from the host has to actually land. Each of
// these is a map key: supply nothing and the page still renders 200, just
// without an editor, a pager, or a working report button.
func TestThreadPlacesEveryHostRenderedWidget(t *testing.T) {
	got := render1(t, "community_thread.html", threadData(42, false))
	for what, marker := range map[string]string{
		"the pager":            `<nav id="pg">`,
		"the reply editor":     `<div id="reply-ed">`,
		"the per-post editor":  `<div id="post-ed">`,
		"the report modal":     `<div id="report-modal">`,
		"the rendered body":    "<p>rendered</p>",
		"the CSRF form fields": `value="test-csrf"`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("the thread page is missing %s (%q)", what, marker)
		}
	}
	for _, want := range []string{
		`class="forum-thread-hero"`,
		`class="forum-post-layout"`,
		`class="post-card forum-post-card"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the thread page is missing layout landmark %s", want)
		}
	}
	for _, unwanted := range []string{`class="forum-thread-sidebar"`, "Thread Details", "Conversation Guide"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the thread page still renders non-index right rail %q", unwanted)
		}
	}
}

// IsAdmin and CurrentUserID gate the moderation and ownership controls. They
// arrived from the host's BaseData before the lift, where forgetting them was
// not possible; render() injects them now, and this is what proves the markup
// still reads what render() writes.
func TestThreadGatesOnViewerAndAdmin(t *testing.T) {
	stranger := render1(t, "community_thread.html", threadData(999, false))
	owner := render1(t, "community_thread.html", threadData(42, false))
	admin := render1(t, "community_thread.html", threadData(999, true))

	if len(owner) <= len(stranger) {
		t.Errorf("the post owner sees no more than a stranger (%d vs %d bytes) — "+
			"the ownership gate reads CurrentUserID and would render the same for both",
			len(owner), len(stranger))
	}
	if len(admin) <= len(stranger) {
		t.Errorf("an admin sees no more than a stranger (%d vs %d bytes) — "+
			"the pin/lock/delete controls read IsAdmin, and a missing key silently "+
			"removes every moderation button", len(admin), len(stranger))
	}
	for _, want := range []string{"/pin", "/lock"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin view is missing the %s control", want)
		}
		if strings.Contains(stranger, want) {
			t.Errorf("a non-admin was shown the %s control", want)
		}
	}
	// Anonymous must be a stranger, not a panic.
	render1(t, "community_thread.html", threadData(0, false))
}

func TestForumListPagesRender(t *testing.T) {
	forums := render1(t, "community_forums.html", map[string]any{
		"Categories": []*ForumCategory{sampleCategory()},
		"Activity": []*ForumActivityItem{{PostID: 9, ThreadID: 5,
			ThreadTitle: "how do I", UserID: 42, Username: "kirisame",
			CategoryID: 1, CreatedAt: time.Now()}},
		"Contributors": []*ForumContributor{
			{UserID: 42, Username: "kirisame", PostCount: 3}},
		"TotalThreads": 1, "TotalPosts": 1,
		"CurrentUserID": 42, "IsAdmin": false, "CSRFToken": "test-csrf",
	})
	if !strings.Contains(forums, "General") {
		t.Error("the forum index did not list its category")
	}
	for _, want := range []string{
		`class="forum-hero"`,
		`class="forum-index-grid"`,
		`class="forum-panel forum-categories-panel"`,
		`class="forum-panel forum-recent-panel"`,
		`class="forum-index-sidebar"`,
	} {
		if !strings.Contains(forums, want) {
			t.Errorf("the forum index is missing layout landmark %s", want)
		}
	}

	cat := render1(t, "community_category.html", map[string]any{
		"Category": sampleCategory(), "Threads": []*ForumThread{sampleThread()},
		"Total": 1, "Page": 1, "TotalPages": 1,
		"PaginationHTML": template.HTML(`<nav id="pg"></nav>`),
		"CurrentUserID":  42, "IsAdmin": false, "CSRFToken": "test-csrf",
	})
	if !strings.Contains(cat, `<nav id="pg">`) {
		t.Error("the category page did not place the host-rendered pager")
	}
	for _, want := range []string{
		`class="forum-category-hero"`,
		`class="forum-category-layout"`,
		`class="forum-panel forum-thread-panel"`,
	} {
		if !strings.Contains(cat, want) {
			t.Errorf("the category page is missing layout landmark %s", want)
		}
	}
	for _, unwanted := range []string{`class="forum-category-sidebar"`, "About This Forum", "Top Contributors", "Forum Legend"} {
		if strings.Contains(cat, unwanted) {
			t.Errorf("the category page still renders non-index right rail %q", unwanted)
		}
	}

	// The locked shell is a real branch: seeable but not readable, and it
	// passes no pager at all.
	locked := render1(t, "community_category.html", map[string]any{
		"Category": sampleCategory(), "Threads": nil, "Total": 0,
		"Page": 1, "TotalPages": 1, "AccessDenied": true,
		"PaginationHTML": template.HTML(""),
		"CurrentUserID":  42, "IsAdmin": false, "CSRFToken": "test-csrf",
	})
	if strings.Contains(locked, "New Thread") {
		t.Error("a viewer denied access was still offered the New Thread button")
	}
}

func TestNewThreadAndAdminPagesRender(t *testing.T) {
	nt := render1(t, "community_new_thread.html", map[string]any{
		"Categories": []*ForumCategory{sampleCategory()}, "SelectedCategory": 1,
		"EditorHTML":    template.HTML(`<div id="new-ed"></div>`),
		"CurrentUserID": 42, "IsAdmin": false, "CSRFToken": "test-csrf",
	})
	if !strings.Contains(nt, `<div id="new-ed">`) {
		t.Error("the new-thread page did not place the editor")
	}

	adm := render1(t, "admin_forum_categories.html", map[string]any{
		"Categories": []*ForumCategory{sampleCategory()},
		"Colors":     categoryColorList, "GateRoles": gateRoleList,
		"Flash": "", "Err": "",
		"CurrentUserID": 42, "IsAdmin": true, "CSRFToken": "test-csrf",
	})
	if !strings.Contains(adm, "General") {
		t.Error("the admin page did not list the category")
	}
}

// The empty state is what a fresh install shows, and a range over nothing is
// where a missing {{else}} surfaces.
func TestForumPagesRenderEmpty(t *testing.T) {
	render1(t, "community_forums.html", map[string]any{
		"Categories": []*ForumCategory{}, "Activity": []*ForumActivityItem{},
		"Contributors": []*ForumContributor{}, "TotalThreads": 0, "TotalPosts": 0,
		"CurrentUserID": 0, "IsAdmin": false, "CSRFToken": "",
	})
	render1(t, "community_category.html", map[string]any{
		"Category": sampleCategory(), "Threads": []*ForumThread{},
		"Total": 0,
		"Page":  1, "TotalPages": 1, "PaginationHTML": template.HTML(""),
		"CurrentUserID": 0, "IsAdmin": false, "CSRFToken": "",
	})
	render1(t, "admin_forum_categories.html", map[string]any{
		"Categories": []*ForumCategory{}, "Colors": categoryColorList,
		"GateRoles": gateRoleList, "Flash": "", "Err": "",
		"CurrentUserID": 0, "IsAdmin": true, "CSRFToken": "",
	})
}

// Every POST form must carry the CSRF field: the host gate rejects a POST
// without one, and a missing hidden input is invisible on the rendered page,
// so the only symptom is a 403 on submit. The messages plugin's compose form
// shipped without one and nobody could start a conversation for 77 days.
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"community_thread.html": threadData(42, true),
		"community_new_thread.html": {
			"Categories": []*ForumCategory{sampleCategory()}, "SelectedCategory": 1,
			"EditorHTML":    template.HTML(""),
			"CurrentUserID": 42, "IsAdmin": false, "CSRFToken": "test-csrf",
		},
		"admin_forum_categories.html": {
			"Categories": []*ForumCategory{sampleCategory()},
			"Colors":     categoryColorList, "GateRoles": gateRoleList,
			"Flash": "", "Err": "",
			"CurrentUserID": 42, "IsAdmin": true, "CSRFToken": "test-csrf",
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := render1(t, name, data)
			forms := strings.Count(out, `method="POST"`)
			tokens := strings.Count(out, `name="_csrf" value="test-csrf"`)
			if forms == 0 {
				t.Fatalf("no POST form rendered — the branch under test did not open")
			}
			if tokens != forms {
				t.Errorf("%d POST forms but %d CSRF fields — a form would 403 on submit",
					forms, tokens)
			}
		})
	}
}

// The legacy contract must keep working, because loon-demo-site still wires it
// and builds against this working tree. A host that sets BaseData/Paginate
// instead of the render seams renders by template NAME from its own directory
// — so ready() must accept it, and nothing on that path may deref a seam it
// did not wire.
func TestLegacyContractIsStillAccepted(t *testing.T) {
	t.Cleanup(func() { deps = Deps{} })

	SetDeps(Deps{
		Markdown: func(string) template.HTML { return "" },
		BaseData: func(c *gin.Context, extra gin.H) gin.H { return extra },
		Paginate: func(p, tp int, u string) any { return nil },
	})
	if !deps.ready() {
		t.Fatal("a host on the previous contract is refused — this breaks loon-demo-site")
	}
	// The optional-seam helpers must degrade rather than deref nil.
	if editor(nil) != "" || paginate(1, 0, "/x") != "" || reportModal(nil) != "" {
		t.Error("the legacy path should yield empty chrome, not panic or invent markup")
	}
	if legacyPaginate(1, 1, "/x") != nil {
		t.Error("legacyPaginate should return what the host's builder returned")
	}

	// And half of each contract is not a contract: a host that wired some of
	// the render seams would serve some pages and blank others, which reads
	// as a broken site rather than a missing call.
	SetDeps(Deps{
		Markdown:   func(string) template.HTML { return "" },
		RenderPage: func(*gin.Context, int, string, template.HTML) {},
		CSRFToken:  func(*gin.Context) string { return "" },
	})
	if deps.ready() {
		t.Error("a half-wired host was accepted")
	}
	// Markdown alone is never optional — it is the sanitiser.
	SetDeps(Deps{
		BaseData: func(c *gin.Context, extra gin.H) gin.H { return extra },
		Paginate: func(p, tp int, u string) any { return nil },
	})
	if deps.ready() {
		t.Error("a host that wired no Markdown was accepted; forum posts are user-authored")
	}
}
