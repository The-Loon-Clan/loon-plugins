package openlibrary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Release names are not scene-named for books. These are the shapes that
// actually appear, including the one whose deletion started this work.
func TestParseReleaseName(t *testing.T) {
	cases := []struct {
		raw        string
		wantTitle  string
		wantAuthor string
		wantYear   int
	}{
		{"Blackthorn - J.T. Geissinger.epub", "Blackthorn - J.T. Geissinger", "Blackthorn", 0},
		{"Some Author - A Novel (2019) [retail epub]", "Some Author - A Novel", "Some Author", 2019},
		{"The_Hobbit_1937", "The Hobbit", "", 1937},
		{"Dune [EPUB][MOBI]", "Dune", "", 0},
		{"Neuromancer 1st Edition mobi", "Neuromancer", "", 0},
		{"Project Hail Mary - Andy Weir - Unabridged Audiobook", "Project Hail Mary - Andy Weir", "Project Hail Mary", 0},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			q := ParseReleaseName(c.raw)
			if q.Title != c.wantTitle {
				t.Errorf("Title = %q, want %q", q.Title, c.wantTitle)
			}
			if q.Author != c.wantAuthor {
				t.Errorf("Author = %q, want %q", q.Author, c.wantAuthor)
			}
			if q.Year != c.wantYear {
				t.Errorf("Year = %d, want %d", q.Year, c.wantYear)
			}
		})
	}
}

// A source with no credential must still be constructible — that is its whole
// reason for existing, since every other source registers as unconfigured.
func TestNewNeverReturnsNil(t *testing.T) {
	if New("") == nil {
		t.Fatal("New(\"\") returned nil — the keyless source must always register")
	}
	if got := New("").Domain().Key; got != "book" {
		t.Errorf("Domain().Key = %q, want book", got)
	}
}

func fakeOL(t *testing.T, docs []doc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent — Open Library asks clients to identify themselves")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"docs": docs})
	}))
}

func TestSearchBuildsAnEntry(t *testing.T) {
	srv := fakeOL(t, []doc{{
		Key: "/works/OL45804W", Title: "Blackthorn", AuthorName: []string{"J.T. Geissinger"},
		FirstPublishYear: 2019, CoverID: 8231856, ISBN: []string{"9781234567890"},
		Subject: []string{"Romance", "Fiction", "Suspense"}, EditionCount: 3,
		Language: []string{"eng"},
	}})
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "Blackthorn - J.T. Geissinger.epub")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.Title != "Blackthorn" {
		t.Errorf("Title = %q", e.Title)
	}
	if e.Year != 2019 {
		t.Errorf("Year = %d", e.Year)
	}
	// The cover must come from the cover-ID form, which the docs say is the one
	// that is NOT rate-limited (ISBN lookups are, at 100/IP/5min).
	if want := "https://covers.openlibrary.org/b/id/8231856-L.jpg"; e.CoverURL != want {
		t.Errorf("CoverURL = %q, want %q", e.CoverURL, want)
	}
	if len(e.External) != 1 || e.External[0].Namespace != "openlibrary" || e.External[0].Value != "OL45804W" {
		t.Errorf("External = %+v, want openlibrary/OL45804W", e.External)
	}
	if e.Fields["author"] != "J.T. Geissinger" {
		t.Errorf("author field = %v", e.Fields["author"])
	}
	if len(e.Genres) != 3 {
		t.Errorf("Genres = %v", e.Genres)
	}
}

// No match is not an error. The scraper's contract treats a returned error as
// a failure worth reporting; "this release is not a known book" is normal.
func TestSearchNoResultsIsNotAnError(t *testing.T) {
	srv := fakeOL(t, nil)
	defer srv.Close()
	e, ok, err := New(srv.URL).Search(context.Background(), "Some Unknown Thing")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Errorf("ok = true with no docs, entry %+v", e)
	}
}

// The author is the strongest hint a book posting carries: a bare title is far
// more ambiguous than a film's. A result whose author appears in the release
// name must win over Open Library's own first hit.
func TestSearchPrefersTheAuthorNamedInTheRelease(t *testing.T) {
	srv := fakeOL(t, []doc{
		{Key: "/works/OL1W", Title: "Blood Ties", AuthorName: []string{"Someone Else"}, FirstPublishYear: 1990},
		{Key: "/works/OL2W", Title: "Blood Ties", AuthorName: []string{"Kay Hooper"}, FirstPublishYear: 2010},
	})
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "Blood Ties - Kay Hooper.epub")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.External[0].Value != "OL2W" {
		t.Errorf("picked %q, want OL2W — the author named in the release should win", e.External[0].Value)
	}
}

