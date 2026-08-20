package agent

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// agentOnlineWindow is a DISPLAY heuristic only: an agent that polled within
// this window shows a green "online" dot. It gates nothing operational.
const agentOnlineWindow = 5 * time.Minute

var fleetCardTmpl = template.Must(template.New("agent-fleet-card").Parse(`
<div class="card mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
        <span>Agent Fleet</span>
        <a href="/account-settings" style="font-size:0.78rem;">Manage</a>
    </div>
    <div class="card-body">
        {{range .Agents}}
        <div class="d-flex align-items-center justify-content-between mb-2">
            <div style="font-size:0.88rem;min-width:0;">
                {{if .Online}}<span style="color:var(--green);" title="online">&#9679;</span>{{else}}<span style="color:var(--text-muted);" title="offline">&#9679;</span>{{end}}
                <strong>{{.Name}}</strong>
                {{if .Task}}
                    <span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.4rem;">working request #{{.Task.RequestID}}{{if .Task.Progress}} &mdash; {{.Task.Progress}}{{end}}</span>
                {{else}}
                    <span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.4rem;">idle</span>
                {{end}}
            </div>
            <div style="color:var(--text-muted);font-size:0.75rem;white-space:nowrap;">
                {{if .LastSeen}}seen {{.LastSeenAgo}}{{else}}never seen{{end}}
            </div>
        </div>
        {{end}}
    </div>
</div>`))

type fleetAgent struct {
	Name     string
	Online   bool
	LastSeen *time.Time
	Task     *Task
}

// LastSeenAgo renders the compact relative age used in the card.
func (a fleetAgent) LastSeenAgo() string {
	if a.LastSeen == nil {
		return ""
	}
	return shortDuration(time.Since(*a.LastSeen))
}

// renderCard is the SlotUserWidget Render for the profile fleet card.
//
// Renders NOTHING unless the viewer is the profile's owner (an agent roster —
// names, activity, last-seen — is not public) and the owner actually has
// agents. loon's Public/MinRole visibility cannot express "only the subject",
// so the check is the view's own, exactly as the discord link card does.
func (p *Plugin) renderCard(c *gin.Context) (template.HTML, error) {
	subject, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	viewerID, signedIn := deps.Viewer(c)
	if !signedIn || int64(viewerID) != subject {
		return "", nil
	}
	// …and not on the PUBLIC profile, even for the owner. The host renders
	// SlotUserWidget on two pages -- the account-settings profile and
	// /u/<username> -- and a fleet roster on the page whose whole purpose is
	// "what other members see" reads as a leak to the person looking at it,
	// even though the check above means nobody else can. The card belongs
	// where the member manages the fleet, which is the settings page.
	if core.IsPublicProfile(c) {
		return "", nil
	}
	ctx := c.Request.Context()
	agentList, _ := deps.AgentsForUser(ctx, int(subject))
	if len(agentList) == 0 {
		return "", nil
	}
	agents := make([]fleetAgent, 0, len(agentList))
	for _, a := range agentList {
		// Best-effort: a lock lookup that errors just renders the agent as idle.
		task, _ := deps.ActiveTask(ctx, a.ID)
		agents = append(agents, fleetAgent{
			Name:     a.Name,
			Online:   a.LastSeen != nil && time.Since(*a.LastSeen) < agentOnlineWindow,
			LastSeen: a.LastSeen,
			Task:     task,
		})
	}
	var sb strings.Builder
	if err := fleetCardTmpl.Execute(&sb, map[string]any{"Agents": agents}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// shortDuration renders a compact "just now"/"2m ago"/"3h ago"/"5d ago" string.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
