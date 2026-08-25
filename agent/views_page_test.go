package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func pageCtx(viewer int) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/p/agents", nil)
	if viewer != 0 {
		c.Set("test-viewer", viewer)
	}
	return c, w
}

// The page's three feature blocks each stand on an optional dep: a host that
// wired none of them still renders the roster (via the card's own reads) and
// hides self-service and visibility rather than erroring.
func TestMemberPageDegradesWithoutOptionalDeps(t *testing.T) {
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(map[int][]Agent{5: {{ID: 1, Name: "alpha"}}}),
		ActiveTask:    tasksFrom(map[int]*Task{1: {RequestID: 9, Progress: "20%"}}),
	})
	t.Cleanup(func() { deps = nil })
	p := &Plugin{core: &core.Core{}}

	c, _ := pageCtx(5)
	html, err := p.renderMemberPage(c)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(html)
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "request #9") {
		t.Errorf("fallback roster missing: %s", s)
	}
	for _, gone := range []string{"New agent", "Rotate token", "Visibility"} {
		if strings.Contains(s, gone) {
			t.Errorf("optional block %q rendered without its dep", gone)
		}
	}
}

// With the full seams wired, the page carries live detail, self-service and
// the visibility choice — and every verb the forms post to is owner-scoped
// by signature, pinned here by asserting the ownerID that reaches the deps.
func TestMemberPageFullSeams(t *testing.T) {
	var askedOwner int
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(nil),
		ActiveTask:    tasksFrom(nil),
		AgentsDetail: func(_ context.Context, ownerID int) ([]AgentDetail, error) {
			askedOwner = ownerID
			return []AgentDetail{{
				Agent: Agent{ID: 3, Name: "homeserver"},
				Status: &AgentStatus{
					Phase: "downloading", VPNStatus: "up", PublicIP: "203.0.113.9",
					DownloadSpeed: "12 MB/s", TaskTitle: "Some Show - 08", RequestID: 77,
					Files: []FileDetail{{Name: "ep08.mkv", Percent: 41.5, Phase: "dl", Speed: "12 MB/s"}},
				},
			}}, nil
		},
		CreateAgentFor:   func(_ context.Context, ownerID int, name string) (string, error) { return "tok-abc", nil },
		RotateTokenFor:   func(_ context.Context, ownerID, agentID int) (string, error) { return "tok-new", nil },
		DeleteAgentFor:   func(_ context.Context, ownerID, agentID int) error { return nil },
		ShowOnProfile:    func(context.Context, int) (bool, error) { return true, nil },
		SetShowOnProfile: func(context.Context, int, bool) error { return nil },
	})
	t.Cleanup(func() { deps = nil })
	p := &Plugin{core: &core.Core{}}

	c, _ := pageCtx(5)
	html, err := p.renderMemberPage(c)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if askedOwner != 5 {
		t.Errorf("AgentsDetail asked for owner %d, want the viewer 5", askedOwner)
	}
	s := string(html)
	for _, want := range []string{"homeserver", "downloading", "203.0.113.9", "Some Show - 08",
		"ep08.mkv", "New agent", "Rotate token", "Visibility", "checked"} {
		if !strings.Contains(s, want) {
			t.Errorf("full page missing %q", want)
		}
	}
}

// Create returns the token page directly — a token cannot survive a
// redirect, and this render is the only time it exists in HTML.
func TestCreateAgentShowsTheTokenOnce(t *testing.T) {
	SetDeps(Deps{
		Viewer:        testViewer,
		AgentsForUser: agentsFrom(nil),
		ActiveTask:    tasksFrom(nil),
		CreateAgentFor: func(_ context.Context, ownerID int, name string) (string, error) {
			if ownerID != 5 || name != "box" {
				t.Errorf("CreateAgentFor(%d, %q), want (5, box)", ownerID, name)
			}
			return "tok-once-xyz", nil
		},
	})
	t.Cleanup(func() { deps = nil })
	p := &Plugin{core: &core.Core{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/p/agents/create",
		strings.NewReader("name=box"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("test-viewer", 5)

	html, err := p.actionCreateAgent(c)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(string(html), "tok-once-xyz") {
		t.Errorf("token page missing the token: %s", html)
	}
}
