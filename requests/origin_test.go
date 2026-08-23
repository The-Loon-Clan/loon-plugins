package requests

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The origin split: which requests each tab lists, and what its badge says.
//
// The bug this closes is not subtle once you can see it, and was invisible
// before: on 2026-08-18 production's open queue held 6,420 requests, 6,413 of
// them filed by the torrent feed importer. A member asking for a rare title
// went into the same list, so the board looked busy and was unreadable, and
// nothing anywhere counted the asks that had nobody working them.
//
// These tests are mostly about WHICH cut of the queue a tab reads, because
// that is the part a rendered page cannot show you: a member tab wired to the
// automated scope looks completely normal until you know the rows.

// ── Fixtures ────────────────────────────────────────────────────────────────

// originHandler wires a Handlers over the shared stub with just enough chrome
// for RequestsPage to render. RenderPage captures the fragment so a test can
// assert on what a reader would see.
func originHandler(t *testing.T, store *stubRequestStore) (*Handlers, *stubErrs, *string) {
	t.Helper()
	var captured string
	errs := &stubErrs{}
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{
		deps: Deps{
			Requests:    store,
			FeedItems:   stubFeedStore{},
			AgentTokens: stubAgentTokenStore{},
			Viewer:      func(c *gin.Context) *Viewer { return &Viewer{ID: 7, Username: "tester"} },
			RenderPage: func(c *gin.Context, status int, title string, body template.HTML) {
				captured = string(body)
				c.Status(status)
			},
			RenderPagination: func(page, totalPages int, baseURL string) template.HTML {
				// The base URL is asserted on: pagination that drops ?tab=
				// silently moves the reader to a different list on page 2.
				return template.HTML("<nav data-base=\"" + template.HTMLEscapeString(baseURL) + "\">pager</nav>")
			},
			NzbCardCSS:     func() template.HTML { return "" },
			CSRFToken:      func(c *gin.Context) string { return "test-csrf" },
			Markdown:       func(s string) template.HTML { return template.HTML(template.HTMLEscapeString(s)) },
			UpscaleOptions: func(keys []string) []UpscaleOption { return nil },
			PriorityTypes:  func() []PriorityType { return nil },
		},
		errs: errs,
	}
	return h, errs, &captured
}

type stubFeedStore struct{ FeedItemStore }
type stubAgentTokenStore struct{ AgentTokenStore }

func (stubFeedStore) ListFeedItems(ctx context.Context, f FeedItemFilter) ([]*FeedItem, int, error) {
	return []*FeedItem{{ID: 1, Source: "nyaa", Title: "a feed item", SeenAt: time.Now()}}, 1, nil
}

func (stubAgentTokenStore) ListAvailableUpscaleModels(ctx context.Context) ([]string, error) {
	return nil, nil
}

// getRequestsPage drives RequestsPage over a query string.
func getRequestsPage(t *testing.T, h *Handlers, query string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/community/requests"+query, nil)
	h.RequestsPage(c)
	c.Writer.WriteHeaderNow()
	return w, ""
}

func originRequest(id int64, title, origin string) *Request {
	r := sampleRequest(id)
	r.Title = title
	r.Origin = origin
	if origin != string(ScopeMember) {
		r.Username = "nyaa-bot"
	}
	return r
}

// unsourcedRequest is the shape the whole feature exists for: a member named
// something and left nothing an agent can dispatch against.
func unsourcedRequest(id int64, title string, age time.Duration) *Request {
	r := sampleRequest(id)
	r.Title = title
	r.Origin = string(ScopeMember)
	r.Username = "hopeful"
	r.InfoHash = ""
	r.NyaaURL = ""
	r.NzbID = nil
	r.Notes = "only ever released on a private tracker"
	r.CreatedAt = time.Now().Add(-age)
	return r
}

