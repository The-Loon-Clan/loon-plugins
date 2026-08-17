package requests

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// testRender executes one page through the REAL render path — template
// parse, chrome-key injection, fragment execution — capturing what the host's
// RenderPage would receive. Both templates moved here from the host, and a
// map-driven template answers a missing key with emptiness and no error, so
// executing every branch with realistic data is the only net under them.
func testRender(t *testing.T, name string, data gin.H) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) {
			captured = body
		},
		CSRFToken: func(c *gin.Context) string { return "test-csrf" },
		Markdown:  func(s string) template.HTML { return template.HTML(template.HTMLEscapeString(s)) },
		Viewer: func(c *gin.Context) *Viewer {
			return &Viewer{ID: 7, Username: "tester", Points: 100, Contributor: true, Mod: true}
		},
		NzbCardCSS: func() template.HTML { return "<style>/* nzb-card */</style>" },
	})
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{deps: *deps}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/community/requests", nil)
	h.render(c, 200, "t", name, data)
	if captured == "" {
		// render() answers a plain 500 to users; the test wants the cause.
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

func sampleRequest(id int64) *Request {
	aid := 42
	return &Request{
		ID: id, UserID: 7, Username: "tester",
		Title: "Great Show S02E04 1080p WEB-DL", Category: "Anime",
		Resolution: "1080p", Source: "WEB-DL", Season: "02", Episodes: "04",
		NyaaURL: "https://nyaa.si/view/123", InfoHash: "aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
		SeedCount: 12, Notes: "please and thank you", AnimeID: &aid,
		VoteCount: 3, BoostCount: 1, PriorityScore: 9,
		Priorities:  []RequestPriority{{TypeSlug: "boost", Count: 1, Label: "Boost", IconHTML: "<b>+</b>", ShowCount: true}},
		RemuxOption: "none", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func listPageData(tab string) gin.H {
	return gin.H{
		"Tab":            tab,
		"Requests":       []*Request{sampleRequest(1), sampleRequest(2)},
		"FeedItems":      []*FeedItem{{ID: 1, Source: "nyaa", Sources: []string{"nyaa"}, Title: "Feed Item", InfoHash: "bbbb", Status: "request_created", SeenAt: time.Now()}},
		"Total":          2,
		"Page":           1,
		"TotalPages":     1,
		"PriorityTypes":  []PriorityType{{ID: 1, Slug: "boost", Label: "Boost", Mode: "multiple", Active: true}},
		"Pagination":     template.HTML("<nav>pager</nav>"),
		"JustCreatedID":  int64(1),
		"UpscaleOptions": []UpscaleOption{{Key: "upscale_anime_2x", Label: "Anime 2x", Content: "anime", Scale: 2}},
		"Prefill":        map[string]string{"title": "", "anime_id": "", "season": "", "episodes": "", "resolution": "", "source": ""},
		"OpenForm":       true,
		"ErrorMessage":   "Title is required.",
		"AllowedHosts":   allowedRequestHosts,
		"ViewerID":       7,
		"AnimeID":        0,
		"FeedSource":     "",
		"FeedStatus":     "",
		"Query":          "",
	}
}

func detailPageData() gin.H {
	req := sampleRequest(1)
	nzbID := int64(9)
	return gin.H{
		"Request": req,
		"Anime":   &Anime{AID: 42, Title: "Great Show", Episodes: 12, Format: "TV", CoverLarge: "/covers/42.jpg"},
		"Voted":   true,
		"ActiveLock": &RequestLock{ID: 1, RequestID: 1, Status: "downloading", Progress: "42%",
			Speed: "3 MB/s", LockedAt: time.Now(), UpdatedAt: time.Now(), AgentName: "agent-1"},
		"FailedLock": &RequestLock{ID: 2, RequestID: 1, Status: "failed", FailReason: "no peers",
			LockedAt: time.Now(), UpdatedAt: time.Now()},
		"QueuePos": 3, "BoostCost": 15,
		"Breakdown":     BoostCostBreakdown{BaseCost: 5, SeedMult: 100, SeedLabel: "12 seeders", FinalCost: 15},
		"UserPoints":    100,
		"PriorityTotal": 9,
		"Priorities":    req.Priorities,
		"PriorityTypes": []PriorityType{{ID: 1, Slug: "boost", Label: "Boost", Mode: "multiple", Active: true}},
		"CanDelete":     true, "CanEdit": true, "CanUnpark": true,
		"Duplicate": false, "LiveNzbID": nzbID, "NzbRemoved": false, "Revived": false,
		"Actions": []*RequestAction{{ID: 1, RequestID: 1, ProposerID: 7, Action: "edit",
			Reason: "typo", Status: "pending", CreatedAt: time.Now(), ProposerUsername: "tester", RequestTitle: "Great Show"}},
		"CanPropose": true, "IsMod": true, "InQueue": false,
	}
}

func TestListPageRenders(t *testing.T) {
	out := testRender(t, "community_requests.html", listPageData("open"))
	for _, want := range []string{"Great Show S02E04", "Title is required.", "<nav>pager</nav>", "nzb-card"} {
		if !strings.Contains(out, want) {
			t.Errorf("open tab missing %q", want)
		}
	}

	feed := testRender(t, "community_requests.html", listPageData("feed"))
	if !strings.Contains(feed, "Feed Item") {
		t.Error("feed tab did not render the feed item")
	}
	// The status filter's derived options (the host's ListFeedItems
	// translates these sentinels into the open-request join).
	for _, want := range []string{`value="not_queued"`, `value="queued"`} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed status filter missing option %s", want)
		}
	}
}

func TestDetailPageRenders(t *testing.T) {
	out := testRender(t, "community_request_detail.html", detailPageData())
	for _, want := range []string{"Great Show S02E04", "42%", "3 MB/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// The failed-lock card renders only when no agent is actively working.
	data := detailPageData()
	data["ActiveLock"] = (*RequestLock)(nil)
	out = testRender(t, "community_request_detail.html", data)
	if !strings.Contains(out, "no peers") {
		t.Error("failed-lock card did not render its FailReason")
	}
}

// Every POST form must carry the CSRF field: the host gate rejects a POST
// without one, and a missing hidden input is invisible on the rendered page,
// so the only symptom is a 403 on submit. (The messages plugin's compose form
// shipped without one and nobody could start a conversation for 77 days.)
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for _, tc := range []struct {
		label string
		tmpl  string
		data  gin.H
	}{
		{"open tab", "community_requests.html", listPageData("open")},
		{"backlog tab", "community_requests.html", backlogPageData()},
		{"detail", "community_request_detail.html", detailPageData()},
	} {
		t.Run(tc.label, func(t *testing.T) {
			out := testRender(t, tc.tmpl, tc.data)
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

// A page over empty data must still render — a range over nothing is where a
// missing {{else}} shows up.
func TestPagesRenderEmpty(t *testing.T) {
	empty := gin.H{
		"Tab": "open", "Requests": []*Request{}, "Total": 0, "Page": 1, "TotalPages": 1,
		"Prefill":    map[string]string{},
		"Pagination": template.HTML(""),
	}
	out := testRender(t, "community_requests.html", empty)
	if !strings.Contains(out, "No requests yet") {
		t.Error("empty state did not render")
	}
}
