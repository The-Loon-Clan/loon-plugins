package communities

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Executing every fragment is the only thing that proves the view models the
// handlers build still match what the markup reads. html/template streams: a
// field the markup wants and the data lacks aborts the render part way
// through and returns half a page with nothing logged. The view models are
// structs precisely so that miss is an ERROR here instead of an empty string
// in production — and these tests are where the error surfaces.

// testAuth is the one-line fake viewer: signed in as user 7 unless the test
// swaps it out.
func testAuth(signedIn bool) core.AuthService {
	return core.NewAuth(core.AuthAdapter{
		CurrentUserFn: func(c *gin.Context) (*core.User, bool) {
			if !signedIn {
				return nil, false
			}
			return &core.User{ID: 7, Username: "tester"}, true
		},
	})
}

func testRenderAs(t *testing.T, signedIn bool, name string, vm pageVM) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) { captured = body },
		CSRFToken:  func(c *gin.Context) string { return "test-csrf" },
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return `<nav id="pg">pager</nav>`
		},
		RenderEditor: func(opts map[string]any) template.HTML { return `<div id="md-ed">editor</div>` },
		RelativeTime: func(v any) string { return "3 days ago" },
		Markdown: func(src string) template.HTML {
			return template.HTML(template.HTMLEscapeString(src))
		},
		PageOffset: func(page, pageSize int) int { return (page - 1) * pageSize },
	})
	t.Cleanup(func() { deps = Deps{}; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{auth: testAuth(signedIn)}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/c", nil)
	h.render(c, 200, "t", name, vm)
	if captured == "" {
		// Re-execute directly to surface the underlying template error in
		// the failure message.
		var sb strings.Builder
		err := pageTmpl.ExecuteTemplate(&sb, name, vm)
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

func testRender(t *testing.T, name string, vm pageVM) string {
	t.Helper()
	return testRenderAs(t, true, name, vm)
}

// ── fixtures — keys read off the actual handler calls, not invented ──

func sampleCommunity() *Community {
	return &Community{
		ID: 1, Slug: "anime", Name: "Anime Fans", Description: "All things anime",
		SidebarMD: "**welcome**", BannerURL: "", BannerPosition: 50,
		OwnerUserID: 7, JoinType: CommunityJoinTypeOpen,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		OwnerUsername: "owner-one", OwnerAvatarPath: "",
		SubscriberCount: 12, ThreadCount: 3,
	}
}

func sampleThread(id int) *CommunityThread {
	return &CommunityThread{
		ID: id, CommunityID: 1, UserID: 7,
		Title: "First episode discussion", Body: "That opening was great",
		ReplyCount: 2, LastPostAt: time.Now(),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Username: "tester", AvatarPath: "", CommunitySlug: "anime",
	}
}

func samplePost(id int64) threadPostVM {
	return threadPostVM{
		CommunityPost: &CommunityPost{
			ID: id, ThreadID: 1, UserID: 7, Body: "agreed",
			CreatedAt: time.Now(), Username: "replier", AvatarPath: "",
		},
		BodyHTML: template.HTML("agreed"),
	}
}

func sampleRequest() *CommunityJoinRequest {
	return &CommunityJoinRequest{
		ID: 4, CommunityID: 1, UserID: 9, Message: "let me in please",
		Status: JoinRequestPending, PointsHeld: 100, CreatedAt: time.Now(),
		Username: "applicant", Role: "user", AvatarPath: "",
		AccountCreated: time.Now().AddDate(-1, 0, 0), UserPoints: 5000,
	}
}

func sampleInvite() *CommunityInvite {
	exp := time.Now().AddDate(0, 0, 7)
	return &CommunityInvite{
		ID: 2, CommunityID: 1, Code: "abcd1234", Note: "for translators",
		MaxUses: 5, UseCount: 1, ExpiresAt: &exp, CreatedAt: time.Now(),
	}
}

func TestPagesRender(t *testing.T) {
	out := testRender(t, "communities_index.html", &communitiesIndexVM{
		Communities: []*Community{sampleCommunity()},
		Total:       1,
		Pagination:  template.HTML(`<nav id="pg">pager</nav>`),
	})
	if !strings.Contains(out, "Anime Fans") {
		t.Error("index missing the community card")
	}
	if !strings.Contains(out, `<nav id="pg">`) {
		t.Error("index lost the host-rendered pager")
	}

	out = testRender(t, "community_new.html", &communityNewVM{
		Error: "That slug is reserved. Please pick another.",
		Slug:  "admin", Name: "Admins", Description: "d",
	})
	if !strings.Contains(out, "That slug is reserved") {
		t.Error("create form missing the validation error")
	}

	out = testRender(t, "community_view.html", &communityViewVM{
		Community:  sampleCommunity(),
		Threads:    []*CommunityThread{sampleThread(1)},
		Total:      1,
		Pagination: template.HTML(`<nav id="pg">pager</nav>`),
		Rules: []*CommunityRule{
			{ID: 1, Position: 1, Title: "Be kind", Body: "No flaming."},
			{ID: 2, Position: 2, Title: "Tag spoilers", Body: ""},
		},
		Mods:            []*CommunityMod{{UserID: 8, Username: "mod-one"}},
		Role:            CommunityViewerRole{IsSubscriber: true},
		MyRequest:       nil,
		PendingCount:    0,
		Flash:           Flash{Code: "saved"},
		SidebarHTML:     template.HTML("<p>side</p>"),
		DescriptionHTML: template.HTML("<p>desc</p>"),
	})
	// "Settings saved." is now the TEMPLATE's words, reached through the flash
	// CODE — which is what makes this assertion worth more than it was: it
	// proves the code resolved to a sentence rather than rendering blank.
	for _, marker := range []string{"First episode discussion", "Be kind", "mod-one", "Settings saved."} {
		if !strings.Contains(out, marker) {
			t.Errorf("community view missing %q", marker)
		}
	}

	out = testRender(t, "community_new_thread_c.html", &communityNewThreadVM{
		Community: sampleCommunity(),
		Editor:    template.HTML(`<div id="md-ed">editor</div>`),
	})
	if !strings.Contains(out, `<div id="md-ed">`) {
		t.Error("new-thread form lost the host-rendered editor — the body field is gone")
	}

	out = testRender(t, "community_thread_c.html", &communityThreadVM{
		Community:   sampleCommunity(),
		Thread:      sampleThread(1),
		BodyHTML:    template.HTML("That opening was great"),
		Posts:       []threadPostVM{samplePost(10)},
		Total:       1,
		Pagination:  template.HTML(`<nav id="pg">pager</nav>`),
		Rules:       []*CommunityRule{{ID: 1, Position: 1, Title: "Be kind"}},
		Mods:        []*CommunityMod{{UserID: 8, Username: "mod-one"}},
		Role:        CommunityViewerRole{IsMod: true},
		SidebarHTML: template.HTML("<p>side</p>"),
	})
	for _, marker := range []string{"That opening was great", "agreed", "replier"} {
		if !strings.Contains(out, marker) {
			t.Errorf("thread view missing %q", marker)
		}
	}

	out = testRender(t, "community_join_requests.html", &communityJoinRequestsVM{
		Community: sampleCommunity(),
		Requests:  []*CommunityJoinRequest{sampleRequest()},
		Invites:   []*CommunityInvite{sampleInvite()},
		Flash:     Flash{},
	})
	for _, marker := range []string{"applicant", "let me in please", "abcd1234", "100 pts held"} {
		if !strings.Contains(out, marker) {
			t.Errorf("request queue missing %q", marker)
		}
	}

	out = testRender(t, "community_settings.html", &communitySettingsVM{
		Community: sampleCommunity(),
		Flash:     Flash{Code: "saved"},
	})
	if !strings.Contains(out, "Anime Fans") || !strings.Contains(out, "bp-range") {
		t.Error("settings page missing the community name or the banner-position slider")
	}
}

// The denied-request branch reads two extra fields off MyRequest; a fixture
// that never opens it would let those fields rot.
func TestViewShowsTheDeniedRequestNote(t *testing.T) {
	denied := sampleRequest()
	denied.Status = JoinRequestDenied
	denied.ResponseMessage = "not this season"
	out := testRender(t, "community_view.html", &communityViewVM{
		Community: sampleCommunity(),
		Role:      CommunityViewerRole{},
		MyRequest: denied,
	})
	if !strings.Contains(out, "not this season") {
		t.Error("denied-request note lost the moderator response")
	}
}

// Every POST form must carry the hidden _csrf input, or it 403s on submit
// with nothing logged. The equality count has caught real bugs twice in
// sibling plugins. Fixtures deliberately open every form-bearing branch:
// signed-in viewer, moderator role, a post row, a pending request.
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for name, vm := range map[string]pageVM{
		"community_new.html": &communityNewVM{},
		"community_view.html": &communityViewVM{
			Community: sampleCommunity(), // open join type: subscribe form renders
			Role:      CommunityViewerRole{},
		},
		"community_new_thread_c.html": &communityNewThreadVM{Community: sampleCommunity()},
		"community_thread_c.html": &communityThreadVM{
			Community: sampleCommunity(),
			Thread:    sampleThread(1),
			Posts:     []threadPostVM{samplePost(10)},
			Role:      CommunityViewerRole{IsMod: true}, // mod bar + per-post remove
		},
		"community_join_requests.html": &communityJoinRequestsVM{
			Community: sampleCommunity(),
			Requests:  []*CommunityJoinRequest{sampleRequest()}, // approve + deny forms
			Invites:   nil,
		},
		"community_settings.html": &communitySettingsVM{Community: sampleCommunity()},
	} {
		t.Run(name, func(t *testing.T) {
			out := testRender(t, name, vm)
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

// Empty-state data still renders — the index for an anonymous viewer on a
// site with no communities is the first page any adopter sees.
func TestPagesRenderEmpty(t *testing.T) {
	out := testRenderAs(t, false, "communities_index.html", &communitiesIndexVM{})
	if !strings.Contains(out, "No communities yet.") {
		t.Error("empty index lost its empty state")
	}
	if strings.Contains(out, "/c/new") {
		t.Error("anonymous empty index offers the create link it should reserve for members")
	}
}

// The whole fragment set must parse in one pass — Provision treats a parse
// failure as a boot failure, and this is the test-side statement of that.
func TestFragmentSetParses(t *testing.T) {
	SetDeps(Deps{RelativeTime: func(v any) string { return "" }})
	t.Cleanup(func() { deps = Deps{}; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("fragment set does not parse: %v", err)
	}
	for _, name := range []string{
		"communities_index.html", "community_view.html", "community_new.html",
		"community_settings.html", "community_join_requests.html",
		"community_thread_c.html", "community_new_thread_c.html",
	} {
		if pageTmpl.Lookup(name) == nil {
			t.Errorf("fragment %s missing from the parsed set", name)
		}
	}
}

// Half of a contract is not a contract.
//
// This was TestLegacyContractIsStillAccepted, which guaranteed the previous
// BaseData/Pagination contract kept working "because loon-demo-site still
// wires it". It does not any more, and that contract is gone; what survives is
// the half of the test that was never about it.
func TestHalfAContractIsRefused(t *testing.T) {
	t.Cleanup(func() { deps = Deps{} })

	// A host that wired SOME of the render seams would serve some pages and
	// blank others.
	SetDeps(Deps{
		Markdown:   func(string) template.HTML { return "" },
		PageOffset: func(p, ps int) int { return 0 },
		RenderPage: func(*gin.Context, int, string, template.HTML) {},
		CSRFToken:  func(*gin.Context) string { return "" },
	})
	if deps.ready() {
		t.Error("a half-wired host was accepted")
	}

	// Markdown alone is never optional — it is the sanitiser, and community
	// posts are user-authored.
	SetDeps(Deps{
		PageOffset:       func(p, ps int) int { return 0 },
		RenderPage:       func(*gin.Context, int, string, template.HTML) {},
		CSRFToken:        func(*gin.Context) string { return "" },
		RenderPagination: func(int, int, int, string) template.HTML { return "" },
		RenderEditor:     func(map[string]any) template.HTML { return "" },
		RelativeTime:     func(any) string { return "" },
	})
	if deps.ready() {
		t.Error("a host that wired no Markdown was accepted; community posts are user-authored")
	}

	// The optional-seam helpers still degrade rather than deref nil.
	deps = Deps{}
	if pager(1, 0, "/c?") != "" || editorHTML() != "" {
		t.Error("an unwired optional seam should yield empty chrome, not panic")
	}
}

// Every flash CODE renders its own sentence, with the numbers it was given.
//
// Ported from loon-demo-site, which owned this template until the plugin took
// over rendering. The host's copy of the test went with the host's copy of the
// markup, and this is the coverage that would otherwise have been lost.
//
// The failure it pins is specific and silent: Flash carries a code and its
// values separately, so an argument the template asks for and the sender never
// supplied renders "You need  points to join this community" — a grammatical,
// wrong sentence that no Go test sees and no template error reports.
func TestFlashRendersItsNumbers(t *testing.T) {
	SetDeps(Deps{
		RenderPage:       func(c *gin.Context, status int, title string, body template.HTML) {},
		CSRFToken:        func(c *gin.Context) string { return "test-csrf" },
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML { return "" },
		RenderEditor:     func(opts map[string]any) template.HTML { return "" },
		RelativeTime:     func(v any) string { return "3 days ago" },
		Markdown:         func(src string) template.HTML { return template.HTML(src) },
		PageOffset:       func(page, pageSize int) int { return 0 },
	})
	t.Cleanup(func() { deps = Deps{}; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	for _, tc := range []struct {
		code string
		args []string
		want []string
	}{
		{"toopoor", []string{"100", "42"}, []string{"100", "42"}},
		{"tooyoung", []string{"7", "2"}, []string{"7", "2"}},
		{"invited", []string{"abc123"}, []string{"/c/join/abc123"}},
		{"saved", nil, []string{"Settings saved."}},
		// A code nothing maps still says something: silence reads as success.
		{"wat", nil, []string{"Something went wrong."}},
	} {
		var buf strings.Builder
		if err := pageTmpl.ExecuteTemplate(&buf, "c-flash", Flash{Code: tc.code, Args: tc.args}); err != nil {
			t.Fatalf("%s: execute: %v", tc.code, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("flash %q is missing %q:\n%s", tc.code, want, buf.String())
			}
		}
	}
}
