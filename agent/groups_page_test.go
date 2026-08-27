package agent

import (
	"context"
	"html/template"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// postCtx builds a form POST the way the page's own forms submit.
func postCtx(form url.Values) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/admin/p/agent-groups/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	return c
}

func groupsCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/admin/p/agent-groups", nil)
	return c
}

// validForm is the minimum the form accepts, so a case can vary one field.
func validForm() url.Values {
	return url.Values{
		"name":       {"anime"},
		"newsgroups": {"alt.binaries.boneless"},
	}
}

// THE central rule of this page: blank is not zero. 0 screenshots means take
// none and 0% PAR2 means ship without recovery, so coercing an empty box to
// zero would turn every unset override into the most aggressive setting
// available — silently, on every group an operator saved.
func TestBlankOverrideStaysNil(t *testing.T) {
	f := validForm()
	f.Set("screenshots", "")
	f.Set("sample_seconds", "")
	f.Set("par2_redundancy", "")
	f.Set("obfuscate", "")

	g, err := groupFromForm(postCtx(f))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if g.Screenshots != nil || g.SampleSeconds != nil || g.Par2Redundancy != nil || g.Obfuscate != nil {
		t.Fatalf("blank fields produced non-nil overrides: %+v", g)
	}

	// And an explicit zero is a real instruction that must survive.
	f.Set("screenshots", "0")
	f.Set("par2_redundancy", "0")
	f.Set("obfuscate", "0")
	g, err = groupFromForm(postCtx(f))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if g.Screenshots == nil || *g.Screenshots != 0 {
		t.Errorf("screenshots = %v, want an explicit 0", g.Screenshots)
	}
	if g.Par2Redundancy == nil || *g.Par2Redundancy != 0 {
		t.Errorf("par2 = %v, want an explicit 0", g.Par2Redundancy)
	}
	if g.Obfuscate == nil || *g.Obfuscate {
		t.Errorf("obfuscate = %v, want an explicit false", g.Obfuscate)
	}
}

// A typo must not read as "leave it to the agent". On the host page it did:
// strconv failure fell through to nil, so "6o" and "" were the same request.
func TestUnparseableOverrideIsAnError(t *testing.T) {
	f := validForm()
	f.Set("screenshots", "6o")
	if _, err := groupFromForm(postCtx(f)); err == nil {
		t.Fatal("a non-numeric override was accepted as nil")
	}
	f = validForm()
	f.Set("par2_redundancy", "150")
	if _, err := groupFromForm(postCtx(f)); err == nil {
		t.Fatal("PAR2 of 150% was accepted")
	}
}

// The two textareas round-trip: what the page renders must parse back to what
// it was rendered from, or an operator pressing Save without editing anything
// silently rewrites the group.
func TestTextareasRoundTrip(t *testing.T) {
	orig := AgentGroup{
		Name:             "anime",
		Type:             "video",
		Newsgroups:       []string{"alt.binaries.boneless", "alt.binaries.multimedia.anime"},
		BannedExtensions: []string{".exe", ".scr"},
	}
	row := groupRow{AgentGroup: orig}

	f := url.Values{
		"name":              {orig.Name},
		"type":              {orig.Type},
		"newsgroups":        {row.NewsgroupsText()},
		"banned_extensions": {row.BannedText()},
	}
	back, err := groupFromForm(postCtx(f))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(back.Newsgroups, ",") != strings.Join(orig.Newsgroups, ",") {
		t.Errorf("newsgroups round-tripped to %v, want %v", back.Newsgroups, orig.Newsgroups)
	}
	if strings.Join(back.BannedExtensions, ",") != strings.Join(orig.BannedExtensions, ",") {
		t.Errorf("extensions round-tripped to %v, want %v", back.BannedExtensions, orig.BannedExtensions)
	}
}

// Extensions are normalised because the agent compares against filepath.Ext
// output, which is lowercase and dotted. A list that does not match that shape
// bans nothing at all, which looks identical to having no list.
func TestExtensionsNormalise(t *testing.T) {
	f := validForm()
	f.Set("banned_extensions", "EXE\n.Scr\n  bat  ")
	g, err := groupFromForm(postCtx(f))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := ".exe,.scr,.bat"
	if got := strings.Join(g.BannedExtensions, ","); got != want {
		t.Errorf("extensions = %q, want %q", got, want)
	}
}

// A group with no newsgroup posts nowhere, so it is refused rather than saved
// as an empty profile agents would fetch and act on.
func TestNewsgroupsRequired(t *testing.T) {
	f := validForm()
	f.Set("newsgroups", "   \n  ")
	if _, err := groupFromForm(postCtx(f)); err == nil {
		t.Fatal("a group with no newsgroups was accepted")
	}
	f = validForm()
	f.Set("name", "  ")
	if _, err := groupFromForm(postCtx(f)); err == nil {
		t.Fatal("a nameless group was accepted")
	}
}

func groupDeps(list []AgentGroup, writable bool) Deps {
	d := baseDeps()
	d.ListAgentGroups = func(context.Context) ([]AgentGroup, error) { return list, nil }
	if writable {
		d.CreateAgentGroup = func(context.Context, AgentGroup) error { return nil }
		d.UpdateAgentGroup = func(context.Context, AgentGroup) error { return nil }
		d.DeleteAgentGroup = func(context.Context, int) error { return nil }
	}
	return d
}

