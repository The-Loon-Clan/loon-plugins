package ranks

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The catalog page shipped with dead Save and Delete buttons: the template
// posted to /admin/p/groups/<id>/update, but the host mounts FLAT actions
// (POST /admin/p/<slug>/<action>), so the three-segment path 404'd into
// NoRoute — and the form carried no id field at all, which the action reads
// from the body. Both bugs were invisible because the test helper copied the id
// out of a path param.
//
// These render the real template and assert the contract between it and the
// routes: exactly the join no unit test was checking.

func renderPage(t *testing.T, p *Plugin) string {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/p/groups", nil)
	frag, err := p.renderGroups(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(frag)
}

func TestGroupsPage_PostsOnlyToFlatActionURLs(t *testing.T) {
	p, st := newPlugin(t)
	seedGroup(t, st, &Group{Name: "Arashi", Kind: "paid", Visible: true, CostPoints: 45000, DurationDays: 30})
	html := renderPage(t, p)

	// Every action the host mounts is /admin/p/groups/<action> with no further
	// segments. A nested id would 404.
	for _, action := range regexp.MustCompile(`action="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		got := action[1]
		if !strings.HasPrefix(got, "/admin/p/groups/") {
			t.Errorf("form posts outside the view's action namespace: %q", got)
			continue
		}
		if rest := strings.TrimPrefix(got, "/admin/p/groups/"); strings.Contains(rest, "/") {
			t.Errorf("form posts to a nested path %q — the host mounts flat actions only, so this 404s", got)
		}
	}
}

func TestGroupsPage_EveryRowActionCarriesItsID(t *testing.T) {
	p, st := newPlugin(t)
	seedGroup(t, st, &Group{Name: "Kirisame", Kind: "paid", Visible: true, DurationDays: 30})
	seedGroup(t, st, &Group{Name: "Arashi", Kind: "paid", Visible: true, DurationDays: 30})
	html := renderPage(t, p)

	// The update and delete actions read c.PostForm("id"); without the hidden
	// input they resolve group 0 and silently do nothing.
	forms := regexp.MustCompile(`(?s)<form[^>]*action="/admin/p/groups/(update|delete)"[^>]*>(.*?)</form>`).
		FindAllStringSubmatch(html, -1)
	if len(forms) != 4 { // two rows x (update, delete)
		t.Fatalf("found %d update/delete forms, want 4 (one pair per group)", len(forms))
	}
	for _, f := range forms {
		if !strings.Contains(f[2], `name="id"`) {
			t.Errorf("%s form has no id field — the action would resolve group 0", f[1])
		}
	}
}

// The visibility switch is the admin-only lever; a mod must not even be handed
// the control, and the row must still round-trip its current value.
func TestGroupsPage_VisibilityControlIsAdminOnly(t *testing.T) {
	staff := func() *Group {
		return &Group{Name: "Staff", Kind: "assigned", Visible: false, DurationDays: 30}
	}

	pMod, stMod := newPlugin(t)
	seedGroup(t, stMod, staff())
	modHTML := renderPage(t, pMod)
	if strings.Contains(modHTML, `type="checkbox" name="visible"`) {
		t.Error("a mod was rendered the visibility switch")
	}
	if !strings.Contains(modHTML, `name="visible"`) {
		t.Error("the mod form does not round-trip the current visibility")
	}

	pAdmin, stAdmin := newAdminPlugin(t)
	seedGroup(t, stAdmin, staff())
	if !strings.Contains(renderPage(t, pAdmin), `type="checkbox" name="visible"`) {
		t.Error("an admin was not rendered the visibility switch")
	}
}

func TestGroupsPage_ShowsMemberCounts(t *testing.T) {
	p, st := newPlugin(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Kind: "paid", Visible: true, DurationDays: 30})
	if err := st.AddMember(context.Background(), 1764, g.ID, 24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if !strings.Contains(renderPage(t, p), "Members: <strong>1</strong>") {
		t.Error("the live member count is not rendered")
	}
}
