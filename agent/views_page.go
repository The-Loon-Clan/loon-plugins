package agent

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The member-facing agents page: /p/agents, a loon SlotSitePage.
//
// The profile card is a summary with no destination — this page is where its
// Manage link lands: the member's OWN agents with their live detail, the
// self-service controls (create / rotate token / delete), and the one
// publishing choice (show the redacted card on the public profile, default
// hidden). Owner-only throughout: every Deps verb it calls is owner-scoped
// by signature, so the host enforces ownership below the plugin.
//
// Each of the page's three feature blocks stands on an OPTIONAL dep and
// degrades to absence when the host has not built the seam — the roster
// falls back to the card's own reads, self-service hides without
// CreateAgentFor, and the visibility choice hides without the opt-in pair.
// A host wires what it has; the page never breaks for what it lacks.

var agentsPageTmpl = template.Must(template.New("agents-page").Parse(`
<div class="page-narrow">
  {{/* No h1: the host's site-page wrapper renders <h1>{{.Title}}</h1>
       above every fragment — a second one here read the page twice to a
       screen reader (demo a11y audit, 2026-08-25). */}}
  <p class="ag-intro">Agents run on your own
  machine and fulfil requests you accept. Each one authenticates with its own
  token; the token is shown once, when it is created or rotated.</p>

  {{if .Msg}}<div class="alert alert-success py-2">{{.MsgText}}</div>{{end}}
  {{if .Err}}<div class="alert alert-danger py-2">{{.ErrText}}</div>{{end}}

  {{if not .Agents}}
  <div class="card mb-4"><div class="card-body ag-empty">
    No agents yet.{{if .CanManage}} Create one below to get its token.{{end}}
  </div></div>
  {{end}}

  {{range .Agents}}
  <div class="card mb-3">
    <div class="card-header d-flex justify-content-between align-items-center">
      <span>
        {{if .Online}}<span class="ag-dot--on" title="online">&#9679;</span>{{else}}<span class="ag-dot--off" title="offline">&#9679;</span>{{end}}
        <strong>{{.Name}}</strong>
        <span class="ag-seen">
          {{if .LastSeen}}seen {{.LastSeenAgo}}{{else}}never seen{{end}}
        </span>
      </span>
      {{if $.CanManage}}
      <span class="d-flex gap-1">
        <form method="POST" action="/p/agents/rotate" class="d-inline"
              onsubmit="return confirm('Rotate this agent’s token? The old token stops working immediately.')">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <input type="hidden" name="agent_id" value="{{.ID}}">
          <button type="submit" class="button button--outlined button--micro button--warning">Rotate token</button>
        </form>
        <form method="POST" action="/p/agents/delete" class="d-inline"
              onsubmit="return confirm('Delete this agent? It will no longer be able to connect.')">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <input type="hidden" name="agent_id" value="{{.ID}}">
          <button type="submit" class="button button--outlined button--micro button--danger">Delete</button>
        </form>
      </span>
      {{end}}
    </div>
    {{if .Status}}
    <div class="card-body ag-status">
      <div class="ag-status__meta">
        <span>phase <strong>{{.Status.Phase}}</strong></span>
        {{if .Status.VPNStatus}}<span>VPN {{.Status.VPNStatus}}</span>{{end}}
        {{if .Status.PublicIP}}<span>IP {{.Status.PublicIP}}</span>{{end}}
        {{if .Status.DownloadSpeed}}<span>&#8595; {{.Status.DownloadSpeed}}</span>{{end}}
        {{if .Status.UploadSpeed}}<span>&#8593; {{.Status.UploadSpeed}}</span>{{end}}
        {{if .Status.DiskFreeGB}}<span>{{printf "%.0f" .Status.DiskFreeGB}} GB free</span>{{end}}
      </div>
      {{if .Status.TaskTitle}}
      <div class="ag-status__task">working: <strong>{{.Status.TaskTitle}}</strong>{{if .Status.RequestID}} <span class="ag-status__req">(request #{{.Status.RequestID}})</span>{{end}}</div>
      {{end}}
      {{range .Status.Files}}
      <div class="ag-file">
        <span class="ag-file__name">{{.Name}}</span>
        <span>{{.Phase}}</span>
        {{if .Speed}}<span>{{.Speed}}</span>{{end}}
        <span class="ag-file__pct">{{printf "%.0f" .Percent}}%</span>
      </div>
      {{end}}
    </div>
    {{else if .Task}}
    <div class="card-body ag-task">
      working request #{{.Task.RequestID}}{{if .Task.Progress}} &mdash; {{.Task.Progress}}{{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .CanManage}}
  <div class="card mb-4">
    <div class="card-header">New agent</div>
    <div class="card-body">
      <form method="POST" action="/p/agents/create" class="d-flex gap-2 align-items-center">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <label class="visually-hidden" for="ag-new-name">Agent name</label>
        <input type="text" name="name" id="ag-new-name" class="form__input ag-name-input" maxlength="60" required
               placeholder="agent name (e.g. home-server)">
        <button type="submit" class="button button--primary button--sm">Create &amp; get token</button>
      </form>
    </div>
  </div>
  {{end}}

  {{if .HasVisibility}}
  <div class="card mb-4">
    <div class="card-header">Visibility</div>
    <div class="card-body">
      <form method="POST" action="/p/agents/visibility">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <ul class="option-list">
          <li>
            <label><input type="checkbox" name="show" value="1" {{if .ShowOnProfile}}checked{{end}}> Show my agents on my public profile</label>
            <span class="option-list__note">Off by default. When on, other members see your agents&rsquo; names and online dots on /u/&lt;you&gt; &mdash; never their tasks, addresses, or activity times.</span>
          </li>
        </ul>
        <button type="submit" class="button button--outlined button--sm ag-save">Save</button>
      </form>
    </div>
  </div>
  {{end}}
</div>`))

