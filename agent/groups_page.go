package agent

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The agent-groups admin page: /admin/p/agent-groups, a loon SlotAdminPage.
//
// A posting profile says where a kind of upload goes and how agents should
// build it — newsgroups, sample count, PAR2 redundancy, obfuscation, the
// watermark, the banned-extension list. Agents fetch them from the host's
// /api/agent/groups and upsert them locally with source='site'.
//
// This belongs to the plugin where the roster does and the dispatch queue does
// not, by the same ownership test: a profile describes how the FLEET behaves,
// which is the agent domain on every host that has one. It reads no table
// whose shape is a host's own business — unlike request_locks, which is why
// that panel stayed home.
//
// Lifted rather than redesigned. The field set, the labels, the placeholder
// text and the create-then-list layout are the host page's, because an
// operator who has been configuring groups for months should not have to
// relearn the screen to gain a plugin.

// splitLinesOrCommas parses the two textarea fields. Newsgroup names and
// extensions cannot contain whitespace or commas, so every one of those is a
// separator — which lets an operator paste a list in whatever shape they have
// it rather than reformatting it first.
func splitLinesOrCommas(s string) []string {
	out := make([]string, 0)
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// normaliseExtensions lowercases and forces a leading dot, so ".EXE", "exe"
// and ".exe" all match the agent's runtime lookup. The agent compares against
// filepath.Ext output, which is lowercase and dotted; a list that does not
// match that shape silently bans nothing.
func normaliseExtensions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, ext := range in {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		out = append(out, ext)
	}
	return out
}

// optInt reads a nullable override. An EMPTY field means "the agent decides",
// so it must stay nil — 0 screenshots and 0% PAR2 are real instructions, and
// coercing blank to zero would quietly turn every unset field into the most
// aggressive setting available.
//
// A field that is present but unparseable is an error rather than a silent
// nil, which is the one place this deliberately differs from the host page it
// lifts: there, strconv failure fell through to "use the default", so a typo
// looked exactly like leaving the box empty.
func optInt(raw string) (*int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a number", raw)
	}
	return &n, nil
}

// groupFromForm parses the shared create/update form.
func groupFromForm(c *gin.Context) (AgentGroup, error) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		return AgentGroup{}, errors.New("name is required")
	}
	typ := strings.TrimSpace(strings.ToLower(c.PostForm("type")))
	if typ == "" {
		typ = "video"
	}
	g := AgentGroup{
		Name:             name,
		Type:             typ,
		Newsgroups:       splitLinesOrCommas(c.PostForm("newsgroups")),
		BannedExtensions: normaliseExtensions(splitLinesOrCommas(c.PostForm("banned_extensions"))),
		WatermarkText:    strings.TrimSpace(c.PostForm("watermark_text")),
	}
	if len(g.Newsgroups) == 0 {
		return AgentGroup{}, errors.New("at least one newsgroup is required")
	}

	var err error
	if g.Screenshots, err = optInt(c.PostForm("screenshots")); err != nil {
		return AgentGroup{}, fmt.Errorf("samples: %w", err)
	}
	if g.SampleSeconds, err = optInt(c.PostForm("sample_seconds")); err != nil {
		return AgentGroup{}, fmt.Errorf("sec/sample: %w", err)
	}
	if g.Par2Redundancy, err = optInt(c.PostForm("par2_redundancy")); err != nil {
		return AgentGroup{}, fmt.Errorf("PAR2 %%: %w", err)
	}
	if g.Par2Redundancy != nil && (*g.Par2Redundancy < 0 || *g.Par2Redundancy > 100) {
		return AgentGroup{}, errors.New("PAR2 % must be between 0 and 100")
	}
	// Three states, and the empty one is not false: "" inherits, "1" forces
	// obfuscation on, "0" forces it off.
	switch strings.TrimSpace(c.PostForm("obfuscate")) {
	case "1":
		t := true
		g.Obfuscate = &t
	case "0":
		f := false
		g.Obfuscate = &f
	}
	return g, nil
}

// groupRow decorates a group with the form-value shapes the template needs.
// Every one answers the same question — "what goes in the value attribute" —
// and the answer for an unset override is the EMPTY STRING, never "0", so the
// box renders blank and round-trips back to nil.
type groupRow struct {
	AgentGroup
}

