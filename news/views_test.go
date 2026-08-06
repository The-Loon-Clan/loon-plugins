package news

import (
	"html/template"
	"strings"
	"testing"
	"time"
)

// Lifted markup, so executing it is the only proof the data still matches what
// it reads. html/template streams: a field the markup wants and the data lacks
// aborts the render part way through and returns half a page silently.

func render1(t *testing.T, name string, data map[string]any) string {
	t.Helper()
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
	return s
}

// safePost is declared inside the handlers, so the test mirrors its shape
// rather than importing it. html/template is duck-typed, and what is being
// pinned here is the TEMPLATE's contract — which is the thing that drifts.
type safePostView struct {
	ID        int64
	Title     string
	Slug      string
	Body      template.HTML
	CreatedAt interface{}
}

func sampleSafe() safePostView {
	return safePostView{1, "Server maintenance", "server-maintenance",
		template.HTML("<p>back shortly</p>"), time.Now()}
}

func TestNewsPagesRender(t *testing.T) {
	got := render1(t, "news.html", map[string]any{"News": []safePostView{sampleSafe()}})
	for _, want := range []string{"Server maintenance", "<p>back shortly</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("news index missing %q", want)
		}
	}
	detail := render1(t, "news_detail.html", map[string]any{"Post": sampleSafe()})
	if !strings.Contains(detail, "<p>back shortly</p>") {
		t.Error("detail page did not place the sanitised body")
	}
}

func TestNewsAdminPagesRender(t *testing.T) {
	posts := []NewsPost{{ID: 1, Title: "Server maintenance", Slug: "server-maintenance",
		Body: "back shortly", Published: true, CreatedAt: time.Now()}}
	got := render1(t, "admin_news.html", map[string]any{"Posts": posts})
	if !strings.Contains(got, "Server maintenance") {
		t.Error("admin list missing the post")
	}
	// The form renders for both create (no post) and edit.
	render1(t, "admin_news_form.html", map[string]any{"Post": NewsPost{}})
	render1(t, "admin_news_form.html", map[string]any{"Post": posts[0]})
}

// The empty state is what a fresh install shows, and a range over nothing is
// where a missing {{else}} surfaces.
func TestNewsPagesRenderEmpty(t *testing.T) {
	render1(t, "news.html", map[string]any{"News": []safePostView{}})
	render1(t, "admin_news.html", map[string]any{"Posts": []NewsPost{}})
}