func scopedStore() *stubRequestStore {
	return &stubRequestStore{
		openRows: map[Scope][]*Request{
			ScopeMember:        {originRequest(1, "a member asked for this", "member")},
			ScopeAutomated:     {originRequest(2, "the importer filed this", "feed")},
			ScopeNeedsSourcing: {unsourcedRequest(3, "rare OVA nobody seeds", 90*24*time.Hour)},
		},
		scopeCounts: map[Scope]int{
			ScopeMember: 7, ScopeAutomated: 6413, ScopeNeedsSourcing: 9,
		},
		counts: map[string]int{"stale": 7151},
	}
}

// ── Which tab reads which scope ─────────────────────────────────────────────

// The default view is the members' one. This is the pin: /community/requests
// with no query at all must read ScopeMember, because every existing link on
// the site points there and the whole change is worthless if the landing page
// still shows the importer's output.
func TestDefaultViewIsMemberFiledOnly(t *testing.T) {
	store := scopedStore()
	h, _, body := originHandler(t, store)

	w, _ := getRequestsPage(t, h, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(store.openScopes) != 1 || store.openScopes[0] != ScopeMember {
		t.Fatalf("default view read %v, want exactly [%s]", store.openScopes, ScopeMember)
	}
	if !strings.Contains(*body, "a member asked for this") {
		t.Error("the member's request is not on the default page")
	}
	if strings.Contains(*body, "the importer filed this") {
		t.Error("an auto-filed request rendered on the default view — the split did nothing")
	}
}

// Each tab word maps to exactly one scope. Table-driven because the mapping is
// the contract: a tab reading the wrong cut renders a perfectly normal page.
func TestEachTabReadsItsOwnScope(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  Scope
		shows string
	}{
		{"", ScopeMember, "a member asked for this"},
		{"?tab=open", ScopeMember, "a member asked for this"},
		{"?tab=automated", ScopeAutomated, "the importer filed this"},
		{"?tab=sourcing", ScopeNeedsSourcing, "rare OVA nobody seeds"},
		// An unknown tab falls back to the members' board rather than
		// erroring or — much worse — serving the unsplit list.
		{"?tab=nonsense", ScopeMember, "a member asked for this"},
	} {
		t.Run("tab="+tc.query, func(t *testing.T) {
			store := scopedStore()
			h, _, body := originHandler(t, store)
			getRequestsPage(t, h, tc.query)
			if len(store.openScopes) != 1 || store.openScopes[0] != tc.want {
				t.Fatalf("%q read %v, want [%s]", tc.query, store.openScopes, tc.want)
			}
			if !strings.Contains(*body, tc.shows) {
				t.Errorf("%q did not render %q", tc.query, tc.shows)
			}
		})
	}
}

// The feed and backlog tabs are unchanged by the split and must not start
// reading the open queue — the backlog has its own listing, and the feed is
// not a request list at all.
func TestFeedAndBacklogDoNotReadTheOpenQueue(t *testing.T) {
	for _, tab := range []string{"?tab=feed", "?tab=backlog"} {
		store := scopedStore()
		store.backlog = []*Request{originRequest(9, "shelved", "feed")}
		store.backlogTotal = 1
		h, _, _ := originHandler(t, store)
		getRequestsPage(t, h, tab)
		if len(store.openScopes) != 0 {
			t.Errorf("%s read the open queue as %v", tab, store.openScopes)
		}
	}
}

// Page 2 of the auto-filed list must still be the auto-filed list. Losing
// ?tab= in the pager quietly teleports the reader to a different queue.
func TestPaginationStaysOnTheTab(t *testing.T) {
	for _, tc := range []struct{ query, wantBase string }{
		{"", "/community/requests?"},
		{"?tab=automated", "/community/requests?tab=automated&amp;"},
		{"?tab=sourcing", "/community/requests?tab=sourcing&amp;"},
	} {
		store := scopedStore()
		h, _, body := originHandler(t, store)
		getRequestsPage(t, h, tc.query)
		if !strings.Contains(*body, `data-base="`+tc.wantBase+`"`) {
			t.Errorf("%q paginated against something other than %q", tc.query, tc.wantBase)
		}
	}
}

// ── Needs sourcing ──────────────────────────────────────────────────────────

