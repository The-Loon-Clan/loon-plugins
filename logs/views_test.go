package logs

import (
	"context"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// Executing the fragment is the point of this test, not the values it
// produces. html/template streams: a field the markup reads and the data does
// not carry aborts the render PART WAY THROUGH, so the page returns 200 with
// the top half of itself and nothing says why. Building the plugin proves
// nothing about that — only running the template does.
func TestRenderPageExecutesTheWholeFragment(t *testing.T) {
	at := time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC)
	SetDeps(Deps{
		RenderPagination: func(page, pageSize, total int, baseURL string) template.HTML {
			return template.HTML(`<nav id="pg">` + baseURL + `</nav>`)
		},
		JSONOK:            func(*gin.Context, gin.H) {},
		JSONError:         func(*gin.Context, int, string) {},
		JSONInternalError: func(*gin.Context, string, error) {},
		Search: func(context.Context, Search) ([]Row, int, error) {
			return []Row{
				{ID: 7, Severity: "fatal", Op: "usenet/flush", Message: "boom",
					RequestPath: "/admin/x", UserID: 42, Count: 3, LastAt: at},
				{ID: 8, Severity: "warning", Op: "api/search", Message: "slow", Count: 1, LastAt: at},
			}, 120, nil
		},
		Facets: func(context.Context, Search, int) ([]Facet, []Facet, error) {
			return []Facet{{Key: "usenet/flush", Rows: 1, Count: 3}},
				[]Facet{{Key: "fatal", Rows: 1, Count: 3}}, nil
		},
		Histogram: func(context.Context, Search, string) ([]Bucket, error) {
			return []Bucket{{Bucket: at, Rows: 1, Count: 3}}, nil
		},
		Archive: func(context.Context, int64) error { return nil },
	})
	t.Cleanup(func() { deps = nil })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/admin/p/logs?q=boom", nil)

	p := &Plugin{handlers: &Handlers{}}
	html, err := p.renderPage(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(html)

	// The closing marker proves the render reached the end rather than
	// aborting somewhere in the middle.
	if !strings.HasSuffix(strings.TrimSpace(got), "</script>") {
		t.Errorf("fragment looks truncated; it ends with:\n%q", tail(got, 200))
	}
	for _, want := range []string{
		"Log Search",           // header
		"usenet/flush", "boom", // first row
		"api/search", "slow", // SECOND row — a streamed abort typically loses this
		"badge bg-danger",         // severity chrome
		"/admin/users/42",         // the user link off a set pointer
		`action="/admin/p/logs"`,  // the moved form target
		`<nav id="pg">`,           // the host-rendered pager landed
		"/admin/p/logs?q=boom&",   // ...with the query carried into it
		"/admin/logs/search.json", // the JSON endpoints did NOT move
		"log-layout",              // the plugin's own CSS travelled with it
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment missing %q", want)
		}
	}

	// The fragment is body content: the host wrapper owns the chrome, and a
	// stray <html> here would nest a document inside a document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", "<body", `template "navbar"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("fragment carries host chrome it should not: %q", unwanted)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
