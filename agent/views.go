package agent

import (
	"context"
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
        <span>My Agents</span>
        {{if .Owner}}<a href="/p/agents" style="font-size:0.78rem;">Manage</a>{{end}}
    </div>
    <div class="card-body">
        {{range .Agents}}
        <div class="d-flex align-items-center justify-content-between mb-2">
            <div style="font-size:0.88rem;min-width:0;">
                {{if .Online}}<span style="color:var(--green);" title="online">&#9679;</span>{{else}}<span style="color:var(--text-muted);" title="offline">&#9679;</span>{{end}}
                <strong>{{.Name}}</strong>
                {{if .Task}}
                    <span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.4rem;">working request #{{.Task.RequestID}}{{if .Task.Progress}} &mdash; {{.Task.Progress}}{{end}}</span>
                {{else if .Owner}}
                    <span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.4rem;">idle</span>
                {{end}}
            </div>
            <div style="color:var(--text-muted);font-size:0.75rem;white-space:nowrap;">
                {{if .Owner}}{{if .LastSeen}}seen {{.LastSeenAgo}}{{else}}never seen{{end}}{{end}}
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
	// Owner marks the OWNER's rendering. The public opt-in variant redacts
	// to name + online dot: tasks say what someone is downloading, and
	// last-seen says when their machine is on — neither is what a member
	// consented to by ticking "show my agents".
	Owner bool
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
//
// The one exception is the member's own OPT-IN (ShowOnProfile, default
// hidden): with it set, the PUBLIC profile renders a redacted variant —
// names and online dots, no tasks, no last-seen — for every viewer,
// including the owner, so the owner sees exactly what they published.
func (p *Plugin) renderCard(c *gin.Context) (template.HTML, error) {
	subject, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	ctx := c.Request.Context()
	viewerID, signedIn := deps.Viewer(c)
	isOwner := signedIn && int64(viewerID) == subject

	// The PUBLIC profile — /u/<username>, the page whose whole purpose is
	// "what other members see". Hidden by default even for the owner (a
	// roster here reads as a leak to the person looking at it); shown, in
	// the redacted variant, only when the member opted in on /p/agents.
	if core.IsPublicProfile(c) {
		if deps.ShowOnProfile == nil {
			return "", nil
		}
		show, err := deps.ShowOnProfile(ctx, int(subject))
		if err != nil || !show {
			return "", nil
		}
		return p.renderFleet(ctx, int(subject), false)
	}

	// Everywhere else (the account-settings profile): owner-only, full card.
	if !isOwner {
		return "", nil
	}
	return p.renderFleet(ctx, int(subject), true)
}

// renderFleet draws the card for one member, at owner or public redaction.
func (p *Plugin) renderFleet(ctx context.Context, userID int, owner bool) (template.HTML, error) {
	agentList, _ := deps.AgentsForUser(ctx, userID)
	if len(agentList) == 0 {
		return "", nil
	}
	agents := make([]fleetAgent, 0, len(agentList))
	for _, a := range agentList {
		fa := fleetAgent{
			Name:   a.Name,
			Online: a.LastSeen != nil && time.Since(*a.LastSeen) < agentOnlineWindow,
			Owner:  owner,
		}
		if owner {
			fa.LastSeen = a.LastSeen
			// Best-effort: a lock lookup that errors renders the agent idle.
			task, _ := deps.ActiveTask(ctx, a.ID)
			fa.Task = task
		}
		agents = append(agents, fa)
	}
	var sb strings.Builder
	if err := fleetCardTmpl.Execute(&sb, map[string]any{"Agents": agents, "Owner": owner}); err != nil {
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
