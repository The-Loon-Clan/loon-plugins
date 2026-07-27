package ranks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

func init() { gin.SetMode(gin.TestMode) }

// deductCall records the args of a fake PointsService.Deduct.
type deductCall struct {
	userID int64
	n      int
	reason string
	ref    int64
}

// newPlugin wires a Plugin over the in-memory group store, enough to drive the
// admin View's actions. The viewer resolves as a non-admin (the zero
// AuthAdapter has no CurrentUser), which is the interesting default: it is the
// mod case, where visibility must come from the stored row rather than the
// form.
func newPlugin(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &Plugin{
		store: st,
		errs:  core.NewErrorReporter(core.ErrorAdapter{}),
		auth:  core.NewAuth(core.AuthAdapter{}),
	}, st
}

// newAdminPlugin is newPlugin with a viewer who is an admin, for the paths
// gated on that.
func newAdminPlugin(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &Plugin{
		store: st,
		errs:  core.NewErrorReporter(core.ErrorAdapter{}),
		auth: core.NewAuth(core.AuthAdapter{
			CurrentUserFn: func(*gin.Context) (*core.User, bool) {
				return &core.User{ID: 1, Role: core.RoleAdmin}, true
			},
		}),
	}, st
}

// The buy-rank flow that used to live here is gone: the store plugin now
// deducts the points and calls GrantRank through the capability, so there is
// one purchase path for every reward type instead of a rank-only one hanging
// off /profile. The tests below cover what survived the move — the granter,
// which is where the rank-specific semantics ended up.

func seedGroup(t *testing.T, st *MemStore, g *Group) *Group {
	t.Helper()
	if g.Grants == nil {
		g.Grants = map[string]int64{}
	}
	if err := st.CreateGroup(context.Background(), g); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	return g
}

func newGranter(t *testing.T) (*rankGranter, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &rankGranter{store: st, errs: core.NewErrorReporter(core.ErrorAdapter{})}, st
}

// The old profile handler floored the duration at 30 days before subscribing.
// The store passes an item's reward_days instead, which a mis-typed catalog
// row can leave at 0 — that would subscribe the user to an ALREADY-EXPIRED
// rank after taking their points. The floor moved into the granter with the
// flow; this pins it there.
func TestGrantRank_NonPositiveDurationFallsBackToRankDuration(t *testing.T) {
	g, st := newGranter(t)
	grp := seedGroup(t, st, &Group{Name: "Kirisame", CostPoints: 5000, DurationDays: 30})

	if _, err := g.GrantRank(context.Background(), 7, grp.ID, 0); err != nil {
		t.Fatalf("GrantRank: %v", err)
	}
	m, err := st.ActiveMembership(context.Background(), 7)
	if err != nil || m == nil {
		t.Fatalf("no membership after grant: %v", err)
	}
	if !m.ExpiresAt.After(time.Now().Add(29 * 24 * time.Hour)) {
		t.Errorf("expires %s — a 0 duration granted an already-expired rank", m.ExpiresAt)
	}
}

func TestGrantRank_NonPositiveDurationAndUnsetRankDaysDefaultsTo30(t *testing.T) {
	g, st := newGranter(t)
	grp := seedGroup(t, st, &Group{Name: "Shigure", CostPoints: 10000, DurationDays: 0})

	if _, err := g.GrantRank(context.Background(), 7, grp.ID, 0); err != nil {
		t.Fatalf("GrantRank: %v", err)
	}
	m, _ := st.ActiveMembership(context.Background(), 7)
	if m == nil || !m.ExpiresAt.After(time.Now().Add(29*24*time.Hour)) {
		t.Errorf("want the 30-day default when the rank carries no duration, got %+v", m)
	}
}

