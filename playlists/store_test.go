package playlists

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon/core"
)

// TestSlugify. The slug is the playlist's address and feeds a UNIQUE
// constraint, so the rule and its edges are worth pinning rather than
// rediscovering from a constraint violation in a log.
func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"My Best Encodes":   "my-best-encodes",
		"  padded  ":        "padded",
		"Multiple   spaces": "multiple-spaces",
		"UPPER Case":        "upper-case",
		"already-a-slug":    "already-a-slug",
		"2160p Remuxes":     "2160p-remuxes",
		"C++ & Friends":     "c-friends",
		"...leading dots":   "leading-dots",
		"trailing dots...":  "trailing-dots",
		"Ægir's list":       "gir-s-list", // non-ASCII is dropped, not transliterated
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSlugifyIsAlwaysAUsableAddress. Every branch has to produce something a
// URL can hold: no leading or trailing dash, no run of dashes, at most 60
// characters, and never empty — a playlist with no address cannot be opened.
func TestSlugifyIsAlwaysAUsableAddress(t *testing.T) {
	for _, in := range []string{
		"", "   ", "???", "!!!", "...", "日本語だけ",
		"---", "a---b", "...both...",
		strings.Repeat("Very long playlist name ", 20),
		strings.Repeat("x", 200),
	} {
		got := slugify(in)
		if got == "" {
			t.Errorf("slugify(%q) = %q — a playlist with no address cannot be opened", in, got)
			continue
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("slugify(%q) = %q — dangling dash", in, got)
		}
		if strings.Contains(got, "--") {
			t.Errorf("slugify(%q) = %q — doubled dash", in, got)
		}
		if len(got) > 60 {
			t.Errorf("slugify(%q) is %d characters, want at most 60", in, len(got))
		}
		if strings.TrimLeft(got, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			t.Errorf("slugify(%q) = %q — not URL-safe", in, got)
		}
	}
}

// TestSlugifyFallsBackRatherThanCollide. A name with nothing usable in it still
// needs an address, and two of them made in the same run must not be the same
// address — the UNIQUE constraint would reject the second and the member would
// see a failure for a name that looked fine to them.
func TestSlugifyFallsBackForANameWithNothingInIt(t *testing.T) {
	got := slugify("???")
	if !strings.HasPrefix(got, "list-") {
		t.Errorf("slugify(%q) = %q, want the list- fallback", "???", got)
	}
}

// ── the ownership gate ──────────────────────────────────────────────

type fakeStore struct {
	Store // the rest of the interface is never reached from owned()

	playlist *Playlist
	err      error
	askedFor string
}

func (f *fakeStore) BySlug(_ context.Context, slug string) (*Playlist, error) {
	f.askedFor = slug
	return f.playlist, f.err
}

type fakeAuth struct {
	core.AuthService // only CurrentUser is called

	user *core.User
}

func (f *fakeAuth) CurrentUser(*gin.Context) (*core.User, bool) {
	return f.user, f.user != nil
}

func request(t *testing.T, h *Handlers, slug string) (*Playlist, bool, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/playlists/"+slug, nil)
	c.Params = gin.Params{{Key: "slug", Value: slug}}
	p, ok := h.owned(c)
	return p, ok, rec
}

