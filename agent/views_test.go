package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() { gin.SetMode(gin.TestMode) }

// The stubs are the whole payoff of function-typed deps: no interfaces to
// implement, and the test says what the host would return rather than how.

// testViewer reads the signed-in user off the context, so each test can set a
// viewer the same way a host session middleware would.
func testViewer(c *gin.Context) (int, bool) {
	v, ok := c.Get("test-viewer")
	if !ok {
		return 0, false
	}
	return v.(int), true
}

func agentsFrom(byUser map[int][]Agent) func(context.Context, int) ([]Agent, error) {
	return func(_ context.Context, userID int) ([]Agent, error) { return byUser[userID], nil }
}

func tasksFrom(byAgent map[int]*Task) func(context.Context, int) (*Task, error) {
	return func(_ context.Context, agentID int) (*Task, error) { return byAgent[agentID], nil }
}

func countFrom(byUser map[int][]Agent) func(context.Context, time.Time) (int, int, error) {
	return func(_ context.Context, since time.Time) (online, total int, err error) {
		for _, as := range byUser {
			for _, a := range as {
				total++
				if a.LastSeen != nil && a.LastSeen.After(since) {
					online++
				}
			}
		}
		return online, total, nil
	}
}

func testCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/profile", nil)
	return c
}

// The card is an owner-only surface: an agent roster (names, activity,
// last-seen) must never render for a stranger viewing the profile. deps is
// seeded with an agent so that any leak would show up in the output.
func TestRenderCard_OwnerGating(t *testing.T) {
	roster := map[int][]Agent{5: {{ID: 1, Name: "leaky"}}}
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(roster),
		ActiveTask:    tasksFrom(nil),
	})
	t.Cleanup(func() { deps = nil })
	p := &Plugin{}

	t.Run("no view subject", func(t *testing.T) {
		html, err := p.renderCard(testCtx())
		if err != nil || html != "" {
			t.Fatalf("want empty, got %q err=%v", html, err)
		}
	})

	t.Run("anonymous viewer", func(t *testing.T) {
		c := testCtx()
		core.SetViewSubject(c, 5)
		html, err := p.renderCard(c)
		if err != nil || html != "" {
			t.Fatalf("want empty, got %q err=%v", html, err)
		}
	})

	t.Run("non-owner viewer does not leak the roster", func(t *testing.T) {
		c := testCtx()
		core.SetViewSubject(c, 5)
		c.Set("test-viewer", 6) // viewing user 5's profile as user 6
		html, err := p.renderCard(c)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if html != "" {
			t.Fatalf("agent roster leaked to non-owner: %q", html)
		}
	})
}

func TestRenderCard_OwnerSeesFleet(t *testing.T) {
	seen := time.Now().Add(-2 * time.Minute)
	SetDeps(Deps{
		Viewer: testViewer,
		AgentsForUser: agentsFrom(map[int][]Agent{
			5: {{ID: 1, Name: "alpha", LastSeen: &seen}},
		}),
		ActiveTask: tasksFrom(map[int]*Task{1: {RequestID: 42, Progress: "50%"}}),
	})
	t.Cleanup(func() { deps = nil })

	c := testCtx()
	core.SetViewSubject(c, 5)
	c.Set("test-viewer", 5)
	html, err := (&Plugin{}).renderCard(c)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(html)
	for _, want := range []string{"Agent Fleet", "alpha", "request #42", "50%"} {
		if !strings.Contains(s, want) {
			t.Errorf("card missing %q; got: %s", want, s)
		}
	}
}

func TestRenderCard_NoAgentsRendersNothing(t *testing.T) {
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(nil),
		ActiveTask:    tasksFrom(nil),
	})
	t.Cleanup(func() { deps = nil })

	c := testCtx()
	core.SetViewSubject(c, 7)
	c.Set("test-viewer", 7)
	html, err := (&Plugin{}).renderCard(c)
	if err != nil || html != "" {
		t.Fatalf("want empty for a user with no agents, got %q err=%v", html, err)
	}
}

func TestRenderDispatchPanel(t *testing.T) {
	recent := time.Now().Add(-1 * time.Minute)
	roster := map[int][]Agent{
		1: {{ID: 1, LastSeen: &recent}},
		2: {{ID: 2}}, // never seen -> offline
	}
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(roster),
		ActiveTask:    tasksFrom(nil),
		CountAgents:   countFrom(roster),
		MaxConcurrent: func(context.Context) int { return 3 },
	})
	t.Cleanup(func() { deps = nil })

	html, err := (&Plugin{}).renderDispatchPanel(testCtx())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(html)
	// labels + the three jump links + the online/total pair + the cap value
	for _, want := range []string{
		"Agents online", "Max concurrent",
		"/admin/agents", "/admin/agent-groups", "/admin/dispatch",
		">1</span> / 2",
		">3</div>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dispatch panel missing %q; got: %s", want, s)
		}
	}
}

// Provision must refuse to boot on a partial Deps. A missing adapter used to be
// survivable — the card just rendered empty forever, which is indistinguishable
// from "this member has no agents" and so would ship unnoticed.
func TestProvisionRequiresFullDeps(t *testing.T) {
	t.Cleanup(func() { deps = nil })
	for name, d := range map[string]Deps{
		"empty":         {},
		"no viewer":     {AgentsForUser: agentsFrom(nil), ActiveTask: tasksFrom(nil), CountAgents: countFrom(nil), MaxConcurrent: func(context.Context) int { return 0 }},
		"no task":       {Viewer: testViewer, AgentsForUser: agentsFrom(nil), CountAgents: countFrom(nil), MaxConcurrent: func(context.Context) int { return 0 }},
		"no maxconcurr": {Viewer: testViewer, AgentsForUser: agentsFrom(nil), ActiveTask: tasksFrom(nil), CountAgents: countFrom(nil)},
	} {
		SetDeps(d)
		if err := (&Plugin{}).Provision(&core.Core{Process: "web"}); err == nil {
			t.Errorf("%s: Provision accepted an incomplete Deps", name)
		}
	}
}

func TestShortDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		if got := shortDuration(tc.d); got != tc.want {
			t.Errorf("shortDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