// An explicit duration from the caller must win — the floor is a fallback,
// not an override, or a 7-day promo item would silently become 30 days.
func TestGrantRank_ExplicitDurationWins(t *testing.T) {
	g, st := newGranter(t)
	grp := seedGroup(t, st, &Group{Name: "Samidare", CostPoints: 25000, DurationDays: 30})

	if _, err := g.GrantRank(context.Background(), 7, grp.ID, 7*24*time.Hour); err != nil {
		t.Fatalf("GrantRank: %v", err)
	}
	m, _ := st.ActiveMembership(context.Background(), 7)
	if m == nil || m.ExpiresAt.After(time.Now().Add(8*24*time.Hour)) {
		t.Errorf("a 7-day grant was widened to the rank default: %+v", m)
	}
}

func TestGrantRank_UnknownRankErrors(t *testing.T) {
	g, _ := newGranter(t)
	if _, err := g.GrantRank(context.Background(), 7, 999, time.Hour); err == nil {
		t.Error("granting an unknown rank must error so the store unwinds the points")
	}
}

// formCtx builds a POST /admin/ranks form context.
// formCtxID is formCtx with the row id in the BODY, which is where the flat
// action reads it — the helper used to copy it out of a path param, which hid
// both that the template posted the wrong URL shape and that it never sent an
// id at all.
func formCtxID(form url.Values, id int) (*httptest.ResponseRecorder, *gin.Context) {
	form.Set("id", strconv.Itoa(id))
	return formCtx(form, nil)
}

func formCtx(form url.Values, params gin.Params) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/admin/p/groups/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	c.Params = params
	return w, c
}

func TestCreateRank_ClampsMissingNumericFieldsToDefaults(t *testing.T) {
	p, st := newPlugin(t)
	// Only a name; every numeric field is absent/invalid -> defaults.
	form := url.Values{}
	form.Set("name", "Bronze")
	w, c := formCtx(form, nil)
	_, _ = p.actionCreate(c)

	if loc := w.Header().Get("Location"); loc != "/admin/p/groups?ok=1" {
		t.Fatalf("want ok redirect, got %q", loc)
	}
	all, _ := st.Groups(context.Background())
	if len(all) != 1 {
		t.Fatalf("want 1 group, got %d", len(all))
	}
	g := all[0]
	if g.Name != "Bronze" {
		t.Errorf("name = %q", g.Name)
	}
	// Blank limits mean "no opinion", NOT the code default: storing a default
	// as a grant would push it to the legacy mirror and overwrite an
	// operator-set value (this is how saving any group used to rewrite Free's
	// api_limit from 1000 to 10000).
	if _, ok := g.Grants[entDownloadDaily]; ok {
		t.Errorf("a blank download limit was stored as a grant: %v", g.Grants)
	}
	if _, ok := g.Grants[entAPIDaily]; ok {
		t.Errorf("a blank api limit was stored as a grant: %v", g.Grants)
	}
	if g.DurationDays != 30 {
		t.Errorf("duration default not applied: %d", g.DurationDays)
	}
	// monthly_cost / sort_order absent -> 0 (no default clamp).
	if g.CostPoints != 0 || g.SortOrder != 0 {
		t.Errorf("cost/sort should be 0, got %d/%d", g.CostPoints, g.SortOrder)
	}
	// Cost 0 -> no DM grant, mirroring the migration's monthly_cost > 0 rule.
	if _, ok := g.Grants[entDMInitiate]; ok {
		t.Errorf("a zero-cost tier must not confer %s", entDMInitiate)
	}
	// New groups are visible paid tiers, so they mirror to the legacy table.
	if !g.Visible || g.Kind != "paid" {
		t.Errorf("new group should be a visible paid tier, got visible=%v kind=%q", g.Visible, g.Kind)
	}
	if g.Slug != "bronze" {
		t.Errorf("slug = %q, want bronze", g.Slug)
	}
}