// No list seam, no page — same rule as the roster.
func TestGroupsPageAbsentWithoutTheSeam(t *testing.T) {
	SetDeps(baseDeps())
	t.Cleanup(func() { deps = nil })

	c := &core.Core{}
	if err := (&Plugin{core: c}).registerGroupsPage(c); err != nil {
		t.Fatalf("err=%v", err)
	}
	if n := len(c.AllViews(core.SlotAdminPage)); n != 0 {
		t.Fatalf("registered %d views with no ListAgentGroups, want 0", n)
	}

	SetDeps(groupDeps(nil, true))
	c2 := &core.Core{}
	if err := (&Plugin{core: c2}).registerGroupsPage(c2); err != nil {
		t.Fatalf("err=%v", err)
	}
	views := c2.AllViews(core.SlotAdminPage)
	if len(views) != 1 || views[0].Slug != "agent-groups" {
		t.Fatalf("views = %+v, want one slug 'agent-groups'", views)
	}
	if views[0].MinRole != core.RoleAdmin {
		t.Errorf("MinRole = %v, want RoleAdmin", views[0].MinRole)
	}
	for _, a := range []string{"create", "update", "delete"} {
		if views[0].Actions[a] == nil {
			t.Errorf("action %q not registered", a)
		}
	}
}

// The plugin now owns two admin pages; they must not fight over a slug.
func TestBothAdminPagesCoexist(t *testing.T) {
	d := groupDeps(nil, true)
	d.AllAgents = rosterOf(nil, nil)
	SetDeps(d)
	t.Cleanup(func() { deps = nil })

	c := &core.Core{}
	p := &Plugin{core: c}
	if err := p.registerAdminPage(c); err != nil {
		t.Fatalf("roster: %v", err)
	}
	if err := p.registerGroupsPage(c); err != nil {
		t.Fatalf("groups: %v", err)
	}
	if n := len(c.AllViews(core.SlotAdminPage)); n != 2 {
		t.Fatalf("got %d admin views, want 2", n)
	}
}

// Listable but not writable renders a read-only page rather than a broken one,
// and it must not draw a create form it cannot submit.
func TestGroupsPageReadOnlyWithoutMutators(t *testing.T) {
	SetDeps(groupDeps([]AgentGroup{{ID: 1, Name: "anime", Type: "video",
		Newsgroups: []string{"alt.binaries.boneless"}, Version: 3}}, false))
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderGroupsPage(groupsCtx())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(html)
	if !strings.Contains(s, "anime") || !strings.Contains(s, "v3") {
		t.Errorf("group not rendered:\n%s", s)
	}
	if strings.Contains(s, "/admin/p/agent-groups/create") {
		t.Errorf("create form drawn without a CreateAgentGroup seam:\n%s", s)
	}
	if !strings.Contains(s, "read-only") {
		t.Errorf("read-only state not explained:\n%s", s)
	}
}

// Every action re-checks the write seams. A form outlives the page that drew
// it, and a host can be rewired between the two.
func TestGroupActionsRefuseWithoutMutators(t *testing.T) {
	SetDeps(groupDeps(nil, false))
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	for name, action := range map[string]func(*gin.Context) (template.HTML, error){
		"create": p.actionCreateGroup,
		"update": p.actionUpdateGroup,
		"delete": p.actionDeleteGroup,
	} {
		f := validForm()
		f.Set("group_id", "1")
		c := postCtx(f)
		if _, err := action(c); err != nil {
			t.Fatalf("%s returned err=%v, want a redirect", name, err)
		}
		loc := c.Writer.Header().Get("Location")
		if !strings.Contains(loc, "err=") {
			t.Errorf("%s redirected to %q, want an error message", name, loc)
		}
	}
}

// An update or delete without a usable id must not reach the host.
func TestUpdateAndDeleteNeedAnID(t *testing.T) {
	called := false
	d := groupDeps(nil, true)
	d.UpdateAgentGroup = func(context.Context, AgentGroup) error { called = true; return nil }
	d.DeleteAgentGroup = func(context.Context, int) error { called = true; return nil }
	SetDeps(d)
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	for _, id := range []string{"", "0", "-3", "abc"} {
		f := validForm()
		f.Set("group_id", id)
		if _, err := p.actionUpdateGroup(postCtx(f)); err != nil {
			t.Fatalf("update err=%v", err)
		}
		if _, err := p.actionDeleteGroup(postCtx(f)); err != nil {
			t.Fatalf("delete err=%v", err)
		}
		if called {
			t.Fatalf("group_id %q reached the host", id)
		}
	}
}

// The panel must not offer a door to a groups page this host did not mount.
func TestDispatchPanelLinksGroupsOnlyWhenMounted(t *testing.T) {
	SetDeps(baseDeps())
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderDispatchPanel(groupsCtx())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(string(html), "/admin/p/agent-groups") {
		t.Errorf("panel linked agent-groups with no ListAgentGroups:\n%s", html)
	}

	SetDeps(groupDeps(nil, true))
	html, err = (&Plugin{}).renderDispatchPanel(groupsCtx())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(string(html), "/admin/p/agent-groups") {
		t.Errorf("panel did not link the mounted agent-groups page:\n%s", html)
	}
}
