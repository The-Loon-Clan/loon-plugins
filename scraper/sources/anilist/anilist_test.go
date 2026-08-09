package anilist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/catalog"
)

// The fansub convention is "[Group] Title - NN [tags]", and the parts have to
// come off in that order. These are real shapes from the index.
func TestParseReleaseName(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"[Erai-raws] Ragna Crimson - 04 [1080p][HEVC][2CDF803A]", "Ragna Crimson"},
		{"[Erai-raws] Kakkou no Iinazuke - 10 [720p][Multiple Subtitle][2B7D0FC3]", "Kakkou no Iinazuke"},
		{"[NH] The Rising of the Shield Hero - 05 (BD 1080p x264 10-bit FLAC) [F9D]", "The Rising of the Shield Hero"},
		// The group tag itself contains " - ". Stripping the tag BEFORE looking
		// for the episode dash is the only reason this works.
		{"[ArAn - Kuraki-Subs] UFO Robo Grendizer - 67 [BD 1080p x264 AAC][FB63382]", "UFO Robo Grendizer"},
		// A hyphenated title has no spaces around its hyphen, so it survives.
		{"Souten no Ken Re-Genesis - 02 [F-R][4f809950]", "Souten no Ken Re-Genesis"},
		{"[Erai-raws] Lapis Re-LiGHTs - 01 [720p][Multiple Subtitle]", "Lapis Re-LiGHTs"},
		// A four-digit episode number, and a posting handle in front.
		{"Crayon Shin-chan - 0059 - Hindi+Tamil+Telugu dub [ATTKC][AEE86F88]", "Crayon Shin-chan"},
		{"@AnimesHunt - Assassination Classroom S01 E20 [1080p] MULTI ~ VyxoR", "Assassination Classroom"},
		// A season suffix is part of the name AniList stores, so it stays.
		{"[Erai-raws] Kengan Ashura Season 2 - 26 [1080p][Multiple Subtitle]", "Kengan Ashura Season 2"},
		// No episode marker at all: the packaging still has to come off.
		{"[Anime Time] Record Of Ragnarok 1080p BD", "Record Of Ragnarok"},
	} {
		if got := ParseReleaseName(c.raw).Title; got != c.want {
			t.Errorf("ParseReleaseName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// AniList ranks a spin-off above its parent often enough to matter: searching
// "Frieren Beyond Journey's End" returns the chibi shorts first. Taking the
// first result would put the wrong poster on the release, and a wrong poster is
// a confident, plausible, incorrect claim nothing downstream disagrees with.
func TestPickBestPrefersTheTitleThatMatches(t *testing.T) {
	list := []media{
		{ID: 1, Format: "ONA", Title: struct {
			Romaji  string `json:"romaji"`
			English string `json:"english"`
			Native  string `json:"native"`
		}{Romaji: "Sousou no Frieren: Mahou"}},
		{ID: 2, Format: "TV", Title: struct {
			Romaji  string `json:"romaji"`
			English string `json:"english"`
			Native  string `json:"native"`
		}{Romaji: "Sousou no Frieren", English: "Frieren: Beyond Journey's End"}},
	}
	// By the romaji name.
	if got, ok := pickBest(list, "Sousou no Frieren"); !ok || got.ID != 2 {
		t.Errorf("picked %d, want the series (2)", got.ID)
	}
	// And by the English one, which is what many groups use.
	if got, ok := pickBest(list, "Frieren Beyond Journeys End"); !ok || got.ID != 2 {
		t.Errorf("picked %d by english title, want 2", got.ID)
	}
	// With no title match, a full series beats a short — never "the first".
	if got, ok := pickBest(list, "Something Else Entirely"); !ok || got.ID != 2 {
		t.Errorf("fallback picked %d, want the TV entry (2)", got.ID)
	}
	if _, ok := pickBest(nil, "Anything"); ok {
		t.Error("picked something from an empty result set")
	}
}

func fakeAniList(t *testing.T, payload any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "loon") {
			t.Errorf("User-Agent %q does not identify the client", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
}

func TestSearchBuildsAnEntry(t *testing.T) {
	srv := fakeAniList(t, map[string]any{"data": map[string]any{"Page": map[string]any{
		"media": []map[string]any{{
			"id": 154587, "idMal": 52991, "format": "TV", "episodes": 28,
			"status": "FINISHED", "genres": []string{"Adventure", "Drama", "Fantasy"},
			"averageScore": 91, "startDate": map[string]any{"year": 2023},
			"title":       map[string]any{"romaji": "Sousou no Frieren", "english": "Frieren: Beyond Journey's End"},
			"synonyms":    []string{"Frieren at the Funeral"},
			"description": "<p>The <i>elf</i> mage Frieren.</p>",
			"coverImage":  map[string]any{"extraLarge": "https://cdn/cover-xl.jpg", "large": "https://cdn/cover-l.jpg"},
			"bannerImage": "https://cdn/banner.jpg",
		}},
	}}})
	defer srv.Close()

	e, ok, err := New(srv.URL).Search(context.Background(),
		"[Erai-raws] Sousou no Frieren - 12 [1080p][Multiple Subtitle]")
	if err != nil || !ok {
		t.Fatalf("Search: ok=%v err=%v", ok, err)
	}
	if e.Title != "Sousou no Frieren" || e.Year != 2023 {
		t.Errorf("Title/Year = %q/%d", e.Title, e.Year)
	}
	// The FULL-SIZE cover, not the thumbnail — a poster slot is bigger.
	if e.CoverURL != "https://cdn/cover-xl.jpg" {
		t.Errorf("CoverURL = %q, want the extraLarge", e.CoverURL)
	}
	if e.Fields["banner_url"] != "https://cdn/banner.jpg" {
		t.Errorf("banner_url = %v", e.Fields["banner_url"])
	}
	if got := e.Fields["overview"]; got != "The elf mage Frieren." {
		t.Errorf("overview = %q — markup not stripped?", got)
	}
	if len(e.Genres) != 3 || e.Fields["episodes"] != 28 {
		t.Errorf("Genres=%v episodes=%v", e.Genres, e.Fields["episodes"])
	}
	// Both ids: its own, and the MyAnimeList cross-id that becomes a link
	// button on the release page.
	var haveAniList, haveMAL bool
	for _, x := range e.External {
		switch x.Namespace {
		case "anilist":
			haveAniList = x.Value == "154587"
		case "mal":
			haveMAL = x.Value == "52991"
		}
	}
	if !haveAniList || !haveMAL {
		t.Errorf("External = %+v, want anilist + mal ids", e.External)
	}
	// The English title and synonyms ride along, so a release named in either
	// language can still be matched later.
	if len(e.AltTitles) < 2 {
		t.Errorf("AltTitles = %v", e.AltTitles)
	}
}

// The job asks per RELEASE; AniList answers per SERIES. An anime index is
// almost entirely siblings, so caching the series is most of the saving.
func TestOneRequestPerSeries(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"Page": map[string]any{
			"media": []map[string]any{{"id": 1, "format": "TV",
				"title": map[string]any{"romaji": "Ragna Crimson"}}},
		}}})
	}))
	defer srv.Close()

	s := New(srv.URL)
	for _, ep := range []string{"04", "05", "06", "07"} {
		if _, ok, err := s.Search(context.Background(),
			"[Erai-raws] Ragna Crimson - "+ep+" [1080p][HEVC]"); err != nil || !ok {
			t.Fatalf("ep %s: ok=%v err=%v", ep, ok, err)
		}
	}
	if hits != 1 {
		t.Errorf("made %d requests for one series, want 1", hits)
	}
}

// A rate limit says nothing about the title. Caching one would turn a transient
// blip into a permanent hole in the catalogue.
func TestRateLimitIsReportedAndNotCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := New(srv.URL)
	for i := 0; i < 2; i++ {
		if _, ok, err := s.Search(context.Background(), "[X] Some Show - 01 [1080p]"); err == nil || ok {
			t.Fatalf("429 gave ok=%v err=%v", ok, err)
		}
	}
	if hits != 2 {
		t.Errorf("made %d requests, want 2 — a 429 must not be cached", hits)
	}
}

func TestNewNeverReturnsNil(t *testing.T) {
	if New("") == nil {
		t.Fatal(`New("") returned nil — the keyless source must always register`)
	}
	if got := New("").Domain().Key; got != "anime" {
		t.Errorf("domain = %q, want anime", got)
	}
	var _ catalog.MetadataSource = New("")
}