func TestCreateRank_KeepsExplicitValues(t *testing.T) {
	p, st := newPlugin(t)
	form := url.Values{}
	form.Set("name", "Gold")
	form.Set("download_limit", "500")
	form.Set("api_limit", "99999")
	form.Set("monthly_cost", "1200")
	form.Set("duration_days", "7")
	form.Set("sort_order", "3")
	form.Set("color", "warning")
	form.Set("title_color", "#ffcc00")
	w, c := formCtx(form, nil)
	_, _ = p.actionCreate(c)

	if loc := w.Header().Get("Location"); loc != "/admin/p/groups?ok=1" {
		t.Fatalf("want ok redirect, got %q", loc)
	}
	all, _ := st.Groups(context.Background())
	g := all[0]
	if g.Grants[entDMInitiate] != 1 {
		t.Errorf("a paid tier should grant %s — the half of canSendDM the role baseline does not cover", entDMInitiate)
	}
	if g.Grants[entDownloadDaily] != 500 || g.Grants[entAPIDaily] != 99999 || g.CostPoints != 1200 ||
		g.DurationDays != 7 || g.SortOrder != 3 || g.Color != "warning" || g.TitleColor != "#ffcc00" {
		t.Errorf("explicit values not preserved: %+v", g)
	}
}

func TestCreateRank_NameRequired(t *testing.T) {
	p, st := newPlugin(t)
	form := url.Values{}
	form.Set("download_limit", "500")
	w, c := formCtx(form, nil)
	_, _ = p.actionCreate(c)

	if loc := w.Header().Get("Location"); loc != "/admin/p/groups?error=name+required" {
		t.Fatalf("want name-required redirect, got %q", loc)
	}
	all, _ := st.Groups(context.Background())
	if len(all) != 0 {
		t.Errorf("no group should be created, got %d", len(all))
	}
}