func numOrBlank(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func (g groupRow) ScreenshotsVal() string   { return numOrBlank(g.Screenshots) }
func (g groupRow) SampleSecondsVal() string { return numOrBlank(g.SampleSeconds) }
func (g groupRow) Par2Val() string          { return numOrBlank(g.Par2Redundancy) }

// ObfuscateVal collapses the tri-state to the option values the select uses:
// "" inherit, "1" on, "0" off.
func (g groupRow) ObfuscateVal() string {
	if g.Obfuscate == nil {
		return ""
	}
	if *g.Obfuscate {
		return "1"
	}
	return "0"
}

// The textareas are one-per-line, which is what splitLinesOrCommas reads back.
func (g groupRow) NewsgroupsText() string { return strings.Join(g.Newsgroups, "\n") }
func (g groupRow) BannedText() string     { return strings.Join(g.BannedExtensions, "\n") }

var groupsPageTmpl = template.Must(template.New("agent-groups").Parse(`
<div class="page">
  {{/* No h1: the host's admin-page wrapper renders one above the fragment. */}}
  <p class="ag-intro">Posting profiles pushed to agents via
  <code>GET /api/agent/groups</code>. Agents upsert them with
  <code>source='site'</code>; groups an agent defines locally are preserved.</p>

  {{if .Msg}}<div class="alert alert-success py-2">{{.Msg}}</div>{{end}}
  {{if .Err}}<div class="alert alert-danger py-2">{{.Err}}</div>{{end}}

  {{if not .CanManage}}
  <div class="alert alert-secondary py-2">This site has not wired group editing, so these are read-only.</div>
  {{end}}

  <datalist id="ag-type-list">
    <option value="video"></option><option value="manga"></option><option value="music"></option>
  </datalist>

  {{if .CanManage}}
  <div class="card mb-4">
    <div class="card-header">Create group</div>
    <div class="card-body">
      <form method="POST" action="/admin/p/agent-groups/create">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="row g-3">
          <div class="col-md-3">
            <label class="form-label">Name</label>
            <input type="text" name="name" class="form-control" required placeholder="anime">
          </div>
          <div class="col-md-2">
            <label class="form-label">Type</label>
            <input type="text" name="type" class="form-control" placeholder="video" list="ag-type-list">
            <small class="ag-hint">Open label — agents skip sampling on unknown types.</small>
          </div>
          <div class="col-md-7">
            <label class="form-label">Newsgroups</label>
            <textarea name="newsgroups" class="form-control" rows="2" required
                      placeholder="alt.binaries.multimedia.anime.highspeed&#10;alt.binaries.boneless"></textarea>
            <small class="ag-hint">One per line (commas also accepted).</small>
          </div>
          <div class="col-md-2">
            <label class="form-label">Samples</label>
            <input type="number" name="screenshots" class="form-control" min="0" placeholder="agent default">
          </div>
          <div class="col-md-2">
            <label class="form-label">Sec/sample</label>
            <input type="number" name="sample_seconds" class="form-control" min="1" placeholder="agent default">
            <small class="ag-hint">audio only</small>
          </div>
          <div class="col-md-2">
            <label class="form-label">PAR2 %</label>
            <input type="number" name="par2_redundancy" class="form-control" min="0" max="100" placeholder="agent default">
          </div>
          <div class="col-md-2">
            <label class="form-label">Obfuscate</label>
            <select name="obfuscate" class="form-select">
              <option value="">inherit</option><option value="1">yes</option><option value="0">no</option>
            </select>
          </div>
          <div class="col-md-4">
            <label class="form-label">Watermark</label>
            <input type="text" name="watermark_text" class="form-control" placeholder="-YourTag">
            <small class="ag-hint">drawn on screenshots; blank = off</small>
          </div>
          <div class="col-md-12">
            <label class="form-label">Banned extensions</label>
            <textarea name="banned_extensions" class="form-control" rows="2"
                      placeholder="(blank = use agent's default blocklist)"></textarea>
            <small class="ag-hint">One per line. A non-empty list replaces the agent's default blocklist outright.</small>
          </div>
        </div>
        <div class="mt-3"><button type="submit" class="btn btn-primary">Create group</button></div>
      </form>
    </div>
  </div>
  {{end}}

  <h5 class="ag-grp__heading">Existing ({{len .Groups}})</h5>

  {{range .Groups}}
  <div class="card mb-3">
    <div class="card-header py-2 d-flex justify-content-between align-items-center">
      <div>
        <strong>{{.Name}}</strong>
        <span class="badge bg-secondary bg-opacity-50 ms-2 ag-grp__type">{{.Type}}</span>
        <span class="ag-grp__ver">v{{.Version}}</span>
      </div>
      {{if $.CanManage}}
      <form method="POST" action="/admin/p/agent-groups/delete" class="m-0"
            onsubmit="return confirm('Delete {{.Name}}? Agents keep their local copy until they next poll.')">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <input type="hidden" name="group_id" value="{{.ID}}">
        <button type="submit" class="btn btn-sm btn-outline-danger py-0 px-2 ag-grp__del">Delete</button>
      </form>
      {{end}}
    </div>
    <div class="card-body">
      <form method="POST" action="/admin/p/agent-groups/update">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <input type="hidden" name="group_id" value="{{.ID}}">
        <div class="row g-3">
          <div class="col-md-3">
            <label class="form-label ag-grp__label">Name</label>
            <input type="text" name="name" class="form-control form-control-sm" value="{{.Name}}" required {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-2">
            <label class="form-label ag-grp__label">Type</label>
            <input type="text" name="type" class="form-control form-control-sm" value="{{.Type}}" list="ag-type-list" {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-7">
            <label class="form-label ag-grp__label">Newsgroups</label>
            <textarea name="newsgroups" class="form-control form-control-sm" rows="2" required {{if not $.CanManage}}disabled{{end}}>{{.NewsgroupsText}}</textarea>
            <small class="ag-hint">One per line.</small>
          </div>
          <div class="col-md-2">
            <label class="form-label ag-grp__label">Samples</label>
            <input type="number" name="screenshots" class="form-control form-control-sm" value="{{.ScreenshotsVal}}" min="0" placeholder="agent default" {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-2">
            <label class="form-label ag-grp__label">Sec/sample</label>
            <input type="number" name="sample_seconds" class="form-control form-control-sm" value="{{.SampleSecondsVal}}" min="1" placeholder="agent default" {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-2">
            <label class="form-label ag-grp__label">PAR2 %</label>
            <input type="number" name="par2_redundancy" class="form-control form-control-sm" value="{{.Par2Val}}" min="0" max="100" placeholder="agent default" {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-2">
            <label class="form-label ag-grp__label">Obfuscate</label>
            <select name="obfuscate" class="form-select form-select-sm" {{if not $.CanManage}}disabled{{end}}>
              <option value=""{{if eq .ObfuscateVal ""}} selected{{end}}>inherit</option>
              <option value="1"{{if eq .ObfuscateVal "1"}} selected{{end}}>yes</option>
              <option value="0"{{if eq .ObfuscateVal "0"}} selected{{end}}>no</option>
            </select>
          </div>
          <div class="col-md-4">
            <label class="form-label ag-grp__label">Watermark</label>
            <input type="text" name="watermark_text" class="form-control form-control-sm" value="{{.WatermarkText}}" placeholder="-YourTag" {{if not $.CanManage}}disabled{{end}}>
          </div>
          <div class="col-md-12">
            <label class="form-label ag-grp__label">Banned extensions</label>
            <textarea name="banned_extensions" class="form-control form-control-sm" rows="2"
                      placeholder="(blank = use agent's default blocklist)" {{if not $.CanManage}}disabled{{end}}>{{.BannedText}}</textarea>
            <small class="ag-hint">One per line. A non-empty list replaces the agent's default blocklist outright.</small>
          </div>
        </div>
        {{if $.CanManage}}
        <div class="mt-3"><button type="submit" class="btn btn-primary btn-sm">Save changes</button></div>
        {{end}}
      </form>
    </div>
  </div>
  {{else}}
  <div class="card"><div class="card-body ag-empty ag-grp__none">No agent groups defined.</div></div>
  {{end}}

  <p class="ag-hint ag-grp__foot">Samples, Sec/sample, PAR2 and Obfuscate are
  nullable: blank means the agent uses its own default for that group, which is
  not the same as zero.</p>
</div>`))

// registerGroupsPage mounts the page and its three actions, and only when the
// host can at least list. Same reasoning as the roster: a page that renders
// "no groups" on a host whose seam is missing ends an investigation that
// should have started.
func (p *Plugin) registerGroupsPage(c *core.Core) error {
	if deps.ListAgentGroups == nil {
		return nil
	}
	return c.RegisterView(core.View{
		Slug:    "agent-groups",
		Title:   "Agent Groups",
		Slot:    core.SlotAdminPage,
		MinRole: core.RoleAdmin,
		// Files the hub card under the host's existing Operations
		// section rather than a generic Plugins bucket, so the fleet
		// pages stay next to the dispatch ones they belong with.
		Nav:         core.NavHint{Group: "Operations"},
		Description: "Posting profiles pushed to the fleet: newsgroups, samples, PAR2 and obfuscation.",
		Render:      p.renderGroupsPage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"create": p.actionCreateGroup,
			"update": p.actionUpdateGroup,
			"delete": p.actionDeleteGroup,
		},
	})
}

