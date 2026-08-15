package news

import (
	"html/template"
	"io/fs"
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
// The index and the detail page take DIFFERENT shapes — Excerpt is plain text
// for the list, Body is sanitised HTML for the page — and this one struct
// carries both so a single fixture drives both templates.
type safePostView struct {
	ID        int64
	Title     string
	Slug      string
	Excerpt   string
	Age       string
	Body      template.HTML
	CreatedAt interface{}
}

func sampleSafe() safePostView {
	return safePostView{1, "Server maintenance", "server-maintenance",
		"back shortly", "2 days ago", template.HTML("<p>back shortly</p>"), time.Now()}
}

func TestNewsPagesRender(t *testing.T) {
	got := render1(t, "news.html", map[string]any{"News": []safePostView{sampleSafe()}})
	for _, want := range []string{"Server maintenance", "back shortly", "2 days ago",
		`href="/news/server-maintenance"`} {
		if !strings.Contains(got, want) {
			t.Errorf("news index missing %q", want)
		}
	}
	// The index shows an excerpt, so it must NOT be putting markup on the page:
	// the excerpt is plain text and escaping it is the whole reason a truncated
	// body cannot leave a tag open.
	if strings.Contains(got, "<p>back shortly</p>") {
		t.Error("the index rendered the body as HTML — excerpts are plain text, " +
			"and one cut at a character count leaves tags open")
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

// No template here may pull a script or a stylesheet off a third-party host.
//
// The news feed loaded Bootstrap from cdn.jsdelivr.net for one widget, and on a
// host with a script-src 'self' CSP the request never happened. What that left
// was worse than an unstyled page: the collapse toggles were buttons that did
// nothing, and every post but the first carried aria-expanded="false" over a
// body that was on screen — a lie to a screen reader, told silently, on a page
// that looked fine.
//
// Nothing about that failure is visible from the plugin side, which is why this
// is a test rather than a note. Every template is scanned, not just the one that
// had the tag, because the next one added will be copied from an example that
// has it too.
func TestNoTemplateLoadsFromAThirdPartyHost(t *testing.T) {
	names, err := fs.Glob(pageFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 4 {
		t.Fatalf("found %d templates; the glob is not matching", len(names))
	}
	for _, name := range names {
		b, err := fs.ReadFile(pageFS, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, attr := range []string{`src="http`, `src='http`, `href="http`, `href='http`} {
			if i := strings.Index(string(b), attr); i >= 0 {
				line := 1 + strings.Count(string(b)[:i], "\n")
				t.Errorf("%s:%d loads from an external origin (%s…) — a host with a "+
					"default-src 'self' CSP blocks it, and the feature it powers "+
					"fails without saying so", name, line, attr)
			}
		}
	}
}

// excerpt, and the four ways a summary goes wrong without saying so.
func TestExcerpt(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		max      int
		want     string
	}{
		{"tags become a space, not nothing",
			"<p>one</p><p>two</p>", 100, "one two"},
		{"entities resolve after the tags are gone",
			"<p>Tom &amp; Jerry</p>", 100, "Tom & Jerry"},
		{"an escaped tag stays text rather than becoming one",
			"<p>use &lt;strong&gt; for bold</p>", 100, "use <strong> for bold"},
		{"whitespace from the source collapses",
			"<p>one</p>\n\n   <p>two</p>", 100, "one two"},
		// The other half of "a tag becomes a space": right between blocks,
		// wrong right before a full stop. Found on screen, in the demo post
		// whose body is "Real <b>body</b>. bad" — it read "Real body . bad".
		{"an inline tag does not push punctuation off its word",
			"<p>Real <b>body</b>. bad</p>", 100, "Real body. bad"},
		{"a closing bracket keeps its place too",
			"<p>see the <a href=\"/x\">wiki</a>) for more</p>", 100, "see the wiki) for more"},
		{"the same at the very end of the text",
			"<p>done <b>already</b>.</p>", 100, "done already."},
		// The other direction, and the reason the rule tests what FOLLOWS the
		// punctuation. ? ends a sentence in prose and opens a query string in a
		// URL; the character alone cannot tell you which. This read
		// "The?t=caps response" on the feed until the rule got fussier.
		{"punctuation that starts a token keeps the space before it",
			"<p>The <code>?t=caps</code> response</p>", 100, "The ?t=caps response"},
		{"a bare percent is still a word of its own",
			"<p>seeded <b>19</b> % of the time</p>", 100, "seeded 19 % of the time"},
		{"a line break is a word boundary even though it is written inline",
			"one<br />two", 100, "one two"},
		{"an unterminated < is text, not the start of a tag that eats the post",
			"a < b and that is all", 100, "a < b and that is all"},
		{"short bodies are left alone, with no ellipsis",
			"<p>brief</p>", 100, "brief"},
		{"a long body is cut at a word boundary",
			"<p>alpha beta gamma delta</p>", 12, "alpha beta…"},
		{"trailing punctuation goes with the cut",
			"<p>alpha beta, gamma</p>", 12, "alpha beta…"},
		{"one unbroken token is cut hard rather than dropped entirely",
			"aaaaaaaaaaaaaaaaaaaa", 8, "aaaaaaaa…"},
		{"an empty body is an empty excerpt, not an ellipsis",
			"", 100, ""},
	} {
		if got := excerpt(tc.in, tc.max); got != tc.want {
			t.Errorf("%s:\n excerpt(%q, %d)\n = %q\n want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
	}
}

// The cut is in runes, not bytes.
//
// Slicing a UTF-8 string by byte index splits a multi-byte character and the
// page shows a replacement glyph — the same corruption as the mojibake in the
// release titles, reached from the other end. Every character here is 3 bytes,
// so a byte-based cut lands mid-character on two thirds of the possible limits.
func TestExcerptDoesNotSplitACharacter(t *testing.T) {
	const body = "日本語のニュース記事です。これは長い本文になります。"
	for max := 1; max < 20; max++ {
		got := excerpt(body, max)
		if strings.ContainsRune(got, '\uFFFD') {
			t.Fatalf("max=%d produced a replacement character: %q", max, got)
		}
		// The ellipsis is not part of the budget, so the text itself must not
		// exceed it either.
		if n := len([]rune(strings.TrimSuffix(got, "…"))); n > max {
			t.Errorf("max=%d returned %d runes: %q", max, n, got)
		}
	}
}

// humanAge, at every boundary it has.
//
// The units are what a reader uses to answer "is this new" — and each boundary
// is somewhere an off-by-one shows up as a plainly wrong sentence ("0 days ago"
// on something posted this morning, "24 hours ago" on yesterday).
func TestHumanAge(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "just now"},
		{30 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{90 * time.Second, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{59 * time.Minute, "59 minutes ago"},
		{time.Hour, "1 hour ago"},
		{23 * time.Hour, "23 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{47 * time.Hour, "1 day ago"},
		{2 * 24 * time.Hour, "2 days ago"},
		{29 * 24 * time.Hour, "29 days ago"},
		{30 * 24 * time.Hour, "1 month ago"},
		{89 * 24 * time.Hour, "2 months ago"},
		{364 * 24 * time.Hour, "12 months ago"},
		{365 * 24 * time.Hour, "1 year ago"},
		{800 * 24 * time.Hour, "2 years ago"},
		// A post-dated post, or a clock skew between the app and the database.
		// It must not read "-1 days ago", and it is not an error worth showing.
		{-time.Hour, "just now"},
	} {
		if got := humanAge(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("humanAge(now-%s) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

// Nothing may say "1 minutes ago".
func TestHumanAgeNeverPluralisesOne(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{
		time.Minute, time.Hour, 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour,
	} {
		got := humanAge(now.Add(-d), now)
		if strings.HasPrefix(got, "1 ") && strings.Contains(got, "s ago") {
			t.Errorf("humanAge(now-%s) = %q", d, got)
		}
	}
}
