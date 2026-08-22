package playlists

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// These four templates had never been executed by a test in either repo.
//
// They lived in the HOST's plugin set until this plugin took over rendering
// them, so nothing here could reach them; and the host's own fixture list
// executed them with chrome keys rather than with what the handlers pass. That
// gap is the reason to write these first rather than after: html/template
// STREAMS, so a field the markup wants and the data lacks aborts the render
// part way through and returns half a page with nothing logged.
//
// Fixtures mirror the keys the handlers actually pass, read off the render
// calls rather than invented — a test built from guessed keys proves the
// template renders SOMETHING, which is not the question.

func testRender(t *testing.T, name string, signedIn bool, data gin.H) string {
	t.Helper()
	var captured template.HTML
	var gotStatus int
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) {
			captured, gotStatus = body, status
		},
		CSRFToken: func(c *gin.Context) string { return "test-csrf" },
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return `<nav id="pg">pager</nav>`
		},
		RelativeTime: func(any) string { return "3 days ago" },
		PageOffset:   func(page, pageSize int) int { return 0 },
	})
	t.Cleanup(func() { deps = Deps{}; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	h := &Handlers{auth: testAuth(signedIn)}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/playlists", nil)
	h.render(c, 200, name, data)
	if captured == "" {
		// Re-execute directly to surface the underlying template error in the
		// failure message.
		var sb strings.Builder
		err := pageTmpl.ExecuteTemplate(&sb, name, data)
		t.Fatalf("%s rendered empty — template error: %v", name, err)
	}
	if gotStatus != 200 {
		t.Errorf("%s: status %d crossed the seam, want 200", name, gotStatus)
	}
	out := string(captured)
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "fhead"`, `template "ffoot"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return out
}

// viewer() reads the Core's auth service, so signing somebody in means
// standing one up rather than setting a context key.
func testAuth(signedIn bool) core.AuthService {
	return core.NewAuth(core.AuthAdapter{
		CurrentUserFn: func(c *gin.Context) (*core.User, bool) {
			if !signedIn {
				return nil, false
			}
			return &core.User{ID: 7, Username: "curator"}, true
		},
	})
}

func samplePlaylist() *Playlist {
	return &Playlist{
		ID: 1, UserID: 7, Slug: "best-of", Name: "Best of 2026",
		Description: "The ones worth keeping.", Public: true,
		Username: "curator", ItemCount: 2,
		CreatedAt: time.Now().Add(-72 * time.Hour), UpdatedAt: time.Now(),
	}
}

func TestPagesRender(t *testing.T) {
	idx := testRender(t, "playlists_index.html", true, gin.H{
		"Playlists":      []*Playlist{samplePlaylist(), samplePlaylist()},
		"Total":          2,
		"PaginationHTML": template.HTML(`<nav id="pg">pager</nav>`),
	})
	for _, want := range []string{"Best of 2026", "The ones worth keeping.", `<nav id="pg">`} {
		if !strings.Contains(idx, want) {
			t.Errorf("the index is missing %q", want)
		}
	}

	view := testRender(t, "playlist_view.html", true, gin.H{
		"Playlist": samplePlaylist(),
		"Items": []*Item{
			{ID: 1, PlaylistID: 1, ReleaseID: 99, Note: "the good one",
				AddedAt: time.Now().Add(-time.Hour),
				Release: &Release{ID: 99, Title: "Some.Release.2026", Category: "TV", Size: "1.0 GiB"}},
			// An id whose release is gone: retention removes releases and a
			// collection outliving its contents is normal. The row must still
			// render, so a curator can see what they lost.
			{ID: 2, PlaylistID: 1, ReleaseID: 100, AddedAt: time.Now()},
		},
		"IsOwner": true,
	})
	for _, want := range []string{"Best of 2026", "Some.Release.2026", "the good one"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view page is missing %q", want)
		}
	}

	testRender(t, "playlist_form.html", true, gin.H{"Action": "Create"})
	testRender(t, "playlist_error.html", false, gin.H{"Reason": "notfound"})
}

