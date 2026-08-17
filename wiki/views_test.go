package wiki

import (
	"html/template"
	"strings"
	"testing"
)

// 2,100 lines of lifted markup, so executing it is the only thing that proves
// the data these handlers pass still matches what it reads. html/template
// streams: a field the markup wants and the data lacks aborts the render part
// way through and returns half a page with nothing logged.
//
// The data below mirrors what the handlers actually pass, read off the render
// calls rather than invented — a test built from guessed keys would prove the
// template renders SOMETHING, which is not the question.

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
	if strings.Contains(s, "bootstrap.bundle.min.js") {
		t.Errorf("%s owns the Bootstrap runtime; the host page wrapper must load it exactly once", name)
	}
	return s
}

func sampleTopic() *Topic {
	return &Topic{ID: 1, Name: "Getting Started", Slug: "getting-started",
		Description: "how to begin", Icon: "book", SortOrder: 1}
}

func samplePost() *Post {
	return &Post{ID: 2, TopicID: 1, Title: "First Steps", Slug: "first-steps",
		Content: "# hello", ViewCount: 3}
}

// RecentPosts and PopularPosts are RecentPost, not Post — they carry the
// joined topic name and slug the index links through. Using the wrong one is
// what the render test caught.
func sampleRecent() *RecentPost {
	return &RecentPost{ID: 2, TopicID: 1, Title: "First Steps", Slug: "first-steps",
		TopicName: "Getting Started", TopicSlug: "getting-started",
		CreatedByUsername: "kirisame", ViewCount: 3}
}

func topicsAndPosts() (topics []*Topic, posts []*Post) {
	topics = []*Topic{sampleTopic()}
	posts = []*Post{samplePost()}
	return
}

// The three icon defines were nested inside the page in the host file, where
// that parses because there is no outer wrapper. Wrapping the page for the
// plugin forced them out to siblings; this is what proves they still resolve.
// The negative assertions pin the 2026-08-17 declutter: no explorer rail, no
// right-rail cards, no banner hero — one column and a compact header.
func TestWikiIndexRendersWithItsIcons(t *testing.T) {
	topics, _ := topicsAndPosts()
	got := render1(t, "wiki.html", map[string]any{
		"Topics": topics, "RecentPosts": []*RecentPost{sampleRecent()},
		"PopularPosts": []*RecentPost{sampleRecent()},
	})
	for _, want := range []string{"Getting Started", "First Steps", "Recent Updates", "Popular Articles", "<svg"} {
		if !strings.Contains(got, want) {
			t.Errorf("wiki index missing %q", want)
		}
	}
	for _, gone := range []string{"wiki-shell", "wiki-rail", "wiki-hero", "Wiki Explorer", "Wiki Stats"} {
		if strings.Contains(got, gone) {
			t.Errorf("wiki index still carries the retired %q", gone)
		}
	}
}

// The same page in its recent-changes mode, which is a different branch.
func TestWikiRecentOnlyView(t *testing.T) {
	topics, _ := topicsAndPosts()
	render1(t, "wiki.html", map[string]any{
		"Topics": topics, "RecentPosts": []*RecentPost{sampleRecent()},
		"RecentOnlyView": true,
	})
}

func TestWikiTopicAndPostRender(t *testing.T) {
	_, posts := topicsAndPosts()
	topic := render1(t, "wiki_topic.html", map[string]any{
		"Topic": sampleTopic(), "Posts": posts,
	})
	if !strings.Contains(topic, "Getting Started") {
		t.Error("topic page missing its title")
	}
	if strings.Contains(topic, "folder-toggle") {
		t.Error("topic page still carries the retired explorer sidebar")
	}
	post := render1(t, "wiki_post.html", map[string]any{
		"Topic": sampleTopic(), "Post": samplePost(),
		"RenderedContent": template.HTML("<p>hello</p>"),
	})
	if !strings.Contains(post, "<p>hello</p>") {
		t.Error("post page did not place the rendered markdown")
	}
	if strings.Contains(post, "folder-toggle") {
		t.Error("post page still carries the retired explorer sidebar")
	}
}

func TestWikiAdminPagesRender(t *testing.T) {
	topics, posts := topicsAndPosts()
	render1(t, "admin_wiki.html", map[string]any{
		"Topics": topics, "PostsByTopic": map[int][]*Post{1: posts},
	})
	// Both admin forms render twice each in the handlers — once for create,
	// once for edit — and the edit branch is the one carrying a record.
	render1(t, "admin_wiki_topic_form.html", map[string]any{
		"Action": "/admin/wiki/topics/new", "Icons": TopicIcons})
	render1(t, "admin_wiki_topic_form.html", map[string]any{
		"Action": "/admin/wiki/topics/1", "Icons": TopicIcons, "Topic": sampleTopic()})
	render1(t, "admin_wiki_post_form.html", map[string]any{
		"Action": "/admin/wiki/posts/new", "TopicID": 1})
	render1(t, "admin_wiki_post_form.html", map[string]any{
		"Action": "/admin/wiki/posts/2", "Post": samplePost()})
}

// The empty state is what a fresh install shows, and a range over nothing is
// where a missing {{else}} surfaces.
func TestWikiPagesRenderEmpty(t *testing.T) {
	render1(t, "wiki.html", map[string]any{
		"Topics": []*Topic{}, "RecentPosts": []*RecentPost{}, "PopularPosts": []*RecentPost{}})
	render1(t, "admin_wiki.html", map[string]any{
		"Topics": []*Topic{}, "PostsByTopic": map[int][]*Post{}})
}

// Every POST form must carry the CSRF field.
//
// This plugin SHIPPED without the CSRFToken seam: all seven admin forms posted
// tokenless and the host's CSRF middleware refused each with 403 — the wiki
// admin could not create or delete anything, and nothing said so. The access
// audit probes destructive routes WITH a valid token by design (it tests the
// gate, not the form), so counting tokens in the RENDERED output is the only
// check that catches this class.
func TestEveryWikiPostFormCarriesTheCSRFField(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"admin_wiki.html": {"Topics": []*Topic{sampleTopic()},
			"PostsByTopic": map[int][]*Post{1: {samplePost()}},
			"CSRFToken":    "test-csrf"},
		"admin_wiki_topic_form.html": {"Action": "/admin/wiki/topics/1", "Icons": TopicIcons,
			"Topic": sampleTopic(), "CSRFToken": "test-csrf"},
		"admin_wiki_post_form.html": {"Action": "/admin/wiki/posts/2", "Post": samplePost(),
			"CSRFToken": "test-csrf"},
	} {
		t.Run(name, func(t *testing.T) {
			out := render1(t, name, data)
			forms := strings.Count(out, `method="POST"`)
			tokens := strings.Count(out, `name="_csrf" value="test-csrf"`)
			if forms == 0 {
				t.Fatal("no POST form rendered — the branch under test did not open")
			}
			if tokens != forms {
				t.Errorf("%d POST forms but %d CSRF fields — a form would 403 on submit", forms, tokens)
			}
		})
	}
}
