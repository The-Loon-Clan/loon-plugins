package tvmaze

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TVmaze searches SERIES, so the season/episode marker and everything after it
// has to go. These are real scene shapes.
func TestParseReleaseName(t *testing.T) {
	cases := []struct {
		raw       string
		wantTitle string
		wantYear  int
	}{
		{"Breaking.Bad.S05E14.Ozymandias.1080p.BluRay.x264-GRP", "Breaking Bad", 0},
		{"Doctor.Who.2005.S01E01.1080p.WEB-DL.DDP5.1.H.264", "Doctor Who 2005", 2005},
		{"The.Office.US.S03E10.HDTV.XviD", "The Office US", 0},
		{"Severance.S02E05.2160p.ATVP.WEB-DL.DDP5.1.Atmos.H.265", "Severance", 0},
		{"Fringe 3x19 LSD HDTV", "Fringe", 0},
		{"Chernobyl.S01.COMPLETE.1080p.AMZN.WEB-DL", "Chernobyl", 0},
		{"[Group] Some Show - 03 (1080p)", "Some Show - 03", 0},
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

// Summaries arrive as HTML. A release page renders text: storing the markup
// either shows the tags literally or hands raw third-party HTML to a template.
func TestStripHTML(t *testing.T) {
	in := "<p><b>Breaking Bad</b> follows Walter White, a chemistry teacher &amp; father.</p>"
	want := "Breaking Bad follows Walter White, a chemistry teacher & father."
	if got := stripHTML(in); got != want {
		t.Errorf("stripHTML = %q, want %q", got, want)
	}
	if got := stripHTML(""); got != "" {
		t.Errorf("stripHTML(\"\") = %q", got)
	}
}

func fakeTVmaze(t *testing.T, status int, sh any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent — a keyless API identifies its clients this way")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if sh != nil {
			_ = json.NewEncoder(w).Encode(sh)
		}
	}))
}

// The shape here mirrors a verified live response for Breaking Bad.
func TestSearchBuildsAnEntry(t *testing.T) {
	sh := map[string]any{
		"id": 169, "name": "Breaking Bad", "language": "English",
		"genres": []string{"Drama", "Crime", "Thriller"},
		"status": "Ended", "premiered": "2008-01-20", "ended": "2013-09-29", "runtime": 60,
		"summary": "<p><b>Breaking Bad</b> follows Walter White.</p>",
		"image": map[string]string{
			"medium":   "https://static.tvmaze.com/uploads/images/medium_portrait/501/1253519.jpg",
			"original": "https://static.tvmaze.com/uploads/images/original_untouched/501/1253519.jpg",
		},
		"network":   map[string]string{"name": "AMC"},
		"rating":    map[string]float64{"average": 9.2},
		"externals": map[string]any{"imdb": "tt0903747", "thetvdb": 81189},
	}
	srv := fakeTVmaze(t, http.StatusOK, sh)
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(), "Breaking.Bad.S05E14.1080p.BluRay.x264-GRP")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.Title != "Breaking Bad" || e.Year != 2008 {
		t.Errorf("Title/Year = %q/%d", e.Title, e.Year)
	}
	// The ORIGINAL image, not the 210px medium — a poster slot is bigger than
	// that on any modern display.
	if want := "https://static.tvmaze.com/uploads/images/original_untouched/501/1253519.jpg"; e.CoverURL != want {
		t.Errorf("CoverURL = %q, want the original", e.CoverURL)
	}
	if e.Fields["overview"] != "Breaking Bad follows Walter White." {
		t.Errorf("overview = %v — HTML not stripped?", e.Fields["overview"])
	}
	if e.Fields["network"] != "AMC" || e.Fields["premiered"] != "2008-01-20" {
		t.Errorf("network/premiered = %v/%v", e.Fields["network"], e.Fields["premiered"])
	}
	if len(e.Genres) != 3 {
		t.Errorf("Genres = %v", e.Genres)
	}
	// Cross-ids let a later TMDB/TVDB upgrade reconcile with what this stored.
	var haveIMDB, haveTVDB bool
	for _, x := range e.External {
		switch x.Namespace {
		case "imdb":
			haveIMDB = x.Value == "tt0903747"
		case "tvdb":
			haveTVDB = x.Value == "81189"
		}
	}
	if !haveIMDB || !haveTVDB {
		t.Errorf("External = %+v, want imdb + tvdb cross-ids", e.External)
	}
}

// singlesearch answers 404 when nothing matches. That is the normal "not a
// known series" case, not a failure worth reporting.
func TestSearch404IsNoMatchNotAnError(t *testing.T) {
	srv := fakeTVmaze(t, http.StatusNotFound, nil)
	defer srv.Close()
	_, ok, err := New(srv.URL).Search(context.Background(), "Not.A.Real.Show.S01E01")
	if err != nil {
		t.Fatalf("err = %v, want nil on 404", err)
	}
	if ok {
		t.Error("ok = true on 404")
	}
}

// 429 must be distinguishable: it is the one failure an operator can act on,
// and a keyless API throttles by IP — so it affects everyone on that address.
func TestRateLimitIsReportedClearly(t *testing.T) {
	srv := fakeTVmaze(t, http.StatusTooManyRequests, nil)
	defer srv.Close()
	_, ok, err := New(srv.URL).Search(context.Background(), "Anything.S01E01")
	if err == nil {
		t.Fatal("429 returned no error")
	}
	if ok {
		t.Error("ok = true on 429")
	}
}

// The source throttles ITSELF: without a key, exceeding the documented rate
// gets the whole IP blocked, so politeness cannot be left to the caller.
func TestRequestsAreSpacedOut(t *testing.T) {
	var times []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, nowMillis())
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "X"})
	}))
	defer srv.Close()

	s := New(srv.URL)
	for i := 0; i < 3; i++ {
		if _, _, err := s.Search(context.Background(), "Show.S01E01"); err != nil {
			t.Fatal(err)
		}
	}
	if len(times) < 3 {
		t.Fatalf("got %d requests", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i] - times[i-1]; gap < int64(minInterval/1e6)-50 {
			t.Errorf("requests %d and %d were %dms apart, want >= %dms",
				i-1, i, gap, int64(minInterval/1e6))
		}
	}
}

func TestNewNeverReturnsNil(t *testing.T) {
	if New("") == nil {
		t.Fatal("New(\"\") returned nil — the keyless source must always register")
	}
	if got := New("").Domain().Key; got != "tv" {
		t.Errorf("Domain().Key = %q, want tv", got)
	}
}

func nowMillis() int64 { return time.Now().UnixNano() / 1e6 }