// tokenPageTmpl is the one-time token reveal, returned by create/rotate as
// the action's own page — a token cannot survive a redirect, and the host
// stores only the hash, so this render is the only time it exists in HTML.
var tokenPageTmpl = template.Must(template.New("agent-token").Parse(`
<div class="page-narrow">
  {{/* No h1 — the host wrapper carries the page title (see the page
       template above). The card header names the agent. */}}
  <div class="card mb-3">
    <div class="card-header">Token for {{.Name}}</div>
    <div class="card-body">
      <p class="ag-intro">Copy this token into
      your agent's configuration now. It is shown ONCE — the site keeps only a
      hash, and losing it means rotating for a new one.</p>
      <code class="ag-token">{{.Token}}</code>
    </div>
  </div>
  <a href="/p/agents" class="button button--outlined button--sm">Back to My Agents</a>
</div>`))

type agentsPageVM struct {
	Agents        []pageAgent
	CanManage     bool
	HasVisibility bool
	ShowOnProfile bool
	CSRFToken     string
	Msg, Err      string
}

// MsgText / ErrText map the redirect CODES to sentences — the words live
// here, the URLs carry codes, per the achievements-page convention.
func (vm agentsPageVM) MsgText() string {
	switch vm.Msg {
	case "deleted":
		return "Agent deleted."
	case "saved":
		return "Visibility saved."
	}
	return ""
}

func (vm agentsPageVM) ErrText() string {
	switch vm.Err {
	case "name":
		return "Give the agent a name."
	case "notfound":
		return "That agent no longer exists."
	case "taken":
		return "You already have an agent with that name."
	default:
		if vm.Err != "" {
			return "Something went wrong — the change was not saved."
		}
	}
	return ""
}

type pageAgent struct {
	fleetAgent
	ID     int
	Status *AgentStatus
}

// registerMemberPage mounts /p/agents. Not Public: the page is a member's
// own machines and tokens, so an anonymous visitor gets the host's login
// gate rather than an empty shell. NavHidden: the card's Manage link and the
// host's account bar are its doors — a personal settings page in the site
// nav would read as a public feature.
func (p *Plugin) registerMemberPage(c *core.Core) error {
	return c.RegisterView(core.View{
		Slug: "agents", Title: "My Agents", Slot: core.SlotSitePage,
		Description: "Your agents: live status, tokens, and visibility.",
		Nav:         core.NavHint{Menu: core.NavHidden},
		Render:      p.renderMemberPage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"create":     p.actionCreateAgent,
			"rotate":     p.actionRotateToken,
			"delete":     p.actionDeleteAgent,
			"visibility": p.actionSetVisibility,
		},
	})
}