// The scope is the storage layer's to define; what the handler owes it is to
// ask for it by name and to render what makes a request findable. A row here
// with no season, no episode and no catalog link is a search; one with them is
// an errand somebody can actually run.
func TestNeedsSourcingRendersWhatMakesARequestFindable(t *testing.T) {
	store := scopedStore()
	row := unsourcedRequest(3, "Oishinbo BD-BOX", 120*24*time.Hour)
	aid := 4242
	row.AnimeID = &aid
	row.Season = "01"
	row.Episodes = "1-23"
	shelved := time.Now().Add(-30 * 24 * time.Hour)
	row.BackloggedAt = &shelved
	store.openRows[ScopeNeedsSourcing] = []*Request{row}

	h, _, body := originHandler(t, store)
	getRequestsPage(t, h, "?tab=sourcing")

	for _, want := range []string{
		"Oishinbo BD-BOX",                   // the title
		"S01",                               // season, and
		"E1-23",                             // episode range — what makes it findable
		"AniDB:4242",                        // the catalog link
		"asked by hopeful",                  // who is waiting
		row.CreatedAt.Format("Jan 02 2006"), // since when
		"shelved",                           // and that it is off the board
		"only ever released on a private tracker", // the member's own note
	} {
		if !strings.Contains(*body, want) {
			t.Errorf("needs-sourcing row is missing %q", want)
		}
	}
}

// A shelved request is still an open request. It has to appear here, because
// the backlog sweep is exactly how the two clearest real examples disappeared
// — swept off the board on the same "stopped moving" rule that shelves a stale
// bot row, months before anybody went looking for them.
func TestNeedsSourcingIncludesShelvedRequests(t *testing.T) {
	store := scopedStore()
	shelvedAt := time.Now().Add(-60 * 24 * time.Hour)
	live := unsourcedRequest(3, "still on the board", 10*24*time.Hour)
	gone := unsourcedRequest(4, "swept off months ago", 200*24*time.Hour)
	gone.BackloggedAt = &shelvedAt
	store.openRows[ScopeNeedsSourcing] = []*Request{gone, live}

	h, _, body := originHandler(t, store)
	getRequestsPage(t, h, "?tab=sourcing")
	for _, want := range []string{"still on the board", "swept off months ago"} {
		if !strings.Contains(*body, want) {
			t.Errorf("needs-sourcing dropped %q", want)
		}
	}
}

// The empty state has to say what empty MEANS here, or a reader concludes the
// tab is broken. "Nothing to go looking for" is a different claim from "no
// requests".
func TestNeedsSourcingEmptyStateExplainsItself(t *testing.T) {
	store := scopedStore()
	store.openRows[ScopeNeedsSourcing] = nil
	h, _, body := originHandler(t, store)
	getRequestsPage(t, h, "?tab=sourcing")
	if !strings.Contains(*body, "Nothing to go looking for") {
		t.Error("an empty sourcing queue rendered no empty state")
	}
	// No create form: this queue exists because the form REFUSES requests
	// without a torrent link, so offering it here would be a dead end.
	if strings.Contains(*body, "+ New Request") {
		t.Error("the sourcing tab offered the create form")
	}
	// And no cover grid — the pre-paint script hides #view-table when the
	// stored pref is 'grid', so a tab with no #view-grid comes up BLANK for
	// anyone who ever clicked Covers. The backlog tab learned this first.
	if strings.Contains(*body, `id="toggle-view"`) {
		t.Error("the sourcing tab rendered a cover toggle it cannot honour")
	}
	if strings.Contains(*body, `id="view-grid"`) {
		t.Error("the sourcing tab rendered a grid view — then the toggle should be back")
	}
}

// ── Badges ──────────────────────────────────────────────────────────────────

// Every tab carries its badge on every tab, and each badge counts the list it
// links to. A number that counts something else is worse than no number.
func TestTabBadgesRenderOnEveryTab(t *testing.T) {
	for _, query := range []string{"", "?tab=automated", "?tab=sourcing", "?tab=backlog", "?tab=feed"} {
		store := scopedStore()
		store.backlogTotal = 7151
		h, _, body := originHandler(t, store)
		getRequestsPage(t, h, query)
		for _, want := range []string{">7<", ">6413<", ">9<"} {
			if !strings.Contains(*body, `<span class="tab-count">`+strings.Trim(want, "<>")+`</span>`) {
				t.Errorf("%q is missing the %s badge", query, want)
			}
		}
	}
}