// The empty state is what a fresh install shows, and a range over nothing is
// where a missing {{else}} surfaces.
func TestPagesRenderEmpty(t *testing.T) {
	testRender(t, "playlists_index.html", false, gin.H{
		"Playlists": nil, "Total": 0, "PaginationHTML": template.HTML(""),
	})
	testRender(t, "playlist_view.html", false, gin.H{
		"Playlist": samplePlaylist(), "Items": nil, "IsOwner": false,
	})
}

// Every POST form must carry the token render() injects, or it 403s on submit
// and the page still LOOKS right. The host's CSRF middleware is what refuses
// it, so nothing in this repo would notice.
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for name, data := range map[string]gin.H{
		"playlist_form.html": {"Action": "Create"},
		"playlist_view.html": {"Playlist": samplePlaylist(), "Items": nil, "IsOwner": true},
	} {
		out := testRender(t, name, true, data)
		forms := strings.Count(strings.ToLower(out), `method="post"`)
		tokens := strings.Count(out, `name="_csrf" value="test-csrf"`)
		if forms == 0 {
			t.Fatalf("%s: no POST form rendered — the branch under test did not open", name)
		}
		if tokens != forms {
			t.Errorf("%s: %d POST forms but %d CSRF fields — a form would 403 on submit",
				name, forms, tokens)
		}
	}
}

// SignedIn gates the create link. It arrived from BaseData's chrome map before
// the lift, where forgetting it was not possible; render() injects it now, and
// this is what proves the markup still reads what render() writes.
func TestTheCreateLinkIsGatedOnBeingSignedIn(t *testing.T) {
	anon := testRender(t, "playlists_index.html", false, gin.H{
		"Playlists": nil, "Total": 0, "PaginationHTML": template.HTML(""),
	})
	member := testRender(t, "playlists_index.html", true, gin.H{
		"Playlists": nil, "Total": 0, "PaginationHTML": template.HTML(""),
	})
	if strings.Contains(anon, "/playlists/new") {
		t.Error("a signed-out visitor was offered the create link")
	}
	if !strings.Contains(member, "/playlists/new") {
		t.Error("a signed-in member was not offered the create link")
	}
}

// Every reason code renders its own sentence. An {{if}} chain that falls
// through renders the generic arm and looks fine, which is why each is checked
// against the generic one rather than merely for being non-empty.
func TestErrorPageRendersEveryReason(t *testing.T) {
	generic := testRender(t, "playlist_error.html", false, gin.H{"Reason": ""})
	// The codes fail() actually passes. Checked against the handler rather
	// than guessed: my first list was the FORM's codes (noname, nametaken,
	// createfailed), which this page has never handled and never should — they
	// are validation failures that re-render the form, not refusals.
	for _, reason := range []string{"notfound", "loadfailed", "savefailed",
		"addfailed", "removefailed", "deletefailed"} {
		got := testRender(t, "playlist_error.html", false, gin.H{"Reason": reason})
		if got == generic {
			t.Errorf("%q renders the generic message — its arm never matched", reason)
		}
	}
}

// The create form re-renders on a validation failure with a CODE, and each has
// its own sentence. Separate from the error page's codes on purpose: these are
// failures the member can fix by editing the form, not refusals.
func TestFormRendersEveryValidationCode(t *testing.T) {
	generic := testRender(t, "playlist_form.html", true, gin.H{"Action": "Create", "Error": ""})
	for _, code := range []string{"noname", "nametaken", "createfailed", "loadfailed"} {
		got := testRender(t, "playlist_form.html", true, gin.H{"Action": "Create", "Error": code})
		if got == generic {
			t.Errorf("%q renders the generic message — its arm never matched", code)
		}
	}
}

// A validation failure must hand the member back what they typed. The handler
// echoes Name/Description/CoverURL/Public for exactly this, and a form that
// drops them makes the member retype everything to fix one field.
func TestFormRepopulatesAfterAValidationFailure(t *testing.T) {
	out := testRender(t, "playlist_form.html", true, gin.H{
		"Action": "Create", "Error": "nametaken",
		"Name": "Best of 2026", "Description": "The ones worth keeping.",
		"CoverURL": "https://example.test/c.png", "Public": true,
	})
	for _, want := range []string{"Best of 2026", "The ones worth keeping.",
		"https://example.test/c.png", "checked"} {
		if !strings.Contains(out, want) {
			t.Errorf("the form lost %q — the member would retype it", want)
		}
	}
}
