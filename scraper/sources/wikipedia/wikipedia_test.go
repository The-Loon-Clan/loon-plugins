package wikipedia

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Scene naming puts the year after the title and the packaging after that, so
// the year is the boundary — and a title may contain a year of its own.
func TestParseReleaseName(t *testing.T) {
	cases := []struct {
		raw       string
		wantTitle string
		wantYear  int
	}{
		{"Blade.Runner.2049.2017.1080p.BluRay.x264-GRP", "Blade Runner 2049", 2017},
		{"Everything.Everywhere.All.at.Once.2022.2160p.WEB-DL.DDP5.1.Atmos.HDR.H.265", "Everything Everywhere All at Once", 2022},
		{"Dune.Part.Two.2024.IMAX.WEB-DL", "Dune Part Two", 2024},
		{"The.Matrix.1999.REMASTERED.1080p.BluRay", "The Matrix", 1999},
		{"2001.A.Space.Odyssey.1968.1080p.BluRay.x264", "2001 A Space Odyssey", 1968},
		{"Some Film [REQ] {tag} 2011 HDRip", "Some Film", 2011},
		{"Nineteen.Eighty.Four.1984.DVDRip.XviD", "Nineteen Eighty Four", 1984},
		// No year at all: the whole head is the title.
		{"An.Untitled.Thing.WEB-DL", "An Untitled Thing", 0},

		// A BRACKETED year ends the title. 5,593 of the 24,223 movie releases
		// on the reference index are named this way, and they used to search
		// Wikipedia with the year and everything after it still attached —
		// "Kaali (2023) Tamil 1440p SF" rather than "Kaali".
		{"Kaali (2023) Tamil 1440p SF WEB-DL AAC2.0 H264", "Kaali", 2023},
		{"Macherla Niyojakavargam (2022) 576p ZEE5 WEB-DL", "Macherla Niyojakavargam", 2022},
		{"Haseena Parkar (2019)", "Haseena Parkar", 2019},
		{"The Matrix (1999) REMASTERED 720p", "The Matrix", 1999},
		{"Das Bankentrio (1989) - DVDRiP - Xvid", "Das Bankentrio", 1989},
		// Standard-definition and odd resolutions are packaging too. The list
		// knew only 1080p/2160p/720p/480p, so 1440p and 576p cut in the wrong
		// place — the same gap the categoriser had.
		{"Akkaran.2024.480p.SS.WEB-DL", "Akkaran", 2024},
		{"Jersey.2019.1440p.ZEE5.WEB-DL.DD+5.1.H.265-TR", "Jersey", 2019},
		// A title that IS a year keeps it: the bracket rule must not fire on a
		// bare trailing year, and the release year is the LAST one.
		{"2012.2009.1080p.BluRay.x264", "2012", 2009},

		// Everything after the release year is packaging, named or not. 821
		// movie releases here put a language or a streaming platform between
		// the year and the resolution, which used to stay in the search:
		// "Manmarziyaan 2018 Hindi".
		{"Manmarziyaan.2018.Hindi.1080p.ZEE5.WEB-DL.AAC.2.0.H.264", "Manmarziyaan", 2018},
		{"Aadujeevitham.The.Goat.Life.2024.1080p.NF.WEB-DL.Malayalam", "Aadujeevitham The Goat Life", 2024},
		// A SQUARE-bracketed year is a year. reBracket deletes bracket groups
		// wholesale, which silently ate the release year of 196 releases here.
		{"Chandigarh.Kare.Aashiqui.[2021].1080p.10bit.WEBRip", "Chandigarh Kare Aashiqui", 2021},
		// An indexer's banner stamped on the front, on 87 releases here. Left
		// in place it becomes the first words of the title.
		{"(www.Thunder-News.org) >Bad.Boys.2024.1080p.WEB-DL", "Bad Boys", 2024},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			q := ParseReleaseName(c.raw)
			if q.Title != c.wantTitle {
				t.Errorf("Title = %q, want %q", q.Title, c.wantTitle)
			}
			if q.Year != c.wantYear {
				t.Errorf("Year = %d, want %d", q.Year, c.wantYear)
			}
		})
	}
}

