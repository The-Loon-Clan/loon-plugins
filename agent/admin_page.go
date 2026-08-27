package agent

import (
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The admin roster: /admin/p/agents, a loon SlotAdminPage.
//
// WHY THIS PAGE EXISTS SEPARATELY FROM THE HOST'S /admin/agents, which it does
// not replace wholesale. That host page renders two unrelated things stacked
// in one template: a roster of registered agents, and a live table of active
// dispatch tasks. Only the first is portable.
//
// The roster is the same question on every host that runs agents — who is
// registered, whose is it, is it live, is it revoked — and it reads the agent
// tokens the fleet authenticates with. The task table reads whatever table
// that host queues work in: request_locks here, agent_task on the demo, and
// nothing at all on a host that only uses agents for uploads. A plugin panel
// over either would be a plugin knowing a table it does not own, which is the
// same line that keeps the /api/agent runtime host-side.
//
// So the split is by OWNERSHIP, not by who wrote it first: roster and
// credentials move here, the dispatch queue stays with the host that owns its
// queue. Agreed with the loon-demo-site session 26 Aug 2026 before either
// side was written, because the alternative was discovering it at wiring time
// with two admin pages live.
//
// Read-only, deliberately. The host page it lifts has no revoke or delete
// control, and inventing admin verbs during an extraction is how a lift turns
// into a rewrite that has to re-converge later. Members manage their own
// agents at /p/agents; an operator who needs to revoke somebody else's has
// the same tools they had yesterday.

var adminRosterTmpl = template.Must(template.New("agent-admin-roster").Parse(`
<div class="page">
  {{/* No h1: the host's admin-page wrapper renders one above the fragment. */}}
  <p class="ag-intro">Every agent registered on this site. Agents are created
  and managed by their owners at <code>/p/agents</code>; this is the roster
  view.</p>

  <div class="ag-adm__summary">
    <span><strong>{{.Online}}</strong> online</span>
    <span><strong>{{.Total}}</strong> registered</span>
    {{if .Revoked}}<span><strong>{{.Revoked}}</strong> revoked</span>{{end}}
  </div>

  {{if not .Agents}}
  <div class="card"><div class="card-body ag-empty">No agents registered yet.</div></div>
  {{else}}
  <div class="card">
    <div class="table-responsive">
      <table class="table table-sm mb-0 ag-adm__table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Owner</th>
            <th>State</th>
            <th>Last seen</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {{range .Agents}}
          <tr>
            <td>
              {{if .Online}}<span class="ag-dot--on" title="online">&#9679;</span>
              {{else}}<span class="ag-dot--off" title="offline">&#9679;</span>{{end}}
              {{.Name}}
            </td>
            <td>{{.Owner}}</td>
            <td>
              {{if eq .Status "active"}}<span class="badge bg-success ag-adm__badge">Active</span>
              {{else}}<span class="badge bg-secondary ag-adm__badge">{{.StatusLabel}}</span>{{end}}
            </td>
            <td class="ag-adm__when">{{if .LastSeen}}{{.LastSeenAt}}{{else}}Never{{end}}</td>
            <td class="ag-adm__when">{{.CreatedOn}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </div>
  </div>
  {{end}}
</div>`))

// rosterRow decorates AdminAgent with the display forms the template needs.
// Formatting lives here rather than in the template because html/template has
// no way to express "titlecase this" without a FuncMap, and a FuncMap for one
// string is more machinery than the string is worth.
type rosterRow struct {
	AdminAgent
}

// StatusLabel renders an unknown state readably rather than dropping it. The
// host owns this vocabulary and may grow it (expired, suspended); a template
// that only knew "active" and "revoked" would silently show the wrong badge
// for the third one.
func (r rosterRow) StatusLabel() string {
	if r.Status == "" {
		return "Unknown"
	}
	return strings.ToUpper(r.Status[:1]) + r.Status[1:]
}

// LastSeenAt and CreatedOn use the same formats the host page used, so an
// operator reading the lifted page sees what they read yesterday.
func (r rosterRow) LastSeenAt() string {
	if r.LastSeen == nil {
		return "Never"
	}
	return r.LastSeen.Format("Jan 02, 15:04")
}

func (r rosterRow) CreatedOn() string { return r.CreatedAt.Format("Jan 02, 2006") }

// registerAdminPage mounts /admin/p/agents, and only when the host has wired
// AllAgents. Registering it unconditionally would give an operator a page
// that renders an empty roster on a host whose seam is simply missing, and an
// empty fleet and an unwired seam must not look the same.
func (p *Plugin) registerAdminPage(c *core.Core) error {
	if deps.AllAgents == nil {
		return nil
	}
	return c.RegisterView(core.View{
		Slug:        "agents",
		Title:       "Agents",
		Slot:        core.SlotAdminPage,
		MinRole:     core.RoleAdmin,
		Description: "Every agent registered on this site, with its owner and last check-in.",
		Render:      p.renderAdminRoster,
	})
}

// renderAdminRoster draws the roster. The host gates SlotAdminPage on the
// MinRole above, so anyone reaching here is already an admin — there is no
// entitlement check: CanUseAgents answers "may this MEMBER run agents", which
// is a different question from "may this operator see the fleet", and reusing
// it here would hide the roster from an admin who owns no agents.
func (p *Plugin) renderAdminRoster(gc *gin.Context) (template.HTML, error) {
	agents, err := deps.AllAgents(gc.Request.Context())
	if err != nil {
		return "", err
	}

	vm := struct {
		Agents                 []rosterRow
		Online, Total, Revoked int
	}{Total: len(agents)}

	for _, a := range agents {
		if a.Online() {
			vm.Online++
		}
		if a.Status != "active" {
			vm.Revoked++
		}
		vm.Agents = append(vm.Agents, rosterRow{AdminAgent: a})
	}

	var sb strings.Builder
	if err := adminRosterTmpl.Execute(&sb, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}
