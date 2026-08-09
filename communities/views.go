package communities

import (
	"embed"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The seven /c/* pages, owned by the plugin that owns communities.
//
// The HOST used to carry all 1,083 lines of this markup, which is why the
// surface still read as present in prod after the Go moved out: the plugin
// could not change its own pages, and the site could not delete them (a
// dead-template sweep once flagged all seven and would have broken every
// community page with no compile error).
//
// Embedded, so a missing template is a build error here rather than a 500 at
// runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

// pageTmpl is parsed in Provision — the FuncMap binds deps.RelativeTime,
// which does not exist until SetDeps has run. Nil on the legacy contract,
// where the host renders its own copies of these templates by name.
var pageTmpl *template.Template

func parseTemplates() error {
	t, err := template.New("communities").Funcs(template.FuncMap{
		// The one seam-bound function: the site's time wording.
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		// The rest are copies of the host FuncMap entries these pages
		// rendered with — parity is the lift's contract. They are pure, so
		// the two copies cannot drift toward a silently wrong answer the way
		// a second markdown sanitiser could.
		"initial":     initialRune,
		"wikiExcerpt": communityExcerpt,
		"add":         func(a, b int) int { return a + b },
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("communities: parse templates: %w", err)
	}
	pageTmpl = t
	return nil
}

// The view models are STRUCTS, deliberately, not the gin.H the legacy branch
// still carries. A map answers a missing key with the empty value and no
// error, so markup reading a field nobody supplies renders as if the answer
// were "no" — silently, with every test green. Against a struct that is a
// render error, and the render tests catch it.

// chromeVM carries the viewer-derived keys the fragments read. It is embedded
// in every page VM and populated centrally in render, so no call site can
// forget it — a zero CSRFToken is a form that 403s on submit with nothing
// logged.
type chromeVM struct {
	CSRFToken string
	// User mirrors the truthiness the markup historically tested via
	// BaseData's *models.User: true when someone is signed in. The fragments
	// only gate on it (join buttons, reply form); identity itself flows
	// through Role and the row data.
	User bool
}

func (v *chromeVM) setChrome(csrf string, signedIn bool) {
	v.CSRFToken = csrf
	v.User = signedIn
}

// pageVM is what render accepts: any view model carrying the embedded
// chromeVM.
type pageVM interface {
	setChrome(csrf string, signedIn bool)
}

type communitiesIndexVM struct {
	chromeVM
	Communities []*Community
	// Total is carried for parity with the legacy data map; the markup does
	// not read it today.
	Total      int
	Pagination template.HTML
}

type communityNewVM struct {
	chromeVM
	Error       string
	Slug        string
	Name        string
	Description string
}

type communityViewVM struct {
	chromeVM
	Community       *Community
	Threads         []*CommunityThread
	Total           int
	Pagination      template.HTML
	Rules           []*CommunityRule
	Mods            []*CommunityMod
	Role            CommunityViewerRole
	MyRequest       *CommunityJoinRequest
	PendingCount    int
	Flash           string
	SidebarHTML     template.HTML
	DescriptionHTML template.HTML
}

type communityNewThreadVM struct {
	chromeVM
	Community *Community
	// Editor is the host's shared markdown editor, pre-rendered — the
	// fragment cannot reach the host's md-editor partial (different template
	// set), so it crosses as finished HTML.
	Editor template.HTML
}

// threadPostVM is one reply with its markdown body pre-rendered in Go, so
// the template does not re-invoke the renderer per row. Named (the old
// in-handler anonymous struct) because both contracts now share it.
type threadPostVM struct {
	*CommunityPost
	BodyHTML template.HTML
}

type communityThreadVM struct {
	chromeVM
	Community   *Community
	Thread      *CommunityThread
	BodyHTML    template.HTML
	Posts       []threadPostVM
	Total       int
	Pagination  template.HTML
	Rules       []*CommunityRule
	Mods        []*CommunityMod
	Role        CommunityViewerRole
	SidebarHTML template.HTML
}

type communityJoinRequestsVM struct {
	chromeVM
	Community *Community
	Requests  []*CommunityJoinRequest
	Invites   []*CommunityInvite
	Flash     string
}

type communitySettingsVM struct {
	chromeVM
	Community *Community
	Flash     string
}

// render draws one page: fragment from the plugin's set, chrome from the
// host.
//
// Two contracts, two data shapes, both built by the caller: vm feeds the
// plugin's own fragment on the current contract; legacy is the exact gin.H
// the pre-lift handler passed, still rendered by template NAME through the
// host's BaseData when the host wired the previous contract (loon-demo-site
// does — see Deps.BaseData for when that branch goes). Keeping both shapes
// visible at each call site is what lets them be compared line-by-line
// instead of drifting apart in a conversion helper.
func (h *Handlers) render(c *gin.Context, status int, title, name string, vm pageVM, legacy gin.H) {
	if deps.RenderPage == nil {
		c.HTML(status, name, deps.BaseData(c, legacy))
		return
	}
	vm.setChrome(deps.CSRFToken(c), h.viewerID(c) != 0)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, vm); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// The shared-widget seams, each degrading to nothing on the contract that
// does not carry them — where the other branch's value is an unused key, not
// a missing widget.

// pager is the host's pagination partial as finished HTML (current
// contract).
func pager(page, totalItems int, baseURL string) template.HTML {
	if deps.RenderPagination == nil {
		return ""
	}
	return deps.RenderPagination(page, communityPageSize, totalItems, baseURL)
}

// legacyPager builds the view model the host's own pagination partial
// consumes by field name (previous contract).
func legacyPager(page, totalItems int, baseURL string) any {
	if deps.Pagination == nil {
		return nil
	}
	return deps.Pagination(page, communityPageSize, totalItems, baseURL)
}

// editorHTML is the host's shared markdown editor for the new-thread form,
// with the same options the markup's old dict call passed.
func editorHTML() template.HTML {
	if deps.RenderEditor == nil {
		return ""
	}
	return deps.RenderEditor(map[string]any{
		"Name": "body", "Rows": 10, "Placeholder": "Write your post...", "Required": false,
	})
}

// initialRune is the no-avatar circle's letter — the first RUNE of a name,
// never a byte slice. `slice .Username 0 1` counts BYTES, so a name whose
// first character is multi-byte would put half a rune into the page as
// invalid UTF-8, and an empty one would error mid-stream rather than
// degrade.
func initialRune(name string) string {
	if name == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError && size <= 1 {
		return ""
	}
	return name[:size]
}

// Excerpt-shaping patterns, compiled once rather than per call.
var (
	reFence    = regexp.MustCompile("```[\\s\\S]*?```")
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reMdLink   = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reMdDecor  = regexp.MustCompile("[#*_`>~|]")
	reWhitespc = regexp.MustCompile(`\s+`)
)

// communityExcerpt reduces a thread body to a plain-text preview for the
// community page's row excerpts. Registered as "wikiExcerpt" because that is
// the name the lifted markup calls — the helper was born on the wiki.
//
// The truncation is rune-aware, which the host original was not: it sliced
// BYTES at 140, and cutting mid-rune in a CJK title emits invalid UTF-8 into
// the page. The wiki plugin's lift fixed the same bug the same way.
func communityExcerpt(s string) string {
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
