package tickets

import (
	"github.com/gin-gonic/gin"
	"html/template"
	"strings"
	"testing"
	"time"
)

// Lifted markup, so executing it is the only proof the data still matches what
// it reads. html/template streams: a field the markup wants and the data lacks
// aborts the render part way through and returns half a page silently.

// Templates bind the markdown seam at Provision, so a test that renders has
// to stand in for it. Deliberately NOT a real markdown renderer: these tests
// are about the markup, and a second sanitiser here would be the very thing
// the seam exists to prevent.
func withTemplates(t *testing.T) {
	t.Helper()
	SetDeps(Deps{Markdown: func(s string) template.HTML { return template.HTML(s) }})
	parseTemplates()
	t.Cleanup(func() { deps = nil })
}

func render1(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	// Bound every time: a previous test's cleanup nils deps while leaving
	// pageTmpl set, and the markdown closure would then deref nil.
	withTemplates(t)
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := sb.String()
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return s
}

func sampleTicket() *SupportTicket {
	return &SupportTicket{
		ID: 7, UserID: 42, Username: "kirisame", Subject: "cannot download",
		Body: "it fails", Status: "open", CreatedAt: time.Now(),
	}
}

func TestSupportPageRenders(t *testing.T) {
	got := render1(t, "support.html", map[string]any{
		"Tickets":    []*SupportTicket{sampleTicket()},
		"EditorHTML": template.HTML(`<div id="md-ed"></div>`),
	})
	for _, want := range []string{"cannot download", `<div id="md-ed">`} {
		if !strings.Contains(got, want) {
			t.Errorf("support page missing %q", want)
		}
	}
}

// The owner-only visibility toggle is GONE (the public-tickets experiment was
// retired 2026-08-16) — owner, stranger and anonymous now render the same
// page, and none of them may see the old toggle. Who can LOAD the page at all
// is ticketVisibleTo's job, tested in tickets_test.go.
func TestTicketPageHasNoVisibilityToggle(t *testing.T) {
	data := func(viewer int) map[string]any {
		return map[string]any{
			"Ticket": sampleTicket(), "Replies": []*TicketReply{},
			"EditorHTML": template.HTML(`<div id="md-ed"></div>`),
			"ViewerID":   viewer,
		}
	}
	for _, viewer := range []int{42, 999, 0} {
		got := render1(t, "support_ticket.html", data(viewer))
		if strings.Contains(got, "/visibility") || strings.Contains(got, "Make public") {
			t.Errorf("viewer %d still sees the retired visibility toggle", viewer)
		}
	}
}

func TestAdminListRenders(t *testing.T) {
	got := render1(t, "admin_tickets.html", map[string]any{
		"Tickets":        []*SupportTicket{sampleTicket()},
		"Total":          1,
		"PaginationHTML": template.HTML(`<nav id="pg"></nav>`),
	})
	if !strings.Contains(got, `<nav id="pg">`) {
		t.Error("admin_tickets.html did not place the host-rendered pager")
	}
	if !strings.Contains(got, "cannot download") {
		t.Error("admin_tickets.html missing the ticket")
	}
}

// The empty state is what a fresh install shows, and a range over nothing is
// where a missing {{else}} surfaces.
func TestTicketPagesRenderEmpty(t *testing.T) {
	render1(t, "support.html", map[string]any{
		"Tickets": []*SupportTicket{}, "EditorHTML": template.HTML("")})
	render1(t, "admin_tickets.html", map[string]any{
		"Tickets": []*SupportTicket{}, "Total": 0, "PaginationHTML": template.HTML("")})
}

// The legacy contract must keep working, because loon-demo-site still uses it
// and builds against this working tree. A host that sets BaseData instead of
// RenderPage renders by template NAME from its own directory — so `ready()`
// must accept it, and nothing on the render path may assume the modern seams
// are present.
func TestLegacyContractIsStillAccepted(t *testing.T) {
	t.Cleanup(func() { deps = nil })

	SetDeps(Deps{
		Viewer:     func(*gin.Context) *Viewer { return nil },
		PageOffset: func(p, n int) int { return 0 },
		BaseData:   func(c *gin.Context, extra gin.H) gin.H { return extra },
		Pagination: func(p, n, t int, u string) any { return nil },
	})
	if !deps.ready() {
		t.Fatal("a host on the previous contract is refused — this breaks loon-demo-site")
	}
	// The optional-seam helpers must degrade rather than deref nil.
	if editor(newTicketEditor) != "" || paginate(1, 10, 0, "/x") != "" {
		t.Error("the legacy path should yield empty chrome, not panic or invent markup")
	}

	// And half of each contract is not a contract.
	SetDeps(Deps{
		Viewer:     func(*gin.Context) *Viewer { return nil },
		PageOffset: func(p, n int) int { return 0 },
		BaseData:   func(c *gin.Context, extra gin.H) gin.H { return extra },
	})
	if deps.ready() {
		t.Error("a half-wired host was accepted; it would render some pages and blank others")
	}
}
