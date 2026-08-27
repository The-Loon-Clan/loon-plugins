package agent

import (
	"html/template"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The Agent Dispatch section of /admin/settings, owned by the agent plugin.
//
// Read-only for now: a fleet-at-a-glance overview + jump links to the existing
// agent admin pages. The concurrency cap + dispatch defaults are still edited
// in the host's "Agent Defaults" section on the same page; consolidating those
// into an editable form here is deferred to the handler-move phase (it means
// moving host settings code + widening the settings port with setters).
var dispatchPanelTmpl = template.Must(template.New("agent-dispatch-panel").Parse(`
                <div style="font-size:0.8rem;color:var(--text-muted);margin-bottom:0.75rem;">
                    Live status of the upload fleet and where to manage it.
                </div>
                <div class="row g-3 mb-3">
                    <div class="col-sm-4">
                        <div class="home-card" style="padding:0.7rem 0.9rem;">
                            <div style="font-size:1.3rem;font-weight:700;line-height:1;"><span style="color:var(--green);">{{.Online}}</span> / {{.Total}}</div>
                            <div style="font-size:0.72rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:0.04em;margin-top:0.25rem;">Agents online</div>
                        </div>
                    </div>
                    <div class="col-sm-4">
                        <div class="home-card" style="padding:0.7rem 0.9rem;">
                            <div style="font-size:1.3rem;font-weight:700;line-height:1;">{{.MaxConcurrent}}</div>
                            <div style="font-size:0.72rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:0.04em;margin-top:0.25rem;">Max concurrent / agent</div>
                        </div>
                    </div>
                </div>
                <div style="display:flex;gap:0.5rem;flex-wrap:wrap;">
                    {{/* The PLUGIN's own pages first: it mounts them, so it
                         knows they answer. Host pages come from the host --
                         hardcoding a route name here made /admin/dispatch a
                         404 on a host that draws its queue as a panel. */}}
                    {{if .HasRoster}}<a href="/admin/p/agents" class="btn btn-outline-secondary btn-sm">Agents</a>{{end}}
                    {{if .HasGroups}}<a href="/admin/p/agent-groups" class="btn btn-outline-secondary btn-sm">Agent Groups</a>{{end}}
                    {{range .HostLinks}}<a href="{{.Href}}" class="btn btn-outline-secondary btn-sm">{{.Label}}</a>{{end}}
                </div>
                <div style="font-size:0.72rem;color:var(--text-muted);margin-top:0.6rem;">
                    The concurrency cap + dispatch defaults are set in <strong>Agent Defaults</strong> on this page; this panel is a read-only overview.
                </div>
`))

// hostAdminLinks returns the host's own agent admin pages, or nothing.
func hostAdminLinks() []AdminLink {
	if deps == nil || deps.AdminLinks == nil {
		return nil
	}
	out := make([]AdminLink, 0, 4)
	for _, l := range deps.AdminLinks() {
		// A blank half is a wiring slip, and half a button is worse than
		// none -- an unlabelled link is unclickable and a labelled one with
		// no href navigates to the current page.
		if strings.TrimSpace(l.Label) != "" && strings.TrimSpace(l.Href) != "" {
			out = append(out, l)
		}
	}
	return out
}

// renderDispatchPanel is the SlotAdminSettings Render for the agent dispatch
// overview. The host gates the page on admin, so anyone reaching here is
// already authorised.
func (p *Plugin) renderDispatchPanel(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	online, total, _ := deps.CountAgents(ctx, time.Now().Add(-onlineWindow()))
	var sb strings.Builder
	if err := dispatchPanelTmpl.Execute(&sb, map[string]any{
		"Online":        online,
		"Total":         total,
		"MaxConcurrent": deps.MaxConcurrent(ctx),
		// Same conditions the two register functions use, so the panel never
		// links to a page this host did not mount.
		"HasRoster": deps.AllAgents != nil,
		"HasGroups": deps.ListAgentGroups != nil,
		// The host's own pages, named by the host. Nil is fine: the plugin's
		// two are always reachable because the plugin mounts them.
		"HostLinks": hostAdminLinks(),
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}
