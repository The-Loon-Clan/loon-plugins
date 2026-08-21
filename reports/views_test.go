package reports

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() { gin.SetMode(gin.TestMode) }

func sample(id int64, reason string) Report {
	return Report{
		ID: id, NzbID: id * 10, NzbTitle: "Release." + reason + ".1080p",
		Username: "reporter" + reason, Reason: reason,
		Detail:    "it went wrong",
		CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
}

func withDeps(t *testing.T, rows []Report, total int) {
	t.Helper()
	SetDeps(Deps{
		RenderPagination: func(page, size, total int, base string) template.HTML {
			return template.HTML(`<nav id="pg">` + base + `</nav>`)
		},
		List:        func(context.Context, bool, int, int) ([]Report, int, error) { return rows, total, nil },
		Resolve:     func(context.Context, int64, int) error { return nil },
		ActingAdmin: func(*gin.Context) int { return 99 },
	})
	t.Cleanup(func() { deps = nil })
}

func get(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, w
}

// The fragment is EXECUTED, because html/template streams: a field the markup
// reads and the view model lacks aborts the render part way through and
// returns half a page with nothing logged.
func TestRenderQueue(t *testing.T) {
	withDeps(t, []Report{sample(1, "malware"), sample(2, "broken"), sample(3, "mislabeled")}, 3)
	c, _ := get(t, "/admin/p/reports")

	html, err := (&Plugin{}).render(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(html)
	for _, want := range []string{
		"Open Reports",
		"Release.malware.1080p", "Release.broken.1080p", "Release.mislabeled.1080p",
		"tag--danger", "tag--warning", // malware and broken keep their colours
		"/release/10",   // links back to the release
		`<nav id="pg">`, // host-rendered pager landed
		`action="/admin/p/reports/resolve"`,
		`name="id" value="1"`, // the id travels in the body, not the path
	} {
		if !strings.Contains(got, want) {
			t.Errorf("queue missing %q", want)
		}
	}
	// Body content only — the host wrapper owns the document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("fragment carries host chrome: %q", unwanted)
		}
	}
}

// A reason this plugin does not know must render as itself. The host page
// labelled every unrecognised reason "Mislabeled", which on a site with
// different reasons would misreport what a member actually said.
func TestUnknownReasonIsNotRelabelled(t *testing.T) {
	withDeps(t, []Report{sample(1, "spam")}, 1)
	c, _ := get(t, "/admin/p/reports")
	html, _ := (&Plugin{}).render(c)
	got := string(html)
	if !strings.Contains(got, ">spam<") {
		t.Errorf("an unknown reason should render as itself; got:\n%s", got)
	}
	if strings.Contains(got, "Mislabeled") {
		t.Error(`"spam" was relabelled as "Mislabeled"`)
	}
}

// The resolved tab hides the Resolve button — there is nothing to clear — and
// the empty state must still render the whole page.
func TestResolvedTabAndEmptyState(t *testing.T) {
	withDeps(t, nil, 0)
	c, _ := get(t, "/admin/p/reports?resolved=1")
	html, err := (&Plugin{}).render(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(html)
	if !strings.Contains(got, "Resolved Reports") {
		t.Error("resolved tab did not render its own heading")
	}
	if !strings.Contains(got, "No reports.") {
		t.Error("empty state missing")
	}
	if strings.Contains(got, "/admin/p/reports/resolve") {
		t.Error("the Resolve button appears on the resolved tab")
	}
}

// Clearing a report must return to the tab and page the operator was on.
// Bouncing them to page one of the other list after every action is how a
// queue of 28 becomes unworkable.
func TestResolveReturnsToTheSameView(t *testing.T) {
	withDeps(t, nil, 0)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"id": {"7"}, "resolved": {"1"}, "page": {"3"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/p/reports/resolve",
		strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	frag, err := (&Plugin{}).resolve(c)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if frag != "" {
		t.Error("a redirecting action must return an empty fragment")
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "resolved=1") || !strings.Contains(loc, "page=3") {
		t.Errorf("redirect lost the operator's place: %q", loc)
	}
}

func TestResolveRejectsABadID(t *testing.T) {
	withDeps(t, nil, 0)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/p/reports/resolve",
		strings.NewReader("id=notanumber"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := (&Plugin{}).resolve(c); err == nil {
		t.Error("a non-numeric id should be refused")
	}
}

func TestProvisionRequiresFullDeps(t *testing.T) {
	t.Cleanup(func() { deps = nil })
	SetDeps(Deps{})
	if err := (&Plugin{}).Provision(&core.Core{Process: "web"}); err == nil {
		t.Error("Provision accepted an incomplete Deps")
	}
}
