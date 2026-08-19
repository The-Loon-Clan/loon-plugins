package requests

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

// stubRequestStore implements only the methods a test exercises. The embedded
// nil interface satisfies the type while making any UNEXPECTED call panic —
// which is what we want from a fake: a handler that quietly starts reading
// something else should fail loudly rather than pass on a zero value.
type stubRequestStore struct {
	RequestStore

	backlog      []*Request
	backlogTotal int
	counts       map[string]int
	countsErr    error

	requeueMoved bool
	requeueErr   error
	requeuedID   int64
	requeuedBy   int
	requeueCalls int

	// The origin split. openScopes records every scope the handler asked
	// for, in order — the assertions are mostly about WHICH cut of the
	// queue a tab reads, which is invisible in the rendered HTML.
	openScopes  []Scope
	openRows    map[Scope][]*Request
	openTotal   map[Scope]int
	openErr     error
	scopeCounts map[Scope]int
	countsErr2  error
}

func (s *stubRequestStore) ListOpenRequests(ctx context.Context, scope Scope, limit, offset int) ([]*Request, int, error) {
	s.openScopes = append(s.openScopes, scope)
	if s.openErr != nil {
		return nil, 0, s.openErr
	}
	total, ok := s.openTotal[scope]
	if !ok {
		total = len(s.openRows[scope])
	}
	return s.openRows[scope], total, nil
}

func (s *stubRequestStore) OpenRequestCounts(ctx context.Context) (map[Scope]int, error) {
	return s.scopeCounts, s.countsErr2
}

func (s *stubRequestStore) GetBacklogRequests(ctx context.Context, limit, offset int) ([]*Request, int, error) {
	return s.backlog, s.backlogTotal, nil
}

func (s *stubRequestStore) BacklogCounts(ctx context.Context) (map[string]int, error) {
	return s.counts, s.countsErr
}

func (s *stubRequestStore) RequeueBacklogRequest(ctx context.Context, id int64, userID int) (bool, error) {
	s.requeueCalls++
	s.requeuedID, s.requeuedBy = id, userID
	return s.requeueMoved, s.requeueErr
}

// GetRequestPrioritiesBatch is called by attachRequestPriorities on the
// listing path.
func (s *stubRequestStore) GetRequestPrioritiesBatch(ctx context.Context, ids []int64) (map[int64][]RequestPriority, error) {
	return nil, nil
}

type stubErrs struct{ ops []string }

func (e *stubErrs) Report(ctx context.Context, op string, err error) {
	if err != nil {
		e.ops = append(e.ops, op)
	}
}
func (e *stubErrs) HandlerError(c *gin.Context, op string, err error) { e.Report(c, op, err) }

// requeueHandler builds a Handlers wired to the stub, plus the recorder the
// redirect lands in.
func requeueHandler(t *testing.T, store *stubRequestStore, viewer *Viewer) (*Handlers, *stubErrs) {
	t.Helper()
	errs := &stubErrs{}
	h := &Handlers{
		deps: Deps{
			Requests: store,
			Viewer:   func(c *gin.Context) *Viewer { return viewer },
		},
		errs: errs,
	}
	return h, errs
}

func postRequeue(t *testing.T, h *Handlers, id, page string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"page": {page}}
	c.Request = httptest.NewRequest("POST", "/community/requests/"+id+"/requeue",
		strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.RequeueRequest(c)
	// gin buffers the status until the body is written or the engine flushes
	// after the chain. A POST redirect has no body, and CreateTestContext has
	// no engine, so without this the recorder reports 200 for a real 302.
	c.Writer.WriteHeaderNow()
	return w
}

// ── Requeue ─────────────────────────────────────────────────────────────────

func TestRequeueRedirectsWithTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		moved    bool
		err      error
		wantIn   string
		wantErrs int
	}{
		{"moved", true, nil, "requeued=12", 0},
		// Not an error: two members clicking at once is normal, and the
		// second must be told "already back", not shown a failure.
		{"already back", false, nil, "requeue=already", 0},
		// The opposite: a silent failure here is indistinguishable from the
		// button doing nothing, and the member has no other signal.
		{"store failed", false, errors.New("boom"), "requeue=error", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubRequestStore{requeueMoved: tc.moved, requeueErr: tc.err}
			h, errs := requeueHandler(t, store, &Viewer{ID: 7, Username: "tester"})

			w := postRequeue(t, h, "12", "1")
			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}
			loc := w.Header().Get("Location")
			if !strings.Contains(loc, tc.wantIn) {
				t.Errorf("Location = %q, want it to contain %q", loc, tc.wantIn)
			}
			if !strings.Contains(loc, "tab=backlog") {
				t.Errorf("Location = %q — the reader was not returned to the backlog", loc)
			}
			if len(errs.ops) != tc.wantErrs {
				t.Errorf("reported %v, want %d error(s)", errs.ops, tc.wantErrs)
			}
			if store.requeuedID != 12 || store.requeuedBy != 7 {
				t.Errorf("store saw id=%d user=%d, want 12/7", store.requeuedID, store.requeuedBy)
			}
		})
	}
}

// An anonymous POST must not reach the store at all: requeued_by_id would be
// 0, which records "somebody asked" while naming nobody.
func TestRequeueIgnoresAnonymous(t *testing.T) {
	store := &stubRequestStore{requeueMoved: true}
	h, _ := requeueHandler(t, store, nil)

	w := postRequeue(t, h, "12", "1")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if store.requeueCalls != 0 {
		t.Errorf("anonymous POST reached the store %d time(s)", store.requeueCalls)
	}
}

// The page number ends up in a Location header, so it is re-parsed rather than
// echoed. Page 1 is the default and adds nothing to the URL.
func TestRequeueCarriesOnlyASanePage(t *testing.T) {
	for _, tc := range []struct{ page, want string }{
		{"4", "page=4"},
		{"1", ""},
		{"0", ""},
		{"-3", ""},
		{"abc", ""},
		{"1\r\nX-Injected: 1", ""},
	} {
		store := &stubRequestStore{requeueMoved: true}
		h, _ := requeueHandler(t, store, &Viewer{ID: 7})
		loc := postRequeue(t, h, "12", tc.page).Header().Get("Location")
		if tc.want == "" {
			if strings.Contains(loc, "page=") {
				t.Errorf("page=%q produced %q, want no page parameter", tc.page, loc)
			}
			continue
		}
		if !strings.Contains(loc, tc.want) {
			t.Errorf("page=%q produced %q, want %q", tc.page, loc, tc.want)
		}
	}
}

// ── Reason labels ───────────────────────────────────────────────────────────

// Every slug the host's sweep can write must resolve to a label, and anything
// else must land somewhere rather than render blank. A reason nobody can read
// is worse than a vague one.
func TestBacklogLabelCoversEveryStoredSlug(t *testing.T) {
	for _, slug := range []string{"attempted", "available", "stale"} {
		if got := BacklogLabel(slug); got.Slug != slug {
			t.Errorf("BacklogLabel(%q) = %q, want the matching row", slug, got.Slug)
		}
	}
	for _, slug := range []string{"", "something-new"} {
		got := BacklogLabel(slug)
		if got.Label == "" || got.Hint == "" {
			t.Errorf("BacklogLabel(%q) rendered empty — the pill would be blank", slug)
		}
	}
}

// ── Render ──────────────────────────────────────────────────────────────────

func backlogRequest(id int64, reason string, requeues int) *Request {
	r := sampleRequest(id)
	shelved := time.Now().Add(-48 * time.Hour)
	r.BackloggedAt = &shelved
	r.BacklogReason = reason
	r.RequeueCount = requeues
	return r
}

