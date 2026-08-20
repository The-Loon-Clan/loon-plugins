package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/catalog"
)

func TestNewGuards(t *testing.T) {
	if got := New("", KindMovie, ""); got != nil {
		t.Errorf("New with empty key = %v, want nil", got)
	}
	if got := New("   ", KindTV, ""); got != nil {
		t.Errorf("New with blank key = %v, want nil", got)
	}
	if got := New("key", Kind("music"), ""); got != nil {
		t.Errorf("New with unknown kind = %v, want nil", got)
	}
	if got := New("key", KindMovie, ""); got == nil {
		t.Fatal("New with a key = nil, want a source")
	}
}

func TestDomains(t *testing.T) {
	m := New("key", KindMovie, "").Domain()
	if m.Key != "movie" || m.UnitNoun != "movie" {
		t.Errorf("movie domain = %+v", m)
	}
	tv := New("key", KindTV, "").Domain()
	if tv.Key != "tv" || tv.UnitNoun != "series" {
		t.Errorf("tv domain = %+v", tv)
	}
	// anime (100) must outrank tv; both must outrank xxx (50).
	if !(m.Priority > 50 && tv.Priority > 50 && m.Priority < 100 && tv.Priority < 100) {
		t.Errorf("priorities out of band: movie=%d tv=%d", m.Priority, tv.Priority)
	}
	// Both instances register cleanly on one registry — the whole reason Kind
	// is a construction parameter.
	reg := catalog.NewRegistry()
	if err := reg.RegisterSource(New("key", KindMovie, "")); err != nil {
		t.Fatalf("register movie: %v", err)
	}
	if err := reg.RegisterSource(New("key", KindTV, "")); err != nil {
		t.Fatalf("register tv: %v", err)
	}
}

func TestDegenerateMetadataSource(t *testing.T) {
	s := New("key", KindMovie, "")
	idx, err := s.TitleIndex(context.Background())
	if err != nil || len(idx) != 0 {
		t.Errorf("TitleIndex = %v, %v; want empty, nil", idx, err)
	}
	if _, err := s.Fetch(context.Background(), 42); err != ErrNoLocalID {
		t.Errorf("Fetch err = %v, want ErrNoLocalID", err)
	}
	if got := s.Normalize("The.Matrix (1999)"); got != "the matrix 1999" {
		t.Errorf("Normalize = %q", got)
	}
}