func (p *Plugin) renderMemberPage(gc *gin.Context) (template.HTML, error) {
	userID, ok := deps.Viewer(gc)
	if !ok {
		// Belt-and-braces: the view is not Public, so the host's gate runs
		// first, but a host misconfiguration must not render someone else's
		// nothing.
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	// The operator's answer to "who may run an agent". Checked HERE as well
	// as on every action below, because a page that renders a create form to
	// somebody who cannot use it is a worse refusal than no page.
	if !allowed(gc.Request.Context(), userID) {
		return notEntitledHTML(), nil
	}
	ctx := gc.Request.Context()
	vm := agentsPageVM{
		CanManage:     deps.CreateAgentFor != nil && deps.RotateTokenFor != nil && deps.DeleteAgentFor != nil,
		HasVisibility: deps.ShowOnProfile != nil && deps.SetShowOnProfile != nil,
		CSRFToken:     pluginapi.CSRFToken(p.core, gc),
		Msg:           gc.Query("msg"),
		Err:           pageErrCode(gc),
	}

	if deps.AgentsDetail != nil {
		details, err := deps.AgentsDetail(ctx, userID)
		if err != nil {
			return "", err
		}
		for _, d := range details {
			vm.Agents = append(vm.Agents, pageAgent{
				fleetAgent: fleetAgent{
					Name:     d.Name,
					Online:   onlineNow(d.LastSeen),
					LastSeen: d.LastSeen,
					Owner:    true,
				},
				ID:     d.ID,
				Status: d.Status,
			})
		}
	} else {
		// The fallback a detail-less host gets: the card's own reads.
		agentList, err := deps.AgentsForUser(ctx, userID)
		if err != nil {
			return "", err
		}
		for _, a := range agentList {
			task, _ := deps.ActiveTask(ctx, a.ID)
			vm.Agents = append(vm.Agents, pageAgent{
				fleetAgent: fleetAgent{
					Name:     a.Name,
					Online:   onlineNow(a.LastSeen),
					LastSeen: a.LastSeen,
					Task:     task,
					Owner:    true,
				},
				ID: a.ID,
			})
		}
	}

	if vm.HasVisibility {
		show, err := deps.ShowOnProfile(ctx, userID)
		if err != nil {
			return "", err
		}
		vm.ShowOnProfile = show
	}

	var sb strings.Builder
	if err := agentsPageTmpl.Execute(&sb, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// pageErrCode reads both refusal markers: this plugin's own ?err=<code> and
// the host wrapper's bare ?error=1 after a returned error (see the
// achievements page for the war story — a privacy choice that failed
// silently reads as success).
func pageErrCode(gc *gin.Context) string {
	if code := gc.Query("err"); code != "" {
		return code
	}
	if gc.Query("error") != "" {
		return "savefailed"
	}
	return ""
}

func (p *Plugin) actionCreateAgent(gc *gin.Context) (template.HTML, error) {
	userID, ok := deps.Viewer(gc)
	if !ok {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	// Re-checked per action: a form outlives the page that drew it, and an
	// entitlement can be revoked between the two.
	if !allowed(gc.Request.Context(), userID) {
		gc.Redirect(http.StatusSeeOther, "/p/agents")
		return "", nil
	}
	if deps.CreateAgentFor == nil {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=unavailable")
		return "", nil
	}
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" || len(name) > 60 {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=name")
		return "", nil
	}
	token, err := deps.CreateAgentFor(gc.Request.Context(), userID, name)
	if errors.Is(err, ErrNameTaken) {
		// The one refusal a member can fix themselves gets its own words;
		// hosts signal it with the sentinel, everything else stays the
		// generic banner via the returned error.
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=taken")
		return "", nil
	}
	if err != nil {
		// Returned, not swallowed: the host logs it and redirects with its
		// error marker.
		return "", err
	}
	return renderTokenPage(name, token)
}

func (p *Plugin) actionRotateToken(gc *gin.Context) (template.HTML, error) {
	userID, ok := deps.Viewer(gc)
	if !ok {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	// Re-checked per action: a form outlives the page that drew it, and an
	// entitlement can be revoked between the two.
	if !allowed(gc.Request.Context(), userID) {
		gc.Redirect(http.StatusSeeOther, "/p/agents")
		return "", nil
	}
	if deps.RotateTokenFor == nil {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=unavailable")
		return "", nil
	}
	agentID, _ := strconv.Atoi(gc.PostForm("agent_id"))
	if agentID <= 0 {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=notfound")
		return "", nil
	}
	token, err := deps.RotateTokenFor(gc.Request.Context(), userID, agentID)
	if errors.Is(err, ErrNotFound) {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=notfound")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return renderTokenPage(agentName(gc, userID, agentID), token)
}

func (p *Plugin) actionDeleteAgent(gc *gin.Context) (template.HTML, error) {
	userID, ok := deps.Viewer(gc)
	if !ok {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	// Re-checked per action: a form outlives the page that drew it, and an
	// entitlement can be revoked between the two.
	if !allowed(gc.Request.Context(), userID) {
		gc.Redirect(http.StatusSeeOther, "/p/agents")
		return "", nil
	}
	if deps.DeleteAgentFor == nil {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=unavailable")
		return "", nil
	}
	agentID, _ := strconv.Atoi(gc.PostForm("agent_id"))
	if agentID <= 0 {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=notfound")
		return "", nil
	}
	if err := deps.DeleteAgentFor(gc.Request.Context(), userID, agentID); errors.Is(err, ErrNotFound) {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=notfound")
		return "", nil
	} else if err != nil {
		return "", err
	}
	gc.Redirect(http.StatusSeeOther, "/p/agents?msg=deleted")
	return "", nil
}

func (p *Plugin) actionSetVisibility(gc *gin.Context) (template.HTML, error) {
	userID, ok := deps.Viewer(gc)
	if !ok {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	// Re-checked per action: a form outlives the page that drew it, and an
	// entitlement can be revoked between the two.
	if !allowed(gc.Request.Context(), userID) {
		gc.Redirect(http.StatusSeeOther, "/p/agents")
		return "", nil
	}
	if deps.SetShowOnProfile == nil {
		gc.Redirect(http.StatusSeeOther, "/p/agents?err=unavailable")
		return "", nil
	}
	// An absent checkbox means HIDDEN — the default, and what an unticked
	// box posts. The safe direction for a publishing choice.
	show := gc.PostForm("show") == "1"
	if err := deps.SetShowOnProfile(gc.Request.Context(), userID, show); err != nil {
		return "", err
	}
	gc.Redirect(http.StatusSeeOther, "/p/agents?msg=saved")
	return "", nil
}

func renderTokenPage(name, token string) (template.HTML, error) {
	var sb strings.Builder
	if err := tokenPageTmpl.Execute(&sb, map[string]string{"Name": name, "Token": token}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// onlineNow is the card's display heuristic, shared by the page.
func onlineNow(t *time.Time) bool {
	return t != nil && time.Since(*t) < onlineWindow()
}

// agentName resolves a display name for the token page, best-effort — the
// token reveal must not fail because a name lookup did.
func agentName(gc *gin.Context, userID, agentID int) string {
	if agents, err := deps.AgentsForUser(gc.Request.Context(), userID); err == nil {
		for _, a := range agents {
			if a.ID == agentID {
				return a.Name
			}
		}
	}
	return "agent"
}

// notEntitledHTML is what a member sees when the host says they may not use
// agents. A plain sentence rather than a 403: they are signed in and looking
// at a page the navigation offered them, and the honest answer is that this
// site does not give them this feature -- not that something went wrong.
func notEntitledHTML() template.HTML {
	return template.HTML(`<div class="page-narrow"><div class="card"><div class="card-body ag-empty">` +
		`Agents are not enabled for your account on this site.` +
		`</div></div></div>`)
}
