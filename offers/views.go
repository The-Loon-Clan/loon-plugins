package offers

import (
	"embed"
	"html/template"
	"strings"

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
	"add": func(a, b int) int { return a + b },
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
