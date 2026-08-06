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
                    <a href="/admin/agents" class="btn btn-outline-secondary btn-sm">Agents</a>
                    <a href="/admin/agent-groups" class="btn btn-outline-secondary btn-sm">Agent Groups</a>
                    <a href="/admin/dispatch" class="btn btn-outline-secondary btn-sm">Dispatch Debug</a>
                </div>
                <div style="font-size:0.72rem;color:var(--text-muted);margin-top:0.6rem;">
                    The concurrency cap + dispatch defaults are set in <strong>Agent Defaults</strong> on this page; this panel is a read-only overview.
                </div>
`))

// renderDispatchPanel is the SlotAdminSettings Render for the agent dispatch
// overview. The host gates the page on admin, so anyone reaching here is
// already authorised.
func (p *Plugin) renderDispatchPanel(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	online, total, _ := deps.CountAgents(ctx, time.Now().Add(-agentOnlineWindow))
	var sb strings.Builder
	if err := dispatchPanelTmpl.Execute(&sb, map[string]any{
		"Online":        online,
		"Total":         total,
		"MaxConcurrent": deps.MaxConcurrent(ctx),
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}