// TestOwnedAnswers404ForSomebodyElsesPlaylist, not 403.
//
// The difference is the whole point and it is stated in the handler: a 403 on a
// slug that exists and a 404 on one that does not is an oracle — walk a list of
// guessed slugs and the status code tells you which ones are real, and a public
// playlist's slug is guessable from its name. The bodies must be identical too,
// not only the codes.
func TestOwnedAnswersTheSameForMissingAndNotYours(t *testing.T) {
	owner := &core.User{ID: 7}
	someoneElse := &core.User{ID: 8}

	notYours := &Handlers{
		store: &fakeStore{playlist: &Playlist{ID: 1, UserID: owner.ID, Slug: "theirs", Public: true}},
		auth:  &fakeAuth{user: someoneElse},
	}
	missing := &Handlers{
		store: &fakeStore{playlist: nil},
		auth:  &fakeAuth{user: someoneElse},
	}

	p1, ok1, rec1 := request(t, notYours, "theirs")
	p2, ok2, rec2 := request(t, missing, "no-such-list")

	if ok1 || p1 != nil {
		t.Error("owned() let somebody through to a playlist they do not own")
	}
	if ok2 || p2 != nil {
		t.Error("owned() returned a playlist that does not exist")
	}
	if rec1.Code != http.StatusNotFound || rec2.Code != http.StatusNotFound {
		t.Errorf("codes %d and %d, want two 404s", rec1.Code, rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Errorf("bodies differ: %q vs %q — that difference is the oracle", rec1.Body.String(), rec2.Body.String())
	}
}

func TestOwnedLetsTheOwnerThrough(t *testing.T) {
	st := &fakeStore{playlist: &Playlist{ID: 1, UserID: 7, Slug: "mine"}}
	h := &Handlers{store: st, auth: &fakeAuth{user: &core.User{ID: 7}}}

	p, ok, rec := request(t, h, "mine")
	if !ok || p == nil || p.ID != 1 {
		t.Fatalf("the owner was refused their own playlist (code %d)", rec.Code)
	}
	if st.askedFor != "mine" {
		t.Errorf("looked up %q, want the slug from the route", st.askedFor)
	}
}

// TestOwnedRefusesAnAnonymousViewer. viewer() returns 0 for anonymous, and 0
// must never match a stored UserID — a playlist row with user_id 0 would
// otherwise be editable by everybody who is not signed in.
func TestOwnedRefusesAnAnonymousViewer(t *testing.T) {
	for _, ownerID := range []int64{0, 7} {
		h := &Handlers{
			store: &fakeStore{playlist: &Playlist{ID: 1, UserID: ownerID, Slug: "mine", Public: true}},
			auth:  &fakeAuth{user: nil},
		}
		if _, ok, rec := request(t, h, "mine"); ok {
			t.Errorf("owner=%d: an anonymous request was treated as the owner (code %d)", ownerID, rec.Code)
		}
	}
}

// TestOwnedTreatsAStoreFailureAsNotFound rather than as permission granted —
// the failure direction matters more than the status here.
func TestOwnedTreatsAStoreFailureAsNotFound(t *testing.T) {
	h := &Handlers{
		store: &fakeStore{err: errors.New("database is down")},
		auth:  &fakeAuth{user: &core.User{ID: 7}},
	}
	if _, ok, rec := request(t, h, "mine"); ok || rec.Code != http.StatusNotFound {
		t.Errorf("ok=%v code=%d — a lookup failure must not open the gate", ok, rec.Code)
	}
}

// ── the cart seam's guards ──────────────────────────────────────────

// TestSinkRefusesNonsenseBeforeTouchingTheDatabase. These early returns are
// what let the rest of the sink assume a real member and a real batch; each one
// also means an anonymous cart POST cannot reach a statement at all.
func TestSinkRefusesNonsenseBeforeTouchingTheDatabase(t *testing.T) {
	// A zero-value sink: any of these reaching the query would nil-panic, which
	// is exactly what makes this test meaningful.
	s := sink{}
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		userID int64
		slug   string
		ids    []int64
	}{
		{"anonymous", 0, "mine", []int64{1}},
		{"a negative id", -1, "mine", []int64{1}},
		{"no slug", 7, "", []int64{1}},
		{"an empty batch", 7, "mine", nil},
		{"an empty slice", 7, "mine", []int64{}},
	} {
		n, err := s.AddToCollection(ctx, tc.userID, tc.slug, tc.ids)
		if n != 0 || err != nil {
			t.Errorf("%s: got (%d, %v), want (0, nil)", tc.name, n, err)
		}
	}

	// Listing is the same shape: an anonymous viewer is offered nothing to
	// write to rather than everybody's playlists.
	for _, id := range []int64{0, -1} {
		got, err := s.CollectionsOf(ctx, id)
		if got != nil || err != nil {
			t.Errorf("CollectionsOf(%d) = (%v, %v), want (nil, nil)", id, got, err)
		}
	}
}