type groupsPageVM struct {
	Groups    []groupRow
	CanManage bool
	CSRFToken string
	Msg, Err  string
}

func (p *Plugin) renderGroupsPage(gc *gin.Context) (template.HTML, error) {
	groups, err := deps.ListAgentGroups(gc.Request.Context())
	if err != nil {
		return "", err
	}
	vm := groupsPageVM{
		CanManage: deps.CreateAgentGroup != nil &&
			deps.UpdateAgentGroup != nil && deps.DeleteAgentGroup != nil,
		CSRFToken: pluginapi.CSRFToken(p.core, gc),
		Msg:       gc.Query("msg"),
		Err:       gc.Query("err"),
	}
	for _, g := range groups {
		vm.Groups = append(vm.Groups, groupRow{AgentGroup: g})
	}
	var sb strings.Builder
	if err := groupsPageTmpl.Execute(&sb, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// backToGroups is the post-action redirect. Actions return ("", nil) after
// redirecting, which is the plugin-page contract for "I have written the
// response myself".
func backToGroups(gc *gin.Context, msg, errMsg string) (template.HTML, error) {
	u := "/admin/p/agent-groups"
	switch {
	case msg != "":
		u += "?msg=" + url.QueryEscape(msg)
	case errMsg != "":
		u += "?err=" + url.QueryEscape(errMsg)
	}
	gc.Redirect(http.StatusSeeOther, u)
	return "", nil
}

// manageable re-checks the write seams on every action rather than trusting
// the page that drew the form. A form outlives the page it came from, and a
// host can rewire between the two.
func manageable() bool {
	return deps.CreateAgentGroup != nil && deps.UpdateAgentGroup != nil && deps.DeleteAgentGroup != nil
}

func (p *Plugin) actionCreateGroup(gc *gin.Context) (template.HTML, error) {
	if !manageable() {
		return backToGroups(gc, "", "this site does not allow editing agent groups")
	}
	g, err := groupFromForm(gc)
	if err != nil {
		return backToGroups(gc, "", err.Error())
	}
	if err := deps.CreateAgentGroup(gc.Request.Context(), g); err != nil {
		return backToGroups(gc, "", err.Error())
	}
	return backToGroups(gc, "created "+g.Name, "")
}

func (p *Plugin) actionUpdateGroup(gc *gin.Context) (template.HTML, error) {
	if !manageable() {
		return backToGroups(gc, "", "this site does not allow editing agent groups")
	}
	id, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("group_id")))
	if err != nil || id <= 0 {
		return backToGroups(gc, "", "which group?")
	}
	g, err := groupFromForm(gc)
	if err != nil {
		return backToGroups(gc, "", err.Error())
	}
	g.ID = id
	if err := deps.UpdateAgentGroup(gc.Request.Context(), g); err != nil {
		return backToGroups(gc, "", err.Error())
	}
	return backToGroups(gc, "updated "+g.Name, "")
}

func (p *Plugin) actionDeleteGroup(gc *gin.Context) (template.HTML, error) {
	if !manageable() {
		return backToGroups(gc, "", "this site does not allow editing agent groups")
	}
	id, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("group_id")))
	if err != nil || id <= 0 {
		return backToGroups(gc, "", "which group?")
	}
	if err := deps.DeleteAgentGroup(gc.Request.Context(), id); err != nil {
		return backToGroups(gc, "", err.Error())
	}
	return backToGroups(gc, "deleted", "")
}
