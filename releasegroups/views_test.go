package releasegroups

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// testRender executes one page through the REAL render path — parse, chrome
// injection, fragment execution. Both the map-driven data and the lifted
// markup answer mistakes with silence, so executing every branch over
// realistic data is the net.
func testRender(t *testing.T, name string, data gin.H) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) { captured = body },
		CSRFToken:  func(c *gin.Context) string { return "test-csrf" },
		Markdown:   func(s string) template.HTML { return template.HTML(template.HTMLEscapeString(s)) },
		RenderBioMarkdown: func(s string) template.HTML {
			return template.HTML(template.HTMLEscapeString(s))
		},
		RelativeTime: func(v any) string { return "3 days ago" },
		Viewer: func(c *gin.Context) *Viewer {
			return &Viewer{ID: 7, Username: "tester", Mod: true}
		},
		NzbCardCSS: func() template.HTML { return "<style>/* nzb-card */</style>" },
		BaseURL:    "https://example.test",
	})
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{deps: *deps}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/release-groups", nil)
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

func sampleGroup() *Group {
	when := time.Now().Add(-48 * time.Hour)
	return &Group{
		ID: 7, Name: "SubsPlease", Slug: "subsplease", Status: "confirmed",
		Source: "scraped", WebsiteURL: "https://subsplease.org",
		Description: "fast subs", BioMarkdown: "# hello", LogoURL: "/static/group-logos/subsplease.jpg",
		NzbCountCached: 1234, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		ArchiveLastRefreshAt: &when, NekobtGroupID: "9001", NekobtStatus: "linked",
		ArchiveTorrentCountCached: 55, ScrapeSource: "nekobt",
	}
}

func sampleTorrent(hidden bool) *ArchiveTorrent {
	nzbID := int64(42)
	nyaa := "12345"
	tr := &ArchiveTorrent{
		NekobtTorrentID: "8001", ReleaseGroupID: 7, OurNzbID: &nzbID,
		Title: "[SubsPlease] Great Show - 05 (1080p)", FilesizeBytes: 700 << 20,
		InfoHash:   "aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
		UploadedAt: time.Now().Add(-24 * time.Hour), Seeders: 40,
		AudioLang: "ja", SubLang: "en", ImportedNyaaID: &nyaa,
		LastSeenAt: time.Now(), CreatedAt: time.Now(),
	}
	if hidden {
		now := time.Now()
		tr.HiddenAt = &now
	}
	return tr
}

func listData() gin.H {
	return gin.H{
		"Tab": "confirmed", "Query": "", "Groups": []*Group{sampleGroup()},
		"Total": 1, "ConfirmedCount": 1, "UnknownCount": 3,
		"Page": 1, "TotalPages": 1, "Pagination": template.HTML("<nav>pager</nav>"),
		"Suggested": "",
	}
}

func detailData() gin.H {
	g := sampleGroup()
	claim := &Claim{ID: 1, ReleaseGroupID: 7, UserID: 7, Role: RoleOwner,
		Status: "pending", VerificationToken: "tok123", VerificationURL: "https://subsplease.org",
		VerificationAttempts: 1, CreatedAt: time.Now()}
	return gin.H{
		"Group": g,
		"Nzbs":  []NzbCard{{Card: template.HTML("<div class=\"nzb-card\">card-1</div>")}},
		"Total": 1234, "Page": 1, "TotalPages": 25,
		"Pagination":    template.HTML("<nav>pager</nav>"),
		"Owners":        []*Owner{{ID: 1, ReleaseGroupID: 7, UserID: 9, Username: "owner1", Role: RoleOwner, CreatedAt: time.Now()}},
		"FollowerCount": 12, "ViewerIsOwner": true, "ViewerIsFollowing": true,
		"ViewerClaim": claim,
		"NewsPosts": []*NewsPost{{ID: 5, ReleaseGroupID: 7, AuthorUserID: 9, AuthorName: "owner1",
			Title: "New season!", Body: "we are back", CreatedAt: time.Now()}},
		"NewsTotal":       1,
		"ArchiveTorrents": []*ArchiveTorrent{sampleTorrent(false)},
		"ArchiveTotal":    55, "ArchivePage": 1, "ArchiveTotalPages": 2,
		"ArchivePagination": template.HTML("<nav>tpager</nav>"),
		"BioHTML":           template.HTML("<h1>hello</h1>"),
		"ClaimFlash":        "created", "VerifyFlash": "", "NewsFlash": "", "BioFlash": "",
	}
}

func archiveData() gin.H {
	return gin.H{
		"Group": sampleGroup(), "Torrents": []*ArchiveTorrent{sampleTorrent(false)},
		"Total": 55, "Page": 1, "TotalPages": 2, "ViewerIsOwner": true,
		"RefreshFlash": "started", "BulkFlash": "ok",
		"BulkFiled": "3", "BulkDup": "1", "BulkAlready": "2", "BulkSkipped": "0",
		"HideFlash": "",
	}
}

func TestPagesRender(t *testing.T) {
	out := testRender(t, "release_groups_list.html", listData())
	if !strings.Contains(out, "SubsPlease") {
		t.Error("list page missing the group")
	}

	out = testRender(t, "release_group_detail.html", detailData())
	for _, want := range []string{"SubsPlease", "card-1", "New season!", "<nav>tpager</nav>"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	out = testRender(t, "release_group_archive.html", archiveData())
	if !strings.Contains(out, "Great Show") {
		t.Error("archive page missing the torrent row")
	}

	out = testRender(t, "release_group_suggest.html", gin.H{"Group": sampleGroup(), "IsNew": false})
	if !strings.Contains(out, `name="_csrf"`) {
		t.Error("suggest form lost its CSRF field")
	}

	out = testRender(t, "release_group_bio_edit.html", gin.H{
		"Group": sampleGroup(), "BioSource": "# hello", "BioFlash": "",
	})
	if !strings.Contains(out, "# hello") {
		t.Error("bio editor missing the source")
	}
}

// Every POST form must carry the CSRF field. Six of this surface's forms
// historically relied on the host's submit-time csrf-js injection; the lift
// made them explicit, and this keeps them that way.
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for name, data := range map[string]gin.H{
		"release_groups_list.html":    listData(),
		"release_group_detail.html":   detailData(),
		"release_group_archive.html":  archiveData(),
		"release_group_suggest.html":  {"Group": sampleGroup(), "IsNew": false},
		"release_group_bio_edit.html": {"Group": sampleGroup(), "BioSource": "x", "BioFlash": ""},
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

// Empty data must still render — a range over nothing is where a missing
// {{else}} shows up.
func TestPagesRenderEmpty(t *testing.T) {
	out := testRender(t, "release_groups_list.html", gin.H{
		"Tab": "confirmed", "Groups": []*Group{}, "Total": 0,
		"Page": 1, "TotalPages": 1, "Pagination": template.HTML(""),
	})
	if out == "" {
		t.Error("empty list did not render")
	}
}

func TestStripHTMLTags(t *testing.T) {
	if got := stripHTMLTags("<p>Hello <b>world</b></p> extra"); got != "Hello world extra" {
		t.Errorf("stripHTMLTags = %q", got)
	}
	if got := stripHTMLTags("no tags"); got != "no tags" {
		t.Errorf("plain text mangled: %q", got)
	}
}
