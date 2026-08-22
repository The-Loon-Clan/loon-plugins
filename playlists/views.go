package playlists

import (
	"embed"
	"hash/fnv"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed templates/*.html
var pageFS embed.FS

var pageTmpl *template.Template

// parseTemplates binds the seam the markup calls and parses the fragments.
//
// Deferred to Provision because relativeTime is a Deps function, and Deps does
// not exist until then. Skipped entirely on the legacy contract, where the host
// renders its own copies of these four templates by name — parsing a set
// nothing will execute is work for nobody.
func parseTemplates() error {
	if deps.RelativeTime == nil {
		return nil
	}
	t, err := template.New("playlists").Funcs(template.FuncMap{
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		"userTag":      userTag,
		"hue":          hueBucket,
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return err
	}
	pageTmpl = t
	return nil
}

// render executes one fragment and hands it to the host for chrome.
//
// SignedIn and CSRFToken are injected HERE rather than at each of the nine
// call sites, and that is the whole reason this is a method. The markup gates
// the "New playlist" link on .SignedIn and every POST form carries
// .CSRFToken; a call site that forgot either would render a page missing its
// create link, or a form that 403s on submit, with nothing logged. They used
// to arrive from the host's BaseData, where forgetting was not possible.
func (h *Handlers) render(c *gin.Context, status int, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}

	// Legacy contract: render by NAME from the host's own template set. See
	// Deps.BaseData for why this branch exists and when it goes.
	if deps.RenderPage == nil {
		c.HTML(status, name, deps.BaseData(c, data))
		return
	}

	data["SignedIn"] = h.viewer(c) != 0
	data["CSRFToken"] = deps.CSRFToken(c)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(http.StatusInternalServerError, "this page failed to render")
		return
	}
	deps.RenderPage(c, status, playlistTitles[name], template.HTML(sb.String()))
}

// playlistTitles is the <title> each page asks the host for.
//
// A map rather than a per-call-site string: nine render sites across four
// pages, and a title passed at the call site is a title two of the five
// playlist_form.html callers eventually spell differently.
var playlistTitles = map[string]string{
	"playlists_index.html": "Playlists",
	"playlist_view.html":   "Playlist",
	"playlist_form.html":   "New playlist",
	"playlist_error.html":  "Playlists",
}

// paginationHTML is the host's finished pager, or empty.
//
// Optional in effect rather than in the contract: a host on the legacy
// contract passes Deps.Pagination and the old markup built its own <nav>; on
// the current one the host renders it and this drops the result in.
func paginationHTML(page, pageSize, totalItems int, baseURL string) template.HTML {
	if deps.RenderPagination == nil {
		return ""
	}
	return deps.RenderPagination(page, pageSize, totalItems, baseURL)
}

// legacyPagination is the host's pagination VIEW-MODEL, or nil.
//
// Only the host's own copies of these templates read it, and only on the
// previous contract. It goes when Deps.Pagination does.
func legacyPagination(page, pageSize, totalItems int, baseURL string) any {
	if deps.Pagination == nil {
		return nil
	}
	return deps.Pagination(page, pageSize, totalItems, baseURL)
}

// userTag is the host's username chip, or a plain link to the profile.
//
// The fallback is deliberately unstyled rather than an approximation of
// somebody's chip: a plugin guessing at a host's role colours gets them wrong
// on every host but one, and wrong colours on a username say something false
// about that member.
func userTag(name string) template.HTML {
	if name == "" {
		return ""
	}
	if deps.RenderUserTag != nil {
		return deps.RenderUserTag(name)
	}
	return template.HTML(`<a href="/u/` + template.HTMLEscapeString(name) + `">` +
		template.HTMLEscapeString(name) + `</a>`)
}

// hueBucket picks one of the eight poster tints for a cover-less playlist.
//
// REIMPLEMENTED rather than crossed, following the rule the other plugins
// state: a helper crosses the seam when the two copies could disagree silently
// AND the wrong answer is dangerous. This one cannot be dangerous — the worst
// a drifted copy does is tint a tile differently.
//
// The 8 is not arbitrary: the host's CSS defines .poster--h0 … .poster--h7, and
// the template indexes the class straight off this number. A bucket outside
// that range is a class nothing defines, which renders as no tint at all.
func hueBucket(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % 8)
}