// Initials are spaced differently by cataloguers and posters. Verified against
// the live API: Open Library holds "J. T. Geissinger" for the very release
// ("Blackthorn - J.T. Geissinger") this work started from, so a literal compare
// would have failed on the motivating case.
func TestAuthorMatchIgnoresPunctuationAndSpacing(t *testing.T) {
	srv := fakeOL(t, []doc{
		{Key: "/works/OLwrongW", Title: "Blackthorn", AuthorName: []string{"Someone Else"}},
		{Key: "/works/OLrightW", Title: "Blackthorn", AuthorName: []string{"J. T. Geissinger"}},
	})
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "Blackthorn - J.T. Geissinger.epub")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.External[0].Value != "OLrightW" {
		t.Errorf("picked %q, want OLrightW — spaced vs tight initials must still match", e.External[0].Value)
	}
	for _, c := range []struct{ a, b string }{
		{"J.T. Geissinger", "J. T. Geissinger"},
		{"Kay Hooper", "kay hooper"},
		{"O'Brien", "OBrien"},
	} {
		if foldName(c.a) != foldName(c.b) {
			t.Errorf("foldName(%q)=%q != foldName(%q)=%q", c.a, foldName(c.a), c.b, foldName(c.b))
		}
	}
}

// Subjects run to hundreds per work on Open Library; a release page cannot show
// them all and the tail is cataloguing minutiae.
func TestSubjectsAreCapped(t *testing.T) {
	many := make([]string, 40)
	for i := range many {
		many[i] = "subject"
	}
	srv := fakeOL(t, []doc{{Key: "/works/OL9W", Title: "X", Subject: many}})
	defer srv.Close()
	e, _, _ := New(srv.URL).Search(context.Background(), "X")
	if len(e.Genres) > 8 {
		t.Errorf("Genres = %d entries, want <= 8", len(e.Genres))
	}
}

// The request must ask for a bounded field set: search.json returns a very
// large document per result otherwise, and this runs once per release.
func TestSearchRequestsABoundedFieldSet(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"docs": []doc{}})
	}))
	defer srv.Close()
	_, _, _ = New(srv.URL).Search(context.Background(), "Anything")
	if !strings.Contains(got, "fields=") {
		t.Errorf("query %q carries no fields= limit", got)
	}
	if !strings.Contains(got, "limit=") {
		t.Errorf("query %q carries no limit=", got)
	}
	q, _ := url.ParseQuery(got)
	if q.Get("q") == "" {
		t.Errorf("query %q carries no q=", got)
	}
}

// An audiobook posting is a request thread's subject line, not a scene name.
// It carries whatever the poster and the requester said to each other, and the
// title is somewhere in the middle.
//
// Measured over the 5,117 audiobook releases on the reference index: 79% carry
// an "Author - Title" core, 44% of those also carry part bookkeeping, and the
// cleanups below take the corpus from 61% to 97% cleanly parsed.
func TestAudiobookPostingsAreStrippedToTheBook(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		// The conversation in front.
		{"per req - Brad Meltzer - The Inner Circle 04-12 NMR", "Brad Meltzer - The Inner Circle"},
		{"NR Jeffery Deaver - The Burning Wire 08-01", "Jeffery Deaver - The Burning Wire"},
		{"RC_16-36 - John Sandford - Rough Country - 304.73 MB", "John Sandford - Rough Country"},
		{"New 2025 Marie Benedict - The Queens Of Crime 7-04", "Marie Benedict - The Queens Of Crime"},
		// And the bookkeeping after it, including two markers at once.
		{"Laurell K Hamilton - Obsidian Butterfly 03of16 NMR", "Laurell K Hamilton - Obsidian Butterfly"},
		{"Randy Wayne White - Sanibel Flats 14 - 25 Ch 13 NMR 64K", "Randy Wayne White - Sanibel Flats 14 - 25"},
	} {
		if got := ParseReleaseName(tc.raw).Title; got != tc.want {
			t.Errorf("ParseReleaseName(%q)\n  got  %q\n  want %q", tc.raw, got, tc.want)
		}
	}
}

// A posting that names a GENRE is not a book, and this is the dangerous case:
// "hardboiled mystery" is a perfectly good Open Library query that returns a
// real book with a real cover, which would then be attached to a release it has
// nothing to do with. Refusing means no cover, which is the better error.
func TestGenreOnlyPostingsAreRefused(t *testing.T) {
	for _, raw := range []string{
		"hardboiled mystery",
		"historical mysteries",
		"Genres: Romance, Children's Fiction",
		"Mystery, Thriller",
	} {
		if got := ParseReleaseName(raw).Title; got != "" {
			t.Errorf("ParseReleaseName(%q) = %q — a genre would be searched as a book", raw, got)
		}
	}
	// A real book whose title merely CONTAINS a genre word must still parse.
	for _, raw := range []string{
		"Agatha Christie - A Caribbean Mystery",
		"Some Author - The Romance of the Forest",
	} {
		if ParseReleaseName(raw).Title == "" {
			t.Errorf("ParseReleaseName(%q) refused a real book", raw)
		}
	}
}
