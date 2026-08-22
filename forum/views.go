package forum

import (
	"embed"
	"html/template"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The forum's five pages, owned by the plugin that owns the forum.
//
// The HOST used to carry all 1,300 lines of this, which is why the forum still
// read as present in prod after the Go moved out: the plugin could not change
// its own pages, and the site could not delete them.
//
// Embedded, so a missing template is a build error here rather than a 500 at
// runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

var pageTmpl *template.Template

// parseTemplates binds the one seam the markup calls and parses the fragments.
// Deferred to Provision because RelativeTime is a Deps function and Deps does
// not exist until then.
func parseTemplates() {
	if deps.RelativeTime == nil {
		return
	}
	pageTmpl = template.Must(template.New("forum").Funcs(template.FuncMap{
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },

		// The cosmetics a member is WEARING. Not a Deps seam: cosmetics is a
		// pluginapi contract with its own cache, and pluginapi is already
		// imported here — asking the host to pass a copy would be a second
		// answer to a question the contract already answers.
		//
		// The forum draws its own author column, so it never picked these up:
		// the host applies them in its user-tag and avatar templates, which a
		// plugin fragment does not use. A member with an equipped name effect
		// saw it everywhere on the site except the place they post.
		//
		// Both nil-degrade to "": no cosmetics plugin, no classes, plain names.
		"nameFX":   func(name string) string { return pluginapi.NameClass(fxCore, name) },
		"avatarFX": func(name string) string { return pluginapi.SlotClass(fxCore, pluginapi.SlotAvatar, name) },
		"profileFX": func(name string) string {
			return pluginapi.SlotClass(fxCore, pluginapi.SlotProfile, name)
		},

		// The rest are pure and are reimplemented rather than crossed. The
		// rule this follows: a helper crosses the seam when the two copies
		// could disagree SILENTLY and the wrong answer is dangerous —
		// Markdown, because it sanitises. `add` cannot drift toward a wrong
		// answer, and `derefStr` fails loudly if it does.
		// repBadge is the exception among these: it is not pure, it is site
		// VOCABULARY. What the earned tiers are called, and what colour they
		// wear, is the operator's decision — hardcoding one site's ladder
		// into a public plugin would ship ameNZB's words to every adopter.
		// Optional, and nil-degrades to no badge; the markup already guards
		// on an empty name.
		"repBadge": func(tier int) RepBadgeInfo {
			if deps.RepBadge == nil {
				return RepBadgeInfo{}
			}
			return deps.RepBadge(tier)
		},

		// The counted nouns. Every one of these was hand-written as a bare
		// plural, so a board with one thread said "1 threads" -- and a seeded
		// install is nearly all ones. Returns the NOUN alone: the markup
		// already sets the number in its own <strong>.
		"plural": func(n int, one string, many ...string) string {
			if n == 1 {
				return one
			}
			if len(many) > 0 {
				return many[0]
			}
			return one + "s"
		},

		"add":      func(a, b int) int { return a + b },
		"derefStr": derefStr,
		"deref64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"initial": initial,
	}).ParseFS(pageFS, "templates/*.html"))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// initial is the no-avatar circle's letter.
//
// NOT `slice .Username 0 1`, which is what this markup said in four places
// until the lift: slice counts BYTES, so a name whose first character is
// multi-byte puts half a rune into the page as invalid UTF-8, and an empty one
// does not degrade but errors — html/template streams, so the render aborts
// wherever it had reached and the reader gets a thread that stops mid-page.
//
// Case is left alone: the caller's CSS does text-transform: uppercase.
func initial(s string) string {
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 {
		return ""
	}
	return s[:size]
}

// render executes one fragment and hands it to the host for chrome.
//
// The three viewer keys are injected here rather than at each call site, and
// that is the whole reason this is a method. The markup gates moderation
// controls on `.IsAdmin` and post ownership on `.CurrentUserID`; data is a
// map, so a call site that forgot either would render a page with the pin,
// lock and delete controls quietly missing, with nothing logged. They used to
// arrive from the host's BaseData, where forgetting was not possible; a
// per-page copy would have made it possible again.
// fail renders a refusal in the site's chrome instead of a bare string.
//
// Six handlers used to answer c.String(500, "failed to load posts") and the
// like — a user-visible sentence living in Go where the translation seam could
// never reach it, served as plain text on a blank page. One of them,
// AdminCategories, printed the raw Go error with %v.
//
// The reason travels as a code and forum_error.html holds the words.
func (h *Handlers) fail(c *gin.Context, status int, reason string) {
	h.render(c, status, "Forums", "forum_error.html", gin.H{"Reason": reason})
}

func (h *Handlers) render(c *gin.Context, status int, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	uid, isAdmin := h.currentUser(c)
	data["CurrentUserID"] = uid
	data["IsAdmin"] = isAdmin

	data["CSRFToken"] = deps.CSRFToken(c)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// RepBadgeInfo is one earned reputation tier as the markup renders it: a name
// and a Bootstrap colour suffix. Empty Name means "no badge".
type RepBadgeInfo struct {
	Name  string
	Color string
}

// The shared-widget seams, each degrading to nothing on the legacy contract —
// where the host's own copy of the template calls the partial itself, so an
// empty value here is not a missing widget but an unused key.

func editor(opts map[string]any) template.HTML {
	if deps.RenderEditor == nil {
		return ""
	}
	return deps.RenderEditor(opts)
}

func reportModal(c *gin.Context) template.HTML {
	if deps.RenderReportModal == nil {
		return ""
	}
	return deps.RenderReportModal(c)
}

func paginate(page, total int, baseURL string) template.HTML {
	if deps.RenderPagination == nil {
		return ""
	}
	return deps.RenderPagination(page, forumPageSize, total, baseURL)
}

// legacyPaginate builds the view model a host template consumes by field