// The description is the whole disambiguation policy. These are real shapes
// taken from live search results.
func TestIsFilm(t *testing.T) {
	for _, yes := range []string{
		"2022 film by Daniel Kwan and Daniel Scheinert",
		"2017 film by Denis Villeneuve",
		"1997 television film",
		"2019 animated film",
		"2020 documentary film directed by someone",
		"Japanese films of 2001", // plural, still a film page
	} {
		if !isFilm(yes) {
			t.Errorf("isFilm(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{
		"",                        // an accolades list or stub
		"American filmmaking duo", // the DIRECTORS — "film" only as a substring
		"2017 soundtrack album by Hans Zimmer",
		"American film series", // a franchise, not a film
		"film trilogy by Peter Jackson",
		"List of accolades received by a film",
		"2015 video game",
		"American actor",
		"Upcoming American sci-fi miniseries",
	} {
		if isFilm(no) {
			t.Errorf("isFilm(%q) = true, want false", no)
		}
	}
}

// A remake shares its title exactly, so the year decides.
func TestPickFilmPrefersTheMatchingYear(t *testing.T) {
	pages := []searchPage{
		{Key: "Dune_1984", Title: "Dune", Description: "1984 film by David Lynch"},
		{Key: "Dune_2021", Title: "Dune", Description: "2021 film by Denis Villeneuve"},
	}
	got, ok := pickFilm(pages, "Dune", 2021)
	if !ok || got.Key != "Dune_2021" {
		t.Errorf("picked %q, want Dune_2021", got.Key)
	}
	// With no year hint, the title must carry the match — here both articles
	// are titled "Dune", so the first one qualifies.
	got, ok = pickFilm(pages, "Dune", 0)
	if !ok || got.Key != "Dune_1984" {
		t.Errorf("with no year picked %q, want the first title match", got.Key)
	}
}

// A YEAR IS NOT AN IDENTITY. Found against live Wikipedia over 60 uncovered
// movie releases: "The Champion 2024" returned "Chandu Champion" and
// "Mia Moglie 2022" returned "Hey Sinamika" — both real films, both of the
// right year, both complete strangers to the release. The year branch used to
// return on the year alone, without ever comparing the title.
func TestPickFilmRefusesTheRightYearWithTheWrongTitle(t *testing.T) {
	pages := []searchPage{
		{Key: "Chandu_Champion", Title: "Chandu Champion", Description: "2024 Indian Hindi-language film"},
	}
	if got, ok := pickFilm(pages, "The Champion", 2024); ok {
		t.Errorf("accepted %q for an unrelated title of the same year", got.Key)
	}
	pages = []searchPage{
		{Key: "Hey_Sinamika", Title: "Hey Sinamika", Description: "2022 Indian Tamil-language film"},
	}
	if got, ok := pickFilm(pages, "Mia Moglie", 2022); ok {
		t.Errorf("accepted %q for an unrelated title of the same year", got.Key)
	}
}

// The mirror image: the right title in the wrong year. "Annie.2014" wore
// "Annie (1982 film)" and "I.See.You.2006" wore "I See You (2019 film)",
// because the title branch never looked at the year.
//
// The 1982 article's description does not always state its year — but the
// DISAMBIGUATOR does, which is why pageYear reads both.
func TestPickFilmRefusesTheRightTitleInTheWrongYear(t *testing.T) {
	pages := []searchPage{
		{Key: "Annie_1982", Title: "Annie (1982 film)", Description: "American musical film"},
	}
	if got, ok := pickFilm(pages, "Annie", 2014); ok {
		t.Errorf("accepted %q for a release of a different year", got.Key)
	}
	// The same page IS the answer for a release that names its year.
	if _, ok := pickFilm(pages, "Annie", 1982); !ok {
		t.Error("refused the film the release actually names")
	}
}

// An awards ceremony's description says "film awards", which satisfied the
// film test — so "Sikaisal" matched "70th National Film Awards", the ceremony
// the film won at, and took that page's image as its cover.
func TestPickFilmRefusesAwardsCeremonies(t *testing.T) {
	pages := []searchPage{
		{Key: "70th_National_Film_Awards", Title: "70th National Film Awards",
			Description: "2024 Indian film awards ceremony"},
	}
	if got, ok := pickFilm(pages, "Sikaisal", 2022); ok {
		t.Errorf("accepted %q, which is a ceremony rather than a film", got.Key)
	}
}

// Strict equality alone loses real matches, so containment is allowed — but
// only when the shorter side is long enough to be an identity. "Insurgent" is
// the Divergent film; "Dog" is not "Dogville".
func TestTitleRelatedAllowsSubtitlesButNotShortWords(t *testing.T) {
	for _, tc := range []struct {
		release, article string
		want             bool
	}{
		{"Insurgent", "The Divergent Series: Insurgent", true},
		{"Nirnayam Telugu", "Nirnayam (1991 film)", true},
		{"The Darjeeling", "The Darjeeling Limited", true},
		{"Dog", "Dogville", false},
		{"Live", "Live Free or Die Hard", false},
		{"The Champion", "Chandu Champion", false},
	} {
		if got := titleRelated(tc.release, tc.article); got != tc.want {
			t.Errorf("titleRelated(%q, %q) = %v, want %v", tc.release, tc.article, got, tc.want)
		}
	}
}

// The regression this policy exists for, found against LIVE Wikipedia: the
// release "Spiders 2013" returned "Paper Spiders", a 2020 film that merely
// ranked first for the word, and an earlier version accepted it because it was
// the first film in the results.
//
// A wrong match is worse than none. It puts a confident, plausible, incorrect
// poster and synopsis on a release page, and nothing downstream disagrees with
// it; no match just leaves the page as it is.
func TestPickFilmRefusesAPlausibleWrongTitle(t *testing.T) {
	pages := []searchPage{
		{Key: "Paper_Spiders", Title: "Paper Spiders", Description: "2020 film by Inon Shampanier"},
	}
	if got, ok := pickFilm(pages, "Spiders", 2013); ok {
		t.Errorf("picked %q for \"Spiders\" (2013) — neither the year nor the title matches", got.Key)
	}
	// Containment is not a match in either direction.
	if sameTitle("Spiders", "Paper Spiders") {
		t.Error(`sameTitle("Spiders", "Paper Spiders") = true`)
	}
	if sameTitle("Paper Spiders", "Spiders") {
		t.Error(`sameTitle("Paper Spiders", "Spiders") = true`)
	}
}

// The two naming conventions disagree about punctuation and about Wikipedia's
// parenthetical qualifier, and neither difference should cost a match.
func TestSameTitleIgnoresConventionDifferences(t *testing.T) {
	for _, c := range [][2]string{
		{"Dune Part Two", "Dune: Part Two"},
		{"Harry Potter and the Chamber of Secrets", "Harry Potter and the Chamber of Secrets (film)"},
		{"Underworld Rise of the Lycans", "Underworld: Rise of the Lycans"},
		{"Dont Look Up", "Don't Look Up"},
		{"Spider-Man No Way Home", "Spider-Man: No Way Home"},
		{"Spiders", "Spiders (2013 film)"},
	} {
		if !sameTitle(c[0], c[1]) {
			t.Errorf("sameTitle(%q, %q) = false, want true", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"Spiders", "Paper Spiders"},
		{"The Thing", "The Thing About Pam"},
		{"", "Anything"},
	} {
		if sameTitle(c[0], c[1]) {
			t.Errorf("sameTitle(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

// The failure that matters: returning a non-film puts a director's biography or
// a soundtrack on a release page. Better to return nothing.
func TestPickFilmRefusesNonFilms(t *testing.T) {
	pages := []searchPage{
		{Key: "List_of_accolades", Title: "List of accolades", Description: ""},
		{Key: "Daniels_(directors)", Title: "Daniels", Description: "American filmmaking duo"},
		{Key: "EEAAO_(soundtrack)", Title: "EEAAO (soundtrack)", Description: "2022 soundtrack album"},
	}
	if got, ok := pickFilm(pages, "Everything Everywhere All at Once", 2022); ok {
		t.Errorf("picked %q from a list with no film in it", got.Key)
	}
}

func fakeWiki(t *testing.T, pages []searchPage, sum *summary, sumStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "loon") {
			t.Errorf("User-Agent %q does not identify the client — Wikimedia policy asks it to", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		// Cross-ids: the same fake stands in for Wikidata, which is a
		// DIFFERENT host in production. Without this the suite calls the live
		// API — slow, and a test that fails when someone edits an article.
		if strings.Contains(r.URL.Path, "/w/api.php") {
			_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{
				"Q83495": map[string]any{"claims": map[string]any{
					"P345":  []any{claim("tt6710474")},
					"P4947": []any{claim("545611")},
					"P3302": []any{claim("179505")},
				}},
			}})
			return
		}
		if strings.Contains(r.URL.Path, "/summary/") {
			if sumStatus != http.StatusOK {
				w.WriteHeader(sumStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(sum)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pages": pages})
	}))
}

func TestSearchBuildsAnEntry(t *testing.T) {
	srv := fakeWiki(t,
		[]searchPage{
			{Key: "Daniels_(directors)", Title: "Daniels", Description: "American filmmaking duo"},
			{Key: "Everything_Everywhere_All_at_Once", Title: "Everything Everywhere All at Once",
				Description: "2022 film by Daniel Kwan and Daniel Scheinert"},
		},
		&summary{
			Title:       "Everything Everywhere All at Once",
			Description: "2022 film by Daniel Kwan and Daniel Scheinert",
			Extract:     "Everything Everywhere All at Once is a 2022 American film.",
			OriginalImage: struct {
				Source string `json:"source"`
			}{Source: "https://upload.wikimedia.org/poster.png"},
		}, http.StatusOK)
	defer srv.Close()

	src := New(srv.URL)
	src.wikidataURL = srv.URL
	e, ok, err := src.Search(context.Background(),
		"Everything.Everywhere.All.at.Once.2022.2160p.WEB-DL")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.Title != "Everything Everywhere All at Once" {
		t.Errorf("Title = %q — did the directors page win?", e.Title)
	}
	if e.Year != 2022 {
		t.Errorf("Year = %d", e.Year)
	}
	if e.CoverURL != "https://upload.wikimedia.org/poster.png" {
		t.Errorf("CoverURL = %q, want the full-size original", e.CoverURL)
	}
	if !strings.Contains(e.Fields["overview"].(string), "2022 American film") {
		t.Errorf("overview = %v", e.Fields["overview"])
	}
	// Its own id first, then the cross-ids Wikidata carries. These are what
	// put IMDb and Letterboxd buttons on a release page; without them a film
	// links only back to Wikipedia.
	want := map[string]string{
		"wikipedia": "Everything_Everywhere_All_at_Once",
		"imdb":      "tt6710474",
		"tmdb":      "545611",
	}
	// Letterboxd (P3302) is deliberately NOT collected: its value is a bare
	// number and letterboxd.com/film/<number>/ 404s. The host derives that
	// button from the TMDB id instead.
	for _, x := range e.External {
		if x.Namespace == "letterboxd" {
			t.Errorf("collected an unlinkable Letterboxd id: %+v", x)
		}
	}
	got := map[string]string{}
	for _, x := range e.External {
		got[x.Namespace] = x.Value
	}
	for ns, v := range want {
		if got[ns] != v {
			t.Errorf("External[%s] = %q, want %q (all: %+v)", ns, got[ns], v, e.External)
		}
	}
}

// claim builds one Wikidata identifier claim in the shape the API returns.
func claim(v string) map[string]any {
	return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": v}}}
}

// The page was identified; only its detail failed. The match is still correct,
// so it is kept — a title and a year beat discarding a good identification.
func TestSummaryFailureKeepsTheMatch(t *testing.T) {
	srv := fakeWiki(t,
		[]searchPage{{Key: "The_Matrix", Title: "The Matrix", Description: "1999 film by the Wachowskis"}},
		nil, http.StatusInternalServerError)
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "The.Matrix.1999.1080p.BluRay")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.Title != "The Matrix" || e.Year != 1999 {
		t.Errorf("entry lost its identification: %+v", e)
	}
	if e.CoverURL != "" {
		t.Errorf("CoverURL = %q with no summary — where did an image come from?", e.CoverURL)
	}
}

// Wikipedia has an article for almost everything, so "nothing here is a film"
// is a routine answer rather than an error.
func TestNoFilmIsNoMatchNotAnError(t *testing.T) {
	srv := fakeWiki(t,
		[]searchPage{{Key: "Some_Band", Title: "Some Band", Description: "English rock band"}},
		nil, http.StatusOK)
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "Some.Band.2019.1080p.WEB-DL")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Errorf("ok = true for a band: %+v", e)
	}
}

func TestNewNeverReturnsNil(t *testing.T) {
	if New("") == nil {
		t.Fatal("New(\"\") returned nil — the keyless source must always register")
	}
	if got := New("").Domain().Key; got != "movie" {
		t.Errorf("Domain().Key = %q, want movie", got)
	}
}

// The job asks per RELEASE; Wikipedia answers per FILM. 23,061 movie releases
// on the reference index carry 13,862 distinct films, and each match costs
// three calls (search, summary, Wikidata) — so re-asking is expensive.
func TestOneLookupPerFilm(t *testing.T) {
	var searches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/w/api.php"):
			_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
		case strings.Contains(r.URL.Path, "/summary/"):
			_ = json.NewEncoder(w).Encode(&summary{Title: "The Matrix"})
		default:
			searches++
			_ = json.NewEncoder(w).Encode(map[string]any{"pages": []searchPage{
				{Key: "The_Matrix", Title: "The Matrix", Description: "1999 film by the Wachowskis"},
			}})
		}
	}))
	defer srv.Close()

	src := New(srv.URL)
	src.wikidataURL = srv.URL
	// The same film posted three ways — one question.
	for _, rel := range []string{
		"The.Matrix.1999.1080p.BluRay.x264-GRP",
		"The.Matrix.1999.2160p.UHD.BluRay.x265",
		"The Matrix (1999) REMASTERED 720p",
	} {
		if _, ok, err := src.Search(context.Background(), rel); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", rel, ok, err)
		}
	}
	if searches != 1 {
		t.Errorf("made %d searches for one film, want 1", searches)
	}

	// A DIFFERENT year is a different film — remakes share a title exactly, so
	// the year is half the identity and must not collide in the cache.
	if _, _, err := src.Search(context.Background(), "The.Matrix.2021.1080p.WEB-DL"); err != nil {
		t.Fatal(err)
	}
	if searches != 2 {
		t.Errorf("made %d searches after a different year, want 2 — years must not share a cache key", searches)
	}
}