// An edit that leaves the limit fields blank must PRESERVE the group's stored
// limits, not reset them to the code defaults — the same "absent is not a
// value" rule that keeps a save from rewriting Free's api_limit.
func TestUpdateRank_BlankLimitsPreserveTheStoredOnes(t *testing.T) {
	p, st := newPlugin(t)
	seedGroup(t, st, &Group{
		Name: "Old", CostPoints: 1, DurationDays: 1, SortOrder: 1, Visible: true, Kind: "paid",
		Grants: map[string]int64{entDownloadDaily: 1, entAPIDaily: 1},
	})

	form := url.Values{}
	form.Set("name", "New")
	// numeric fields omitted -> clamp to defaults.
	w, c := formCtxID(form, 1)
	_, _ = p.actionUpdate(c)

	if loc := w.Header().Get("Location"); loc != "/admin/p/groups?ok=1" {
		t.Fatalf("want ok redirect, got %q", loc)
	}
	g, err := st.Group(context.Background(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if g.Name != "New" || g.DurationDays != 30 {
		t.Errorf("update not applied: %+v", g)
	}
	if g.Grants[entDownloadDaily] != 1 || g.Grants[entAPIDaily] != 1 {
		t.Errorf("blank limit fields overwrote the stored grants: %v", g.Grants)
	}
}

// A form that OMITS a field must not clear it. The admin View posts kind and
// visibility, but an older cached page or a partial POST does not — and
// silently converting a hidden staff group into a purchasable paid tier would
// republish it to every legacy display reader.
func TestUpdateGroup_OmittedFieldsAreNotCleared(t *testing.T) {
	p, st := newPlugin(t)
	parent := 1
	seedGroup(t, st, &Group{Name: "Parent", Visible: true, Kind: "paid"})
	seedGroup(t, st, &Group{
		Name: "Staff", Visible: false, Kind: "assigned", ParentID: &parent, Icon: "shield",
	})

	form := url.Values{"name": {"Staff Renamed"}} // nothing else
	_, c := formCtxID(form, 2)
	_, _ = p.actionUpdate(c)

	g, err := st.Group(context.Background(), 2)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if g.Name != "Staff Renamed" {
		t.Errorf("name = %q, want the edit applied", g.Name)
	}
	if g.Visible {
		t.Error("a hidden group became visible — it would republish to every legacy reader")
	}
	if g.Kind != "assigned" {
		t.Errorf("kind = %q, want assigned preserved when the form omits it", g.Kind)
	}
	if g.ParentID == nil || *g.ParentID != 1 {
		t.Errorf("parent lost: %v", g.ParentID)
	}
	if g.Icon != "shield" {
		t.Errorf("icon = %q, want shield preserved", g.Icon)
	}
}

// Visibility is admin-only: a mod POSTing visible=1 against a hidden group must
// not publish it, because that lever is how an invisible entitlement-granting
// group would become a visible badge (or vice versa).
func TestUpdateGroup_ModCannotFlipVisibility(t *testing.T) {
	p, st := newPlugin(t) // viewer is not an admin
	seedGroup(t, st, &Group{Name: "Staff", Visible: false, Kind: "assigned"})

	form := url.Values{"name": {"Staff"}, "visible": {"1"}}
	_, c := formCtxID(form, 1)
	_, _ = p.actionUpdate(c)

	g, _ := st.Group(context.Background(), 1)
	if g.Visible {
		t.Error("a mod published a hidden group by posting visible=1")
	}
}

func TestUpdateGroup_AdminCanFlipVisibility(t *testing.T) {
	p, st := newAdminPlugin(t)
	seedGroup(t, st, &Group{Name: "Staff", Visible: false, Kind: "assigned"})

	form := url.Values{"name": {"Staff"}, "visible": {"1"}}
	_, c := formCtxID(form, 1)
	_, _ = p.actionUpdate(c)

	g, _ := st.Group(context.Background(), 1)
	if !g.Visible {
		t.Error("an admin could not make a group visible")
	}
}

// The store owns a rank's price now, so /admin/ranks no longer offers an
// editable Cost box — but UpdateRank still reads monthly_cost off the form.
// The edit form round-trips it through a hidden field precisely so an
// unrelated edit (renaming a rank, changing its colour) does not Atoi("") to 0
// and wipe what the rank historically sold for. These pin both halves.
func TestUpdateRank_PreservesRoundTrippedCost(t *testing.T) {
	p, st := newPlugin(t)
	g := seedGroup(t, st, &Group{
		Name: "Kirisame", CostPoints: 5000, DurationDays: 30, Visible: true, Kind: "paid",
		Grants: map[string]int64{entDownloadDaily: 150, entAPIDaily: 1000},
	})

	// What the edit form submits: everything, including the hidden cost.
	form := url.Values{
		"name":           {"Kirisame Renamed"},
		"download_limit": {"150"},
		"api_limit":      {"1000"},
		"monthly_cost":   {"5000"}, // the hidden field
		"duration_days":  {"30"},
	}
	_, c := formCtxID(form, g.ID)
	_, _ = p.actionUpdate(c)

	got, err := st.Group(context.Background(), g.ID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if got.Name != "Kirisame Renamed" {
		t.Errorf("name = %q, want the edit applied", got.Name)
	}
	if got.CostPoints != 5000 {
		t.Errorf("cost = %d, want 5000 preserved through the hidden field", got.CostPoints)
	}
}

// The failure the hidden field exists to prevent: if the form ever stops
// submitting monthly_cost, an edit silently zeroes it. Pinning the mechanism
// so a later "tidy up that hidden input" doesn't quietly wipe the column.
func TestUpdateRank_MissingCostFieldZeroesIt(t *testing.T) {
	p, st := newPlugin(t)
	g := seedGroup(t, st, &Group{Name: "Shigure", CostPoints: 10000, DurationDays: 30, Visible: true, Kind: "paid"})

	form := url.Values{"name": {"Shigure"}, "duration_days": {"30"}} // no monthly_cost
	_, c := formCtxID(form, g.ID)
	_, _ = p.actionUpdate(c)

	got, _ := st.Group(context.Background(), g.ID)
	if got.CostPoints != 0 {
		t.Fatalf("cost = %d; expected 0 — if this no longer zeroes, the "+
			"hidden field in admin_ranks.html is redundant and can go", got.CostPoints)
	}
}
