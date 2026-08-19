package achievements

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The visibility opt-out, end to end inside the plugin: the page that sets it,
// the action that stores it, and the card that obeys it.
//
// The rule under test is one sentence — a member's badges are public unless
// they said otherwise, and their own view is never affected — and it is worth
// pinning at every layer because each layer can break it differently: the
// action can store the wrong direction, the card can consult it for the wrong
// viewer, and the page can render a checkbox that does not reflect what is
// stored.

func init() { gin.SetMode(gin.TestMode) }

// pageFixture builds the plugin with a member signed in (0 = anonymous).
func pageFixture(t *testing.T, signedInAs int64) (*Plugin, *MemStore) {
	t.Helper()
	m := NewMemStore()
	p := &Plugin{store: m}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	p.core = &core.Core{Auth: core.NewAuth(core.AuthAdapter{
		CurrentUserFn: func(*gin.Context) (*core.User, bool) {
			if signedInAs == 0 {
				return nil, false
			}
			return &core.User{ID: signedInAs, Username: "tester"}, true
		},
	})}
	return p, m
}

// earn gives userID one completed achievement, so the card has something to
// render and an empty card cannot be mistaken for a hidden one.
func earn(t *testing.T, m *MemStore, userID int64) {
	t.Helper()
	d := m.SeedAchievement(AchievementDef{
		Slug: "first-upload", Name: "First Upload", Metric: "uploads", Threshold: 1, Enabled: true,
	})
	ctx := context.Background()
	if _, err := m.IncrementProgress(ctx, d.ID, userID, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAchievement(ctx, d.ID, userID, true); err != nil {
		t.Fatal(err)
	}
}

// widgetFor renders the profile card as the fixture's viewer sees it for the
// given subject — the real Render the host calls.
func widgetFor(t *testing.T, p *Plugin, subject int64) string {
	t.Helper()
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/u/badger", nil)
	core.SetViewSubject(gc, subject)
	out, err := p.renderProfileAchievements(gc)
	if err != nil {
		t.Fatalf("render profile card: %v", err)
	}
	return string(out)
}

// A stranger sees the badges by default, and nothing once the member opts out.
func TestProfileCardIsWithheldFromOthersWhenHidden(t *testing.T) {
	p, m := pageFixture(t, 9) // viewer 9 looking at member 5
	earn(t, m, 5)
	ctx := context.Background()

	if out := widgetFor(t, p, 5); !strings.Contains(out, "First Upload") {
		t.Fatalf("a stranger cannot see a member's badges by default: %q", out)
	}
	if err := m.SetProfileHidden(ctx, 5, true); err != nil {
		t.Fatal(err)
	}
	if out := widgetFor(t, p, 5); out != "" {
		t.Errorf("the opt-out did not withhold the card: %q", out)
	}
	// And back: the choice is reversible, not a one-way door.
	if err := m.SetProfileHidden(ctx, 5, false); err != nil {
		t.Fatal(err)
	}
	if out := widgetFor(t, p, 5); !strings.Contains(out, "First Upload") {
		t.Error("opting back in did not restore the card")
	}
}

// The member always sees their own, wherever the card is mounted. Hiding a
// member's badges from themselves would leave them nothing to decide about.
func TestProfileCardAlwaysShowsToTheSubject(t *testing.T) {
	p, m := pageFixture(t, 5) // viewer 5 looking at their own profile
	earn(t, m, 5)
	if err := m.SetProfileHidden(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}
	if out := widgetFor(t, p, 5); !strings.Contains(out, "First Upload") {
		t.Errorf("the member cannot see their own hidden badges: %q", out)
	}
}

// An anonymous reader is not the subject, so the opt-out covers them too.
func TestProfileCardIsWithheldFromAnonymousWhenHidden(t *testing.T) {
	p, m := pageFixture(t, 0)
	earn(t, m, 5)
	if out := widgetFor(t, p, 5); !strings.Contains(out, "First Upload") {
		t.Fatalf("an anonymous reader cannot see public badges: %q", out)
	}
	if err := m.SetProfileHidden(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}
	if out := widgetFor(t, p, 5); out != "" {
		t.Errorf("an anonymous reader saw hidden badges: %q", out)
	}
}

// countingStore reports how often the achievements themselves were read.
type countingStore struct {
	*MemStore
	reads int
}

func (c *countingStore) Achievements(ctx context.Context, userID int64) ([]Achievement, error) {
	c.reads++
	return c.MemStore.Achievements(ctx, userID)
}

// The gate runs BEFORE the read. A member who said "do not publish this" should
// not have it queried either — filtering after the fact still asks.
func TestHiddenProfileIsNotEvenRead(t *testing.T) {
	p, m := pageFixture(t, 9)
	earn(t, m, 5)
	if err := m.SetProfileHidden(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}
	counting := &countingStore{MemStore: m}
	p.store = counting

	if out := widgetFor(t, p, 5); out != "" {
		t.Fatalf("card rendered despite the opt-out: %q", out)
	}
	if counting.reads != 0 {
		t.Errorf("the hidden member's achievements were read %d time(s)", counting.reads)
	}
}

// postVisibility drives the action the way the host mounts it.
func postVisibility(t *testing.T, p *Plugin, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/p/achievements/visibility",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	gc.Request = req
	if _, err := p.actionSetVisibility(gc); err != nil {
		t.Fatalf("action: %v", err)
	}
	// gin buffers the status and flushes it when the engine finishes the
	// request; calling a handler directly skips that, and the recorder would
	// read 200 for a redirect that was in fact issued.
	gc.Writer.WriteHeaderNow()
	return rec
}

func TestVisibilityActionStoresBothDirections(t *testing.T) {
	p, m := pageFixture(t, 5)
	ctx := context.Background()

	rec := postVisibility(t, p, url.Values{"hidden": {"1"}})
	if hidden, _ := m.ProfileHidden(ctx, 5); !hidden {
		t.Error("ticking the box did not hide the member")
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 — a form POST must redirect, not re-render", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/p/achievements?msg=") {
		t.Errorf("Location = %q, want a message back on the page", loc)
	}

	// An ABSENT checkbox means shown. That is what an unticked box posts, and
	// what a stale form posts — both must read as the default.
	postVisibility(t, p, url.Values{})
	if hidden, _ := m.ProfileHidden(ctx, 5); hidden {
		t.Error("submitting the form with the box unticked left the member hidden")
	}
}

// The view is Public so visitors can read the catalogue, and the host's
// site-page group only SOFT-authenticates — so the action is reachable with no
// session at all and has to refuse for itself.
func TestVisibilityActionRefusesAnonymous(t *testing.T) {
	p, m := pageFixture(t, 0)
	if err := m.SetProfileHidden(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}

	rec := postVisibility(t, p, url.Values{"hidden": {"0"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("anonymous POST got %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	if hidden, _ := m.ProfileHidden(context.Background(), 5); !hidden {
		t.Error("an anonymous POST changed a member's stored choice")
	}
}

// renderPage runs the real page Render, which is the only way to catch a
// template referencing a field the view model does not have — html/template
// streams, so that truncates the page mid-write and still returns nil.
func renderPage(t *testing.T, p *Plugin) string {
	t.Helper()
	return renderPageWithQuery(t, p, "")
}

func renderPageWithQuery(t *testing.T, p *Plugin, query string) string {
	t.Helper()
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	url := "/p/achievements"
	if query != "" {
		url += "?" + query
	}
	gc.Request = httptest.NewRequest(http.MethodGet, url, nil)
	out, err := p.renderMemberPage(gc)
	if err != nil {
		t.Fatalf("render page: %v", err)
	}
	return string(out)
}

// A save that failed says so, under EITHER marker.
//
// The action's own refusals redirect with ?err=<text>. An action that returns
// an error never gets that far — the host's wrapper catches it and redirects
// with its own bare marker (?error=1 on this host). Reading only ?err= showed a
// member whose privacy choice failed to save nothing at all, which reads as
// success.
func TestMemberPageShowsAFailedSaveUnderEitherMarker(t *testing.T) {
	p, m := pageFixture(t, 5)
	earn(t, m, 5)

	if page := renderPageWithQuery(t, p, "err=Nope"); !strings.Contains(page, "Nope") {
		t.Error("the page dropped the action's own error text")
	}
	page := renderPageWithQuery(t, p, "error=1")
	if !strings.Contains(page, "did not save") {
		t.Error("a save the host rejected reported nothing — the member reads that as success")
	}
	// And a clean load says neither.
	if clean := renderPage(t, p); strings.Contains(clean, "did not save") {
		t.Error("an ordinary page load claims a failure that did not happen")
	}
}

func TestMemberPageShowsYourCardTheControlAndTheCatalogue(t *testing.T) {
	p, m := pageFixture(t, 5)
	earn(t, m, 5)

	page := renderPage(t, p)
	// Your own card, from the same fragment the profile renders.
	if !strings.Contains(page, "First Upload") {
		t.Error("the page does not show the member their own achievements")
	}
	// The catalogue of what anyone can earn — the answer to "how do I get one".
	if !strings.Contains(page, "How to earn it") {
		t.Error("the page does not list what can be earned")
	}
	// The control, unticked, posting where the host mounts the action.
	if !strings.Contains(page, `action="/p/achievements/visibility"`) {
		t.Error("the visibility form is missing")
	}
	if strings.Contains(page, `id="ach-hidden"`) && strings.Contains(page, "checked") {
		t.Error("the box is ticked for a member who never opted out")
	}

	// And it reflects what is stored, rather than always rendering unticked.
	if err := m.SetProfileHidden(context.Background(), 5, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renderPage(t, p), "checked") {
		t.Error("the checkbox does not reflect a stored opt-out")
	}
}

// A member who has earned nothing gets a sentence, not an empty card — and the
// catalogue, which is the useful half for them.
func TestMemberPageWithNothingEarned(t *testing.T) {
	p, m := pageFixture(t, 5)
	m.SeedAchievement(AchievementDef{
		Slug: "first-upload", Name: "First Upload", Metric: "uploads", Threshold: 1, Enabled: true,
	})

	page := renderPage(t, p)
	if !strings.Contains(page, "not earned any achievements yet") {
		t.Error("the page says nothing to a member with no badges")
	}
	if !strings.Contains(page, "First Upload") {
		t.Error("the catalogue is missing, which is the only half that member can use")
	}
}

// A visitor gets the catalogue and no control. The page is Public precisely so
// "what can be earned here" is answerable before signing up.
func TestMemberPageForAnonymousVisitor(t *testing.T) {
	p, m := pageFixture(t, 0)
	m.SeedAchievement(AchievementDef{
		Slug: "first-upload", Name: "First Upload", Metric: "uploads", Threshold: 1, Enabled: true,
	})

	page := renderPage(t, p)
	if !strings.Contains(page, "First Upload") {
		t.Error("a visitor cannot see what the site has to earn")
	}
	if strings.Contains(page, "/p/achievements/visibility") {
		t.Error("a visitor was shown a settings form they cannot use")
	}
	if !strings.Contains(page, "Sign in") {
		t.Error("the page does not tell a visitor how to see their own standing")
	}
}

// A secret achievement must not be published by the new page — the catalogue
// filters it, and this is a second, page-level check that the filter is the one
// being called.
func TestMemberPageDoesNotPublishSecretAchievements(t *testing.T) {
	p, m := pageFixture(t, 5)
	m.SeedAchievement(AchievementDef{
		Slug: "secret", Name: "Secret Handshake", Trigger: "x", Enabled: true, Hidden: true,
	})
	if page := renderPage(t, p); strings.Contains(page, "Secret Handshake") {
		t.Error("the page listed a hidden achievement, which is what makes it not secret")
	}
}
