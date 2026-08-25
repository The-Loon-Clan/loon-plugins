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
		// Single-segment postings: the whole string is the title, no author.
		{"The_Hobbit_1937", "The Hobbit", "", 1937},
		{"Dune [EPUB][MOBI]", "Dune", "", 0},
		{"Neuromancer 1st Edition mobi", "Neuromancer", "", 0},
		// Two segments: the FIRST is reported as the author because that is the
		// dominant shape on this index, but the reported split is only a
		// best-guess label — TestBothOrientationsAreTried covers what is
		// actually put to the API.
		{"Some Author - A Novel (2019) [retail epub]", "A Novel", "Some Author", 2019},
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
	// Structured title=, not free-text q=. Measured against the live API:
	// q="Ken Follett - The Pillars Of The Earth 02" returns zero results where
	// title=+author= returns the book at rank 1.
	q, _ := url.ParseQuery(got)
	if q.Get("title") == "" {
		t.Errorf("query %q carries no title=", got)
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
	for _, tc := range []struct{ raw, wantTitle, wantAuthor string }{
		// The conversation in front.
		{"per req - Brad Meltzer - The Inner Circle 04-12 NMR", "The Inner Circle", "Brad Meltzer"},
		{"NR Jeffery Deaver - The Burning Wire 08-01", "The Burning Wire", "Jeffery Deaver"},
		{"RC_16-36 - John Sandford - Rough Country - 304.73 MB", "Rough Country", "John Sandford"},
		{"New 2025 Marie Benedict - The Queens Of Crime 7-04", "The Queens Of Crime", "Marie Benedict"},
		// And the bookkeeping after it, including two markers at once.
		{"Laurell K Hamilton - Obsidian Butterfly 03of16 NMR", "Obsidian Butterfly", "Laurell K Hamilton"},
		{"Randy Wayne White - Sanibel Flats 14 - 25 Ch 13 NMR 64K", "Sanibel Flats", "Randy Wayne White"},
	} {
		q := ParseReleaseName(tc.raw)
		if q.Title != tc.wantTitle || q.Author != tc.wantAuthor {
			t.Errorf("ParseReleaseName(%q)\n  got  title=%q author=%q\n  want title=%q author=%q",
				tc.raw, q.Title, q.Author, tc.wantTitle, tc.wantAuthor)
		}
	}
}

// The author is not always first. This index holds "Ken Follett - The Pillars
// Of The Earth" AND "Blackthorn - J.T. Geissinger", and nothing in either
// string says which half is the person. So the orientation is not guessed: the
// search walks both, and this asserts the RIGHT pair is among the ones tried.
func TestBothOrientationsAreTried(t *testing.T) {
	for _, tc := range []struct{ raw, title, author string }{
		{"Ken Follett - The Pillars Of The Earth 02", "The Pillars Of The Earth", "Ken Follett"},
		{"Blackthorn - J.T. Geissinger.epub", "Blackthorn", "J.T. Geissinger"},
		{"Project Hail Mary - Andy Weir - Unabridged Audiobook", "Project Hail Mary", "Andy Weir"},
		// Three segments with a series marker in the middle: the book is the
		// LAST one, and no catalogue title contains "Last Templar 02".
		{"Raymond Khoury - Last Templar 02 - The Templar Salvation 06of12 NMR",
			"The Templar Salvation", "Raymond Khoury"},
	} {
		var found bool
		for _, a := range ParseReleaseName(tc.raw).attempts() {
			if a[0] == tc.title && a[1] == tc.author {
				found = true
			}
		}
		if !found {
			t.Errorf("ParseReleaseName(%q).attempts() = %q\n  does not include title=%q author=%q",
				tc.raw, ParseReleaseName(tc.raw).attempts(), tc.title, tc.author)
		}
	}
}

// The medium is announced in front of the author, in several shapes and often
// stacked. Left in place it becomes the author — and "from CD William
// Bernhardt" is a query Open Library answers with nothing, so a correctly
// parsed TITLE still produced no cover.
func TestSourceMediumPrefixesAreStripped(t *testing.T) {
	for _, tc := range []struct{ raw, title, author string }{
		{"New CD rip Tess Gerritsen - The Spy Coast 2-02", "The Spy Coast", "Tess Gerritsen"},
		{"New CD rip Kathy Reichs - Fire And Bones 6-01", "Fire And Bones", "Kathy Reichs"},
		{"CD 2026 Kathy Reichs - Evil Bones 5-4", "Evil Bones", "Kathy Reichs"},
		{"NR from CD William Bernhardt - Criminal Intent 04", "Criminal Intent", "William Bernhardt"},
	} {
		q := ParseReleaseName(tc.raw)
		if q.Title != tc.title || q.Author != tc.author {
			t.Errorf("ParseReleaseName(%q)\n  got  title=%q author=%q\n  want title=%q author=%q",
				tc.raw, q.Title, q.Author, tc.title, tc.author)
		}
	}
}

