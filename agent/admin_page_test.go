package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func adminCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/admin/p/agents", nil)
	return c
}

func rosterOf(rows []AdminAgent, err error) func(context.Context) ([]AdminAgent, error) {
	return func(context.Context) ([]AdminAgent, error) { return rows, err }
}

// The base five deps Provision demands, so a test can vary only the seam it
// is about.
func baseDeps() Deps {
	return Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(map[int][]Agent{}),
		ActiveTask:    tasksFrom(map[int]*Task{}),
		CountAgents: func(context.Context, time.Time) (int, int, error) {
			return 0, 0, nil
		},
		MaxConcurrent: func(context.Context) int { return 2 },
	}
}

// An unwired seam and an empty fleet must not look the same. Without
// AllAgents there is no page at all, rather than a page reporting zero
// agents — an operator can investigate a missing page, but a page that
// confidently says "none" ends the investigation.
func TestRosterPageAbsentWithoutTheSeam(t *testing.T) {
	SetDeps(baseDeps())
	t.Cleanup(func() { deps = nil })

	c := &core.Core{}
	p := &Plugin{core: c}
	if err := p.registerAdminPage(c); err != nil {
		t.Fatalf("registerAdminPage err=%v", err)
	}
	if got := len(c.AllViews(core.SlotAdminPage)); got != 0 {
		t.Fatalf("registered %d admin views with no AllAgents, want 0", got)
	}

	d := baseDeps()
	d.AllAgents = rosterOf(nil, nil)
	SetDeps(d)
	c2 := &core.Core{}
	p2 := &Plugin{core: c2}
	if err := p2.registerAdminPage(c2); err != nil {
		t.Fatalf("registerAdminPage err=%v", err)
	}
	views := c2.AllViews(core.SlotAdminPage)
	if len(views) != 1 || views[0].Slug != "agents" {
		t.Fatalf("views = %+v, want one slug 'agents'", views)
	}
	// The roster names people's machines and their owners. If this ever
	// registers below admin, it is a disclosure and not a styling change.
	if views[0].MinRole != core.RoleAdmin {
		t.Errorf("MinRole = %v, want RoleAdmin", views[0].MinRole)
	}
}

// The counters are the only computed thing on the page, and "revoked" is
// deliberately everything that is not active — a state the host adds later
// (expired, suspended) counts as not-active rather than silently vanishing
// from both totals.
func TestRosterCountsOnlineAndNotActive(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	stale := time.Now().Add(-24 * time.Hour)

	d := baseDeps()
	d.AllAgents = rosterOf([]AdminAgent{
		{ID: 1, Name: "alpha", Owner: "ame", Status: "active", LastSeen: &recent},
		{ID: 2, Name: "bravo", Owner: "kanri", Status: "active", LastSeen: &stale},
		{ID: 3, Name: "charlie", Owner: "ame", Status: "revoked"},
		{ID: 4, Name: "delta", Owner: "ame", Status: "suspended", LastSeen: &recent},
	}, nil)
	SetDeps(d)
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderAdminRoster(adminCtx())
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	s := string(html)

	for _, want := range []string{
		"<strong>2</strong> online", // alpha + delta reported recently
		"<strong>4</strong> registered",
		"<strong>2</strong> revoked", // charlie + delta: not active
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q in:\n%s", want, s)
		}
	}
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "kanri"} {
		if !strings.Contains(s, name) {
			t.Errorf("roster missing %q", name)
		}
	}
	// An unknown state is shown as itself rather than mislabelled Active.
	if !strings.Contains(s, "Suspended") {
		t.Errorf("unknown status not rendered readably:\n%s", s)
	}
}

// A read that fails must reach the host's error page rather than rendering a
// roster that looks empty. Returning ("", err) is what makes the difference,
// so it is pinned.
func TestRosterPropagatesReadError(t *testing.T) {
	boom := errors.New("db down")
	d := baseDeps()
	d.AllAgents = rosterOf(nil, boom)
	SetDeps(d)
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderAdminRoster(adminCtx())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error", err)
	}
	if html != "" {
		t.Errorf("rendered %q alongside an error, want empty", html)
	}
}

// An empty fleet on a WIRED host is a legitimate answer and says so.
func TestRosterEmptyStateIsExplicit(t *testing.T) {
	d := baseDeps()
	d.AllAgents = rosterOf(nil, nil)
	SetDeps(d)
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderAdminRoster(adminCtx())
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(string(html), "No agents registered yet") {
		t.Errorf("empty state missing:\n%s", html)
	}
}

// The dispatch panel must not offer a door to a page this host did not mount.
func TestDispatchPanelLinksRosterOnlyWhenMounted(t *testing.T) {
	SetDeps(baseDeps())
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderDispatchPanel(adminCtx())
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if strings.Contains(string(html), "/admin/p/agents") {
		t.Errorf("panel linked the roster with no AllAgents:\n%s", html)
	}

	d := baseDeps()
	d.AllAgents = rosterOf(nil, nil)
	SetDeps(d)
	html, err = (&Plugin{}).renderDispatchPanel(adminCtx())
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(string(html), "/admin/p/agents") {
		t.Errorf("panel did not link the mounted roster:\n%s", html)
	}
}

// fieldExists reports whether T has a field of the given name.
func fieldExists[T any](name string) bool {
	var zero T
	_, ok := reflect.TypeOf(zero).FieldByName(name)
	return ok
}

// AdminAgent carries no token field, and that is load-bearing rather than
// incidental: this page renders inside an admin template where a stray
// {{.Token}} would be invisible in review. The type is the guarantee, so a
// field added to it later trips this.
func TestAdminAgentCannotCarryASecret(t *testing.T) {
	for _, banned := range []string{"Token", "TokenHash", "Secret", "Hash"} {
		if fieldExists[AdminAgent](banned) {
			t.Errorf("AdminAgent grew a %q field — the roster template can now print a credential", banned)
		}
	}
}