func backlogPageData() gin.H {
	d := listPageData("backlog")
	d["Requests"] = []*Request{
		backlogRequest(1, "attempted", 0),
		backlogRequest(2, "available", 2),
		backlogRequest(3, "stale", 0),
	}
	d["BacklogTotal"] = 7151
	d["BacklogSummary"] = []backlogSummaryRow{
		{BacklogReason: BacklogLabel("attempted"), Count: 2846},
		{BacklogReason: BacklogLabel("available"), Count: 1118},
		{BacklogReason: BacklogLabel("stale"), Count: 3187},
	}
	d["RequeuedID"] = int64(0)
	d["RequeueState"] = ""
	return d
}

func TestBacklogTabRenders(t *testing.T) {
	out := testRender(t, "community_requests.html", backlogPageData())

	for _, want := range []string{
		"Tried, failed",       // per-row reason pills
		"May already exist",   //
		"No movement",         //
		"Ask for it back",     // the action
		"asked back 2",        // a request that stalled twice
		"tab=backlog",         // the tab link
		"7151",                // the badge
		"2846",                // the summary strip
		"Nothing was deleted", // the promise the page is making
	} {
		if !strings.Contains(out, want) {
			t.Errorf("backlog tab missing %q", want)
		}
	}

	// The create form belongs to the open tab. Offering "+ New Request"
	// beside a shelved request invites the duplicate this tab prevents.
	if strings.Contains(out, "+ New Request") {
		t.Error("backlog tab rendered the create-request button")
	}
	// No grid view here, so the toggle must not render either: the pre-paint
	// script hides #view-table when the stored pref is 'grid', and a tab with
	// no #view-grid would come up BLANK for anyone who ever clicked Covers.
	if strings.Contains(out, `id="toggle-view"`) {
		t.Error("backlog tab rendered the cover/list toggle it cannot honour")
	}
	if strings.Contains(out, `id="view-grid"`) {
		t.Error("backlog tab rendered a grid view — then the toggle should be back too")
	}
}

// Each outcome of a requeue click has to say something different, or the
// button reads as broken on every path but the happy one.
func TestBacklogBannersRenderPerOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
		requeued          int64
	}{
		{"moved", "", "Back in the queue", 55},
		{"already", "already", "already back in the queue", 0},
		{"error", "error", "did not go through", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := backlogPageData()
			d["RequeuedID"] = tc.requeued
			d["RequeueState"] = tc.state
			out := testRender(t, "community_requests.html", d)
			if !strings.Contains(out, tc.want) {
				t.Errorf("banner missing %q", tc.want)
			}
		})
	}
}

// Anonymous visitors read the backlog (it explains where their old request
// went) but cannot act — the button would be a 302 to login with the click
// lost, which is worse than an honest prompt.
func TestBacklogHidesTheActionFromAnonymous(t *testing.T) {
	d := backlogPageData()
	d["ViewerID"] = 0
	out := testRender(t, "community_requests.html", d)
	if strings.Contains(out, "Ask for it back") {
		t.Error("anonymous visitor was offered a button that cannot work")
	}
	if !strings.Contains(out, "Sign in to ask") {
		t.Error("anonymous visitor got no way in")
	}
	if !strings.Contains(out, "Tried, failed") {
		t.Error("anonymous visitor lost the reasons too — the page's whole point")
	}
}

func TestBacklogEmptyStateRenders(t *testing.T) {
	d := backlogPageData()
	d["Requests"] = []*Request{}
	d["BacklogSummary"] = nil
	d["BacklogTotal"] = 0
	out := testRender(t, "community_requests.html", d)
	if !strings.Contains(out, "Nothing in the backlog") {
		t.Error("empty backlog did not render its empty state")
	}
	// A zero badge is noise: {{with}} treats 0 as absent, which is the
	// behaviour the other tabs rely on when the count query fails.
	if strings.Contains(out, `<span class="tab-count">0</span>`) {
		t.Error("rendered a zero tab badge")
	}
}
