package stats

import (
	"bytes"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Two presentation views over the latest snapshot: a full "Site stats" page
// and a compact home-page widget. Both default to "any logged-in account"
// (Public false, zero MinRole) — a host wanting rank-gating registers a fork
// or we grow a config knob when someone needs it.

var pageTmpl = template.Must(template.New("page").Parse(`
{{if .Stats}}
<div class="panelV2">
    <div class="data-table-wrapper">
        <table class="data-table">
            <caption class="visually-hidden">Site statistics, most recent snapshot</caption>
            <thead><tr><th>Metric</th><th class="text-end">Value</th></tr></thead>
            <tbody>
            {{range .Stats}}
                <tr><td>{{.Label}}</td><td class="text-end">{{.Value}}</td></tr>
            {{end}}
            </tbody>
        </table>
    </div>
    <p class="text-muted small mt-3">Snapshot from {{.At}} — refreshed hourly by the Stats Cache job.</p>
</div>
{{else}}
<div class="notice notice--info">No snapshot yet — the Stats Cache job runs about a minute after boot (or trigger it from the Jobs page).</div>
{{end}}
`))

var widgetTmpl = template.Must(template.New("widget").Parse(`
{{if .Stats}}
<ul style="list-style:none;padding:0;margin:0">
    {{range .Stats}}
    <li style="display:flex;justify-content:space-between;padding:.15rem 0">
        <span class="text-muted small">{{.Label}}</span><strong>{{.Value}}</strong>
    </li>
    {{end}}
</ul>
<a class="small" href="/p/stats">all stats →</a>
{{else}}
<span class="text-muted small">No snapshot yet.</span>
{{end}}
`))

func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug: "stats", Title: "Site stats", Slot: core.SlotSitePage,
		// Public:false + zero MinRole = any logged-in user may view.
		Nav: core.NavHint{Group: "Community", Weight: 20},
		Render: func(_ *gin.Context) (template.HTML, error) {
			return p.renderPage()
		},
	}); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "stats", Title: "Site stats", Slot: core.SlotSiteWidget,
		Render: func(_ *gin.Context) (template.HTML, error) {
			return p.renderWidget(5)
		},
	})
}

func (p *Plugin) renderPage() (template.HTML, error) {
	stats, at := p.snapshot()
	data := map[string]any{"Stats": stats, "At": at.Format("2006-01-02 15:04:05")}
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) renderWidget(max int) (template.HTML, error) {
	stats, _ := p.snapshot()
	if len(stats) > max {
		stats = stats[:max]
	}
	var buf bytes.Buffer
	if err := widgetTmpl.Execute(&buf, map[string]any{"Stats": stats}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}
