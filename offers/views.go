package offers

import (
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"embed"
	"fmt"
	"html/template"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The three offer pages, owned by the plugin that owns offers.
//
// The HOST used to carry all of this markup, which meant the plugin could not
// change its own pages without editing the site. Embedded here, so a missing
// template is a build error rather than a 500 at runtime in the host.

//go:embed templates/*.html
var pageFS embed.FS

// The helpers these templates call. Reimplemented rather than crossed the seam
// because all three are pure and trivial — a func-typed dependency for integer
// addition would be ceremony, and unlike ComputeOfferHash there is nothing here
// that could drift into a different ANSWER.
//
// `slice` is not here: it has been a template builtin since Go 1.13.
var pageTmpl = template.Must(template.New("offers").Funcs(template.FuncMap{
		// The site's username chip, drawn by the HOST: role colour, any
		// equipped name effect, the profile link. Asking rather than drawing
		// our own anchor is what keeps a member's cosmetics from stopping at
		// this plugin's pages.
		"userTag": func(name string) template.HTML {
			return pluginapi.RenderUserTag(fxCore, name)
		},
	"add": func(a, b int) int { return a + b },
	// Byte formatting, same reasoning as the others: pure, trivial, and it
	// cannot drift into a different ANSWER the way a shared hash could. The
	// site has its own formatSize in its FuncMap; a plugin fragment is
	// rendered by THIS template set, so it needs its own.
	"formatSize": formatSize,
	"initial":    initial,
	"deref": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"dict": func(values ...any) map[string]any {
		if len(values)%2 != 0 {
			return map[string]any{}
		}
		m := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			k, ok := values[i].(string)
			if !ok {
				return map[string]any{}
			}
			m[k] = values[i+1]
		}
		return m
	},
}).ParseFS(pageFS, "templates/*.html"))

// page renders one fragment and hands it to the host for chrome.
//
// CSRFToken is injected here rather than left to each caller: it is on the
// data every one of these pages needs, it comes from host middleware, and a
// page that forgot it would render a form that silently fails to submit.
func page(c *gin.Context, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CSRFToken"] = deps.CSRFToken(c)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// A render failure must not go out as a 200 carrying half a page.
		deps.LogError(c.Request.Context(), "offers/render-"+name, err)
		deps.RenderError(c, 500, "this page failed to render")
		return
	}
	deps.RenderPage(c, title, template.HTML(sb.String()))
}

// initial is the no-avatar circle's letter.
//
// NOT `slice .Username 0 1`, which is what this markup used to say: slice
// counts BYTES, so a name whose first character is multi-byte puts half a rune
// into the page, and an empty one does not degrade but errors — html/template
// streams, so the render aborts wherever it had got to and the reader gets a
// page that stops mid-list.
//
// Reimplemented rather than taken from Deps for the same reason `add` is: it
// is pure logic with no answer to drift toward. Markdown crosses the seam
// because it sanitises; this does not.
//
// Case is left alone — the caller's CSS does text-transform: uppercase.
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

// formatSize renders a byte count the way the site's listings do: one decimal
// place, binary units, and no trailing ".0" on whole numbers.
func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v >= 100 || v == float64(int64(v)) {
		return fmt.Sprintf("%.0f %cB", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGTPE"[exp])
}