// The cleanups above are aggressive, so these assert what must NOT change. Each
// is a real book that a slightly greedier rule would damage.
func TestCleanupsDoNotEatRealTitles(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		// A bare "new" would leave "Moon"; the rule requires new+cd/rip/year.
		{"New Moon", "New Moon"},
		// A trailing number is only taken when hyphenated or zero-padded.
		{"Fahrenheit 451", "Fahrenheit 451"},
		{"Slaughterhouse 5", "Slaughterhouse 5"},
		{"Catch 22", "Catch 22"},
		// A hyphen inside a name has no space on either side, so it is not a
		// segment separator.
		{"Jean-Luc Nancy", "Jean-Luc Nancy"},
	} {
		if got := ParseReleaseName(tc.raw).Title; got != tc.want {
			t.Errorf("ParseReleaseName(%q).Title = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// "Rizzoli #3 - Tess Gerritsen - The Sinner": the series marker took the author
// slot and pushed the author into the title slot, so neither field was what it
// claimed to be.
func TestSeriesMarkerSegmentsAreDropped(t *testing.T) {
	q := ParseReleaseName("Bernie Gunther #6 - Philip Kerr - If the Dead Rise Not - 20of56 NMR")
	if q.Title != "If the Dead Rise Not" || q.Author != "Philip Kerr" {
		t.Errorf("got title=%q author=%q, want title=%q author=%q",
			q.Title, q.Author, "If the Dead Rise Not", "Philip Kerr")
	}
	// But only when the marker is the WHOLE segment. Here the author trails it
	// in the same segment and dropping it would lose the author entirely.
	q = ParseReleaseName("per req Georgina Kincaid #3 Richelle Mead - Succubus Dreams 6of9 NMR")
	if !strings.Contains(q.Author, "Richelle Mead") {
		t.Errorf("author = %q, want it to retain \"Richelle Mead\"", q.Author)
	}
}

// A poster writes "Phil Rickman- The Bones of Avalon" as readily as
// "Ken Follett - The Pillars". Requiring a space on BOTH sides of the dash
// treated the first as one unsplittable string.
func TestDashWithoutASpaceInFrontStillSplits(t *testing.T) {
	q := ParseReleaseName("Phil Rickman- The Bones of Avalon 08of12 NMR")
	if q.Title != "The Bones of Avalon" || q.Author != "Phil Rickman" {
		t.Errorf("got title=%q author=%q, want title=%q author=%q",
			q.Title, q.Author, "The Bones of Avalon", "Phil Rickman")
	}
}

// On an audiobook posting the bracket is frequently the TITLE, not decoration.
// Discarding it left the author standing alone as the entire query.
func TestBracketedTitleIsSearched(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"Mike Thompson ( Wolf Point ) WP-CD-08", "Wolf Point"},
		{"Lee Child ( 61 Hours ) 61-Hours-CD-03", "61 Hours"},
		{"Fern Michaels ( Return To Sender ) 04", "Return To Sender"},
	} {
		var found bool
		for _, a := range ParseReleaseName(tc.raw).attempts() {
			if a[0] == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("ParseReleaseName(%q).attempts() = %q, none searches title %q",
				tc.raw, ParseReleaseName(tc.raw).attempts(), tc.want)
		}
	}
	// Decoration must still be discarded rather than searched as a title.
	for _, raw := range []string{"Dune [EPUB][MOBI]", "Some Author - A Novel (2019) [retail epub]"} {
		if b := ParseReleaseName(raw).bracket; b != "" {
			t.Errorf("ParseReleaseName(%q).bracket = %q, want none", raw, b)
		}
	}
}

// A release with no cover is better than a release wearing the wrong one. Open
// Library's top hit for an author is just "a book by that author", which is how
// "Home Free" landed on a posting for "Return To Sender".
func TestWrongBookIsRefusedEvenWhenTheAuthorMatches(t *testing.T) {
	ds := []doc{{Title: "Home Free", AuthorName: []string{"Fern Michaels"}}}
	if _, ok := pick(ds, "Return To Sender", "Fern Michaels", 0); ok {
		t.Error("accepted a different book by the right author")
	}
	if _, ok := pick(ds, "Home Free", "Fern Michaels", 0); !ok {
		t.Error("refused the book it actually is")
	}
	// Open Library's edition decoration must still agree.
	half := []doc{{Title: "The Pillars of the Earth. 1/2", AuthorName: []string{"Ken Follett"}}}
	if _, ok := pick(half, "The Pillars Of The Earth", "Ken Follett", 0); !ok {
		t.Error("refused a match over Open Library's edition suffix")
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

// TestAYearAsTitleSurvivesTheYearStrip pins a review finding: "1984" as the
// book's title must not be deleted by the edition-year strip, which collapsed
// "George Orwell - 1984" to the author alone.
func TestAYearAsTitleSurvivesTheYearStrip(t *testing.T) {
	q := ParseReleaseName("George Orwell - 1984")
	if len(q.parts) != 2 {
		t.Fatalf("parts = %q, want both the author and the year-title", q.parts)
	}
	found := false
	for _, p := range q.parts {
		found = found || p == "1984"
	}
	if !found {
		t.Fatalf("the title segment %q was stripped; parts = %q", "1984", q.parts)
	}
	// A year DECORATING a segment is still stripped.
	q = ParseReleaseName("The_Hobbit_1937")
	if len(q.parts) != 1 || q.parts[0] != "The Hobbit" {
		t.Fatalf("decoration year must still strip; parts = %q", q.parts)
	}
}
