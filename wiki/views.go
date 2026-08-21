package wiki

import (
	"embed"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The wiki's six pages, owned by the plugin that owns the wiki.
//
// The HOST used to carry all 2,100 lines of this, which is why the wiki still
// read as present in prod after the Go moved out: the plugin could not change
// its own pages, and the site could not delete them.
//
// Embedded, so a missing template is a build error here rather than a 500 at
// runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

// `add` is the only host helper this markup calls. Reimplemented rather than
// crossed the seam: it is integer addition, and unlike Markdown — which
// sanitises, and so stays a Deps function — there is no answer here to drift.
var pageTmpl = template.Must(template.New("wiki").Funcs(template.FuncMap{
	"add":         func(a, b int) int { return a + b },
	"wikiExcerpt": wikiExcerpt,
}).ParseFS(pageFS, "templates/*.html"))

// Excerpt-shaping patterns, compiled once. They ran per call in the host,
// which is cheap enough on a page of twenty posts but pointless.
var (
	// Fenced code blocks make poor excerpts, so they go first. Written with
	// an escaped backtick rather than a raw string, because the pattern
	// itself contains backticks.
	reFence    = regexp.MustCompile("```[\\s\\S]*?```")
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reMdLink   = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reMdDecor  = regexp.MustCompile("[#*_`>~|]")
	reWhitespc = regexp.MustCompile(`\s+`)
)

// wikiExcerpt reduces wiki markdown to a plain-text preview.
//
// Moved here from the host FuncMap rather than crossed the seam: it is
// presentation for this plugin's own pages and nothing else uses it.
//
// The truncation is rune-aware, which the original was not. It sliced BYTES
// at 140, and on an anime wiki that is a live hazard — cutting mid-rune in a
// CJK title emits invalid UTF-8 into the page. Same class of bug as the
// Postgres one that lost a batch of instrument flushes.
func wikiExcerpt(s string) string {
	s = reFence.ReplaceAllString(s, "")
	s = reTag.ReplaceAllString(s, " ")
	s = reMdLink.ReplaceAllString(s, "$1")
	s = reMdDecor.ReplaceAllString(s, "")
	s = reWhitespc.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	const limit = 140
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// render executes one fragment and hands it to the host for chrome.
// fail renders a refusal in the site's chrome instead of a bare string.
//
// Six handlers used to answer c.String(404, "topic not found") and the like: a
// user-visible sentence living in Go where the translation seam could never
// reach it, served as plain text on a blank page. The reason travels as a code
// and wiki_error.html holds the words.
//
// A method rather than a package function only so handlers read h.fail(...)
// beside h.store; it delegates straight to render.
func (h *Handlers) fail(c *gin.Context, status int, reason string) {
	render(c, status, "Wiki", "wiki_error.html", gin.H{"Reason": reason})
}

func render(c *gin.Context, status int, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	// Legacy contract: render by NAME from the host's own directory. See
	// Deps.BaseData for why this branch exists and when it goes.
	if deps.RenderPage == nil {
		c.HTML(status, name, deps.BaseData(c, data))
		return
	}
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, status, title, template.HTML(sb.String()))
}