// A failed count costs a badge, never the listing. These render on every tab,
// so the alternative is one broken query taking five pages down.
func TestBadgeFailureDoesNotTakeThePageDown(t *testing.T) {
	store := scopedStore()
	store.countsErr2 = errors.New("db down")
	store.scopeCounts = nil
	// The backlog badge comes from a different query; silence it too so this
	// asserts about the one that failed.
	store.counts = map[string]int{}
	h, errs, body := originHandler(t, store)

	w, _ := getRequestsPage(t, h, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a missing badge is not a broken page", w.Code)
	}
	if !strings.Contains(*body, "a member asked for this") {
		t.Error("the listing vanished along with the badge")
	}
	if strings.Contains(*body, `<span class="tab-count">`) {
		t.Error("a badge rendered from a failed count")
	}
	if len(errs.ops) == 0 {
		t.Error("the count failure was swallowed without a report")
	}
}

// Zero is absent, not "0". A zero pill beside "Needs Sourcing" reads as a
// queue with an item in it at a glance.
func TestZeroBadgesDoNotRender(t *testing.T) {
	store := scopedStore()
	store.scopeCounts = map[Scope]int{ScopeMember: 0, ScopeAutomated: 0, ScopeNeedsSourcing: 0}
	store.backlogTotal = 0
	store.counts = map[string]int{}
	h, _, body := originHandler(t, store)
	getRequestsPage(t, h, "")
	if strings.Contains(*body, `<span class="tab-count">0</span>`) {
		t.Error("rendered a zero badge")
	}
}

// ── Row-level labelling ─────────────────────────────────────────────────────

// Two of the lists that share the main table mix origins on purpose — the
// backlog holds every kind, and ?anime_id= answers "who else wants this" — so
// each row says whether a person filed it. The label comes off Origin, not off
// the username the importer stamped, which has meant different things over the
// years.
func TestAutomatedRowsAreLabelledInTheList(t *testing.T) {
	store := scopedStore()
	store.openRows[ScopeAutomated] = []*Request{
		originRequest(2, "from the feed", "feed"),
		originRequest(3, "a replacement", "resurrector"),
		originRequest(4, "an archive import", "bulk"),
	}
	h, _, body := originHandler(t, store)
	getRequestsPage(t, h, "?tab=automated")
	for _, want := range []string{"auto: feed", "auto: replacement", "auto: bulk import"} {
		if !strings.Contains(*body, want) {
			t.Errorf("auto-filed list is missing the %q label", want)
		}
	}

	// And a member's row carries no such label, on any tab.
	store2 := scopedStore()
	h2, _, body2 := originHandler(t, store2)
	getRequestsPage(t, h2, "")
	if strings.Contains(*body2, "auto:") {
		t.Error("a member's request was labelled as auto-filed")
	}
}

// Automated() is what the template branches on, so an origin the host invents
// later must land on the automated side by default. That is the safe
// direction: it keeps the members' tab about members without a template edit.
func TestAutomatedTreatsUnknownOriginsAsAutomation(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"", false}, // unstamped reads as a member's, matching the column default
		{"member", false},
		{"feed", true},
		{"resurrector", true},
		{"bulk", true},
		{"some-future-importer", true},
	} {
		r := &Request{Origin: tc.origin}
		if got := r.Automated(); got != tc.want {
			t.Errorf("Origin %q: Automated() = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// ── Failure paths ───────────────────────────────────────────────────────────

// A failed listing is a 500 that gets reported, not a blank page. The old code
// returned the string and told nobody.
func TestOpenListFailureIsReported(t *testing.T) {
	store := scopedStore()
	store.openErr = errors.New("boom")
	h, errs, _ := originHandler(t, store)
	w, _ := getRequestsPage(t, h, "")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if len(errs.ops) == 0 {
		t.Error("a failed listing was not reported")
	}
}