func TestParseReleaseName(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		kind    Kind
		title   string
		year    int
		season  int
		episode int
	}{
		{
			name: "canonical scene movie",
			raw:  "Some.Movie.Name.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP",
			kind: KindMovie, title: "Some Movie Name", year: 2024,
		},
		{
			name: "year in parens",
			raw:  "Some Movie Name (2024) 1080p BluRay x264-SPARKS",
			kind: KindMovie, title: "Some Movie Name", year: 2024,
		},
		{
			name: "leading bracket tag and file extension",
			raw:  "[REQ] The.Matrix.1999.1080p.BluRay.DTS-HD.MA.5.1.x264-FGT.mkv",
			kind: KindMovie, title: "The Matrix", year: 1999,
		},
		{
			name: "hyphenated title survives",
			raw:  "Spider-Man.Across.the.Spider-Verse.2023.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
			kind: KindMovie, title: "Spider-Man Across the Spider-Verse", year: 2023,
		},
		{
			// "web" and "dvd" are real title words, so they must NOT be junk
			// tokens: cutting on them leaves "Charlottes"/"The", which the
			// search still answers — with the wrong film.
			name: "ambiguous word that is also a scene tag stays in the title",
			raw:  "Charlottes.Web.2006.1080p.BluRay.x264-AMIABLE",
			kind: KindMovie, title: "Charlottes Web", year: 2006,
		},
		{
			name: "one-word title that is a scene tag survives",
			raw:  "The.Web.1947.1080p.BluRay.x264",
			kind: KindMovie, title: "The Web", year: 1947,
		},
		{
			name: "number in title is not a year at index 0",
			raw:  "1917.2019.1080p.BluRay.x264-SPARKS",
			kind: KindMovie, title: "1917", year: 2019,
		},
		{
			name: "future-looking number in title is not a year",
			raw:  "Blade.Runner.2049.2160p.UHD.BluRay.REMUX-FraMeSToR",
			kind: KindMovie, title: "Blade Runner 2049", year: 0,
		},
		{
			name: "edition tag cuts the title but the year still hints",
			raw:  "Some.Movie.EXTENDED.2011.1080p.BluRay.x264",
			kind: KindMovie, title: "Some Movie", year: 2011,
		},
		{
			name: "scene word at index 0 is kept",
			raw:  "Uncut.Gems.2019.1080p.WEBRip.x265-RARBG",
			kind: KindMovie, title: "Uncut Gems", year: 2019,
		},
		{
			name: "no year, cut at the first scene tag",
			raw:  "Mad.Max.Fury.Road.BluRay.1080p.x264",
			kind: KindMovie, title: "Mad Max Fury Road", year: 0,
		},
		{
			name: "bare all-caps group with no scene tags",
			raw:  "Some.Movie.Name-SPARKS",
			kind: KindMovie, title: "Some Movie Name", year: 0,
		},
		{
			name: "already clean",
			raw:  "The Matrix",
			kind: KindMovie, title: "The Matrix", year: 0,
		},
		{
			name: "empty",
			raw:  "   ",
			kind: KindMovie, title: "", year: 0,
		},
		{
			name: "tv sxxexx",
			raw:  "The.Show.Name.S02E05.1080p.WEB-DL.DDP5.1.H.264-NTb",
			kind: KindTV, title: "The Show Name", year: 0, season: 2, episode: 5,
		},
		{
			name: "tv year before the season marker is a first-air hint",
			raw:  "Doctor.Who.2005.S01E01.1080p.BluRay.x264-GRP",
			kind: KindTV, title: "Doctor Who", year: 2005, season: 1, episode: 1,
		},
		{
			name: "tv year after the season marker is an air date, not a hint",
			raw:  "Some.Show.S01E01.2024.1080p.WEB.h264-GRP",
			kind: KindTV, title: "Some Show", year: 0, season: 1, episode: 1,
		},
		{
			name: "tv season pack",
			raw:  "Some.Show.S03.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb",
			kind: KindTV, title: "Some Show", year: 0, season: 3,
		},
		{
			name: "tv multi-season pack",
			raw:  "Some.Show.S01-S05.1080p.BluRay.x265-GRP",
			kind: KindTV, title: "Some Show", year: 0, season: 1,
		},
		{
			name: "tv spelled-out season",
			raw:  "Some.Show.Season.4.720p.HDTV.x264",
			kind: KindTV, title: "Some Show", year: 0, season: 4,
		},
		{
			name: "tv 1x05 form",
			raw:  "Some.Show.1x05.HDTV.XviD",
			kind: KindTV, title: "Some Show", year: 0, season: 1, episode: 5,
		},
		{
			name: "movie parser ignores season markers",
			raw:  "Season.of.the.Witch.2011.1080p.BluRay.x264",
			kind: KindMovie, title: "Season of the Witch", year: 2011,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReleaseName(tc.raw, tc.kind)
			want := Query{Title: tc.title, Year: tc.year, Season: tc.season, Episode: tc.episode}
			if got != want {
				t.Errorf("ParseReleaseName(%q, %q)\n got %+v\nwant %+v", tc.raw, tc.kind, got, want)
			}
		})
	}
}

// movieBody is one TMDB /search/movie payload with two hits, so the year-hint
// preference has something to choose between.
const movieBody = `{"page":1,"results":[
 {"id":11,"title":"Some Movie Name","original_title":"Un Film","overview":"An overview.",
  "poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg","genre_ids":[28,878,9999],
  "release_date":"1998-05-01","vote_average":6.4},
 {"id":22,"title":"Some Movie Name","original_title":"Some Movie Name","overview":"The remake.",
  "poster_path":"/remake.jpg","genre_ids":[18],"release_date":"2024-03-30","vote_average":7.1}
],"total_results":2}`

func TestSearchMovieMapping(t *testing.T) {
	var gotPath, gotQuery, gotKey, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotKey = r.URL.Query().Get("api_key")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(movieBody))
	}))
	defer srv.Close()

	s := New("secret", KindMovie, srv.URL)
	entry, ok, err := s.Search(context.Background(),
		"Some.Movie.Name.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP")
	if err != nil || !ok {
		t.Fatalf("Search = %v, %v", ok, err)
	}
	if gotPath != "/search/movie" {
		t.Errorf("path = %q, want /search/movie", gotPath)
	}
	if gotQuery != "Some Movie Name" {
		t.Errorf("query = %q, want the cleaned title", gotQuery)
	}
	if gotKey != "secret" {
		t.Errorf("api_key = %q", gotKey)
	}
	if gotUA == "" {
		t.Error("no User-Agent sent")
	}

	want := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "movie"},
		Title:    "Some Movie Name",
		Year:     2024,
		Genres:   []string{"Drama"},
		CoverURL: "https://image.tmdb.org/t/p/w500/remake.jpg",
		External: []catalog.ExternalID{{Namespace: "tmdb", Value: "22"}},
		Fields:   map[string]any{"overview": "The remake.", "vote_average": 7.1},
	}
	if !reflect.DeepEqual(entry, want) {
		t.Errorf("entry mismatch\n got %+v\nwant %+v", entry, want)
	}
}

func TestSearchMovieFallsBackToFirstResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(movieBody))
	}))
	defer srv.Close()

	// No year in the release name → no hint → first result wins.
	entry, ok, err := New("k", KindMovie, srv.URL).
		Search(context.Background(), "Some.Movie.Name.1080p.BluRay.x264-GRP")
	if err != nil || !ok {
		t.Fatalf("Search = %v, %v", ok, err)
	}
	if entry.External[0].Value != "11" || entry.Year != 1998 {
		t.Fatalf("entry = %+v, want the first result (id 11, 1998)", entry)
	}
	// Unknown genre ids are dropped, not rendered as blanks.
	if !reflect.DeepEqual(entry.Genres, []string{"Action", "Science Fiction"}) {
		t.Errorf("genres = %v", entry.Genres)
	}
	if !reflect.DeepEqual(entry.AltTitles, []string{"Un Film"}) {
		t.Errorf("alt titles = %v", entry.AltTitles)
	}
	if entry.Fields["backdrop"] != "https://image.tmdb.org/t/p/w780/backdrop.jpg" {
		t.Errorf("backdrop = %v", entry.Fields["backdrop"])
	}
}

func TestSearchTVMapping(t *testing.T) {
	const body = `{"results":[{"id":1396,"name":"Some Show","original_name":"Some Show",
	 "overview":"","poster_path":"","backdrop_path":"","genre_ids":[18,10759],
	 "first_air_date":"2008-01-20","vote_average":0}]}`
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("query")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	entry, ok, err := New("k", KindTV, srv.URL).
		Search(context.Background(), "Some.Show.S02E05.1080p.WEB-DL.DDP5.1.H.264-NTb")
	if err != nil || !ok {
		t.Fatalf("Search = %v, %v", ok, err)
	}
	if gotPath != "/search/tv" || gotQuery != "Some Show" {
		t.Errorf("path=%q query=%q", gotPath, gotQuery)
	}
	if entry.Ref.Kind != "tv" || entry.Year != 2008 {
		t.Errorf("entry = %+v", entry)
	}
	// TV genre ids resolve against the TV table, not the movie one.
	if !reflect.DeepEqual(entry.Genres, []string{"Drama", "Action & Adventure"}) {
		t.Errorf("genres = %v", entry.Genres)
	}
	// An empty poster_path must not become a bare CDN prefix.
	if entry.CoverURL != "" {
		t.Errorf("cover = %q, want empty", entry.CoverURL)
	}
	if len(entry.AltTitles) != 0 {
		t.Errorf("alt titles = %v, want none when original_name matches", entry.AltTitles)
	}
	// Nothing real to show → no display-only keys at all.
	if len(entry.Fields) != 0 {
		t.Errorf("fields = %v, want empty", entry.Fields)
	}
}

func TestSearchNoMatchIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"page":1,"results":[],"total_results":0}`))
	}))
	defer srv.Close()

	entry, ok, err := New("k", KindMovie, srv.URL).Search(context.Background(), "Nothing.Here.2024.1080p")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Errorf("ok = true, want false")
	}
	if !reflect.DeepEqual(entry, catalog.CatalogEntry{}) {
		t.Errorf("entry = %+v, want zero", entry)
	}
}

func TestSearchEmptyTitleSkipsTheCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	if _, ok, err := New("k", KindMovie, srv.URL).Search(context.Background(), "  "); ok || err != nil {
		t.Fatalf("Search = %v, %v", ok, err)
	}
	if called {
		t.Error("an unparseable release name still hit the API")
	}
}

func TestSearchStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"status_message":"Invalid API key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, ok, err := New("bad", KindMovie, srv.URL).Search(context.Background(), "The.Matrix.1999.1080p"); err == nil || ok {
		t.Fatalf("Search = %v, %v; want an error", ok, err)
	}
}

// The query string must survive escaping for titles with reserved characters.
func TestSearchEscapesQuery(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	_, _, _ = New("k", KindMovie, srv.URL).Search(context.Background(), "Tom & Jerry 2021 1080p WEB-DL")
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("raw query %q: %v", raw, err)
	}
	if v.Get("query") != "Tom & Jerry" {
		t.Errorf("query = %q", v.Get("query"))
	}
	if v.Get("include_adult") != "false" {
		t.Errorf("include_adult = %q", v.Get("include_adult"))
	}
}

// Guard the hardcoded genre tables against typos: every id must be positive and
// every name non-empty, and the two tables must disagree where TMDB does.
func TestGenreTables(t *testing.T) {
	for _, tbl := range []map[int]string{movieGenres, tvGenres} {
		for id, name := range tbl {
			if id <= 0 || name == "" {
				t.Errorf("bad genre entry %d=%q", id, name)
			}
		}
	}
	if movieGenres[10759] != "" {
		t.Error("10759 is a TV-only genre id")
	}
	if tvGenres[28] != "" {
		t.Error("28 (Action) is a movie-only genre id")
	}
}

// The response struct must tolerate a payload with unexpected/absent fields.
func TestResultDecodeIsLenient(t *testing.T) {
	var doc struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal([]byte(`{"results":[{"id":7,"adult":false,"popularity":1.5}]}`), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Results) != 1 || doc.Results[0].ID != 7 {
		t.Fatalf("results = %+v", doc.Results)
	}
	if doc.Results[0].displayTitle() != "" || doc.Results[0].year() != 0 {
		t.Errorf("empty payload produced %+v", doc.Results[0])
	}
}

// ── credential placement ────────────────────────────────────────────

func TestLooksLikeV4Token(t *testing.T) {
	// A real v4 read access token: a JWT, three dot-separated segments.
	if !looksLikeV4Token("eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJhYmMifQ.c2lnbmF0dXJl") {
		t.Error("a v4 token was not recognised")
	}
	for _, in := range []string{
		"",
		"09f7e02f1290be211da707a266f153b3",     // a v3 api_key: 32 hex, no dots
		"eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJhIn0", // two segments
		"a.b.c",                                // three segments, not a JWT
		"eyJhbGciOiJIUzI1NiJ9..c2ln",           // an empty segment
		"eyJhbGciOiJIUzI1NiJ9.eyJhIjoxfQ.sig.extra",
	} {
		if looksLikeV4Token(in) {
			t.Errorf("looksLikeV4Token(%q) = true", in)
		}
	}
}

// TestAV4TokenNeverReachesTheURL is the point of the whole change: a credential
// in a query string is a credential in every place a URL goes, and net/http
// puts the URL into the error it returns from any transport failure.
func TestAV4TokenNeverReachesTheURL(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJhYmMifQ.c2lnbmF0dXJl"
	var gotURL, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(movieBody))
	}))
	defer srv.Close()

	s := New(token, KindMovie, srv.URL)
	if _, _, err := s.Search(context.Background(), "Some.Movie.Name.2024.1080p-GRP"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if strings.Contains(gotURL, token) || strings.Contains(gotURL, "api_key") {
		t.Errorf("the token reached the URL: %s", gotURL)
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
}

// TestAV3KeyStillTravelsInTheQuery. TMDB gives a v3 api_key no header form, so
// an operator who has not migrated must keep working — a "fix" that silently
// broke every v3 key would be worse than the leak it closed.
func TestAV3KeyStillTravelsInTheQuery(t *testing.T) {
	const key = "09f7e02f1290be211da707a266f153b3"
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(movieBody))
	}))
	defer srv.Close()

	s := New(key, KindMovie, srv.URL)
	if _, _, err := s.Search(context.Background(), "Some.Movie.Name.2024.1080p-GRP"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != key {
		t.Errorf("api_key = %q, want the v3 key", gotKey)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none for a v3 key", gotAuth)
	}
}

// TestATransportFailureDoesNotLeakTheKey — the leak this began with, closed at
// the source rather than only at the log.
func TestATransportFailureDoesNotLeakTheKey(t *testing.T) {
	const key = "09f7e02f1290be211da707a266f153b3"
	// A server that is closed before the call, so Do fails at the transport.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	s := New(key, KindMovie, base)
	_, _, err := s.Search(context.Background(), "Some.Movie.Name.2024.1080p-GRP")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the api_key is in the error, and therefore in error_logs:\n%v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("nothing was redacted, so the guard did not run:\n%v", err)
	}
}
