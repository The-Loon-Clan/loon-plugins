package lists

import (
	"embed"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The /lists pages, owned by the plugin that owns lists.
//
// The HOST used to carry all four — 608 lines in web/templates, reading the
// host's own list record directly. That coupling is why the plugin had to
// pass rows through opaquely: the markup and the struct were the same
// decision, made on the host's side. With the markup here, the plugin renders
// from its own List type and the host record stops leaking through.
//
// Embedded rather than added to the host's glob: a missing template is a
// build error here, not a 500 at runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/*.html"))

// The view models are STRUCTS, deliberately, not the map you would reach for
// first. A map answers a missing key with the empty value and no error, so
// markup that reads a field nobody supplies renders as if the answer were
// "no" — which is how the Report button on the detail page came to be gated
// on a value that stopped existing when these templates moved: silently, with
// every test still green. Against a struct that is a render error, and the
// test below catches it.

type userListsVM struct {
	Lists    []List
	Followed []List
}

type listDetailVM struct {
	List        *List
	Items       []Item
	IsFollowing bool
	IsOwner     bool
	// ViewerID is 0 for an anonymous viewer. The page uses it only to hide
	// "report this list" from the list's own owner.
	ViewerID    int
	NzbCardCSS  template.HTML
	ReportModal template.HTML
}

type releaseListsVM struct {
	Nzb   *NzbRef
	Lists []List
}

type watchlistsVM struct {
	Lists      []List
	NzbCardCSS template.HTML
}

// render executes one fragment. Errors are returned rather than written so
// the caller decides the status — a half-rendered page must not go out as a
// 200 (html/template streams, so a bad field truncates rather than fails).
func render(name string, vm any) (template.HTML, error) {
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// page renders a fragment and hands it to the host for chrome. A render
// failure becomes the site's error page rather than a truncated 200.
func page(c *gin.Context, title, name string, vm any) {
	frag, err := render(name, vm)
	if err != nil {
		deps.RenderError(c, 500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, frag)
}
