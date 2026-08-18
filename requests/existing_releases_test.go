package requests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The "this may already exist" endpoint. Everything runs against fake seams:
// the contract under test is resolution order (anime_id wins, then title),
// free-text season/episode parsing, and the nil-seam guarantee that a
// half-wired host degrades to found:false instead of a broken form.

func existingHandlers(d Deps) *Handlers {
	return &Handlers{deps: d, errs: core.NewErrorReporter(core.ErrorAdapter{})}
}

func getExisting(t *testing.T, h *Handlers, rawQuery string) (int, existingReleasesResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/community/requests/existing?"+rawQuery, nil)
	h.ExistingReleases(c)
	var resp existingReleasesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v — %s", err, w.Body.String())
	}
	return w.Code, resp
}

func sampleExistingRows() []ExistingRelease {
	s, e := 2, 5
	return []ExistingRelease{
		{ID: 11, Title: "Great Show S02E05 1080p", Season: &s, Episode: &e,
			Resolution: "1080p", Source: "WEB-DL", Size: 700 << 20,
			CreatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 9, Title: "Great Show S02E05 720p", Season: &s, Episode: &e,
			Resolution: "720p", Source: "HDTV", Size: 300 << 20,
			CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Dead: true},
	}
}

func TestExistingReleasesAnimeIDPath(t *testing.T) {
	var gotAnime, gotLimit int
	var gotSeason, gotEpisode *int
	h := existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			gotAnime, gotSeason, gotEpisode, gotLimit = animeID, season, episode, limit
			return sampleExistingRows(), nil
		},
		// Present but must not be consulted: anime_id wins.
		ResolveAnimeTitle: func(ctx context.Context, title string) (int, bool) {
			t.Errorf("ResolveAnimeTitle called despite anime_id=%q", title)
			return 0, false
		},
	})

	code, resp := getExisting(t, h, "anime_id=42&title=ignored&season=2&episodes=5")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if gotAnime != 42 || gotLimit != existingReleasesLimit+1 {
		t.Errorf("seam got (anime=%d limit=%d), want (42, %d — one over the cap for has_more)",
			gotAnime, gotLimit, existingReleasesLimit+1)
	}
	if gotSeason == nil || *gotSeason != 2 || gotEpisode == nil || *gotEpisode != 5 {
		t.Errorf("filters = (%v, %v), want (2, 5)", gotSeason, gotEpisode)
	}
	if !resp.OK || !resp.Found || !resp.Exact {
		t.Errorf("envelope = %+v, want ok+found+exact", resp)
	}
	if resp.Total != 2 || len(resp.Releases) != 2 {
		t.Fatalf("total=%d releases=%d, want 2/2", resp.Total, len(resp.Releases))
	}
	if resp.Releases[0].URL != "/release/11" {
		t.Errorf("url = %q, want /release/11", resp.Releases[0].URL)
	}
	if resp.Releases[0].Dead || !resp.Releases[1].Dead {
		t.Errorf("dead flags = [%v %v], want [false true]", resp.Releases[0].Dead, resp.Releases[1].Dead)
	}
	if resp.Releases[0].Date != "2026-08-02" {
		t.Errorf("date = %q", resp.Releases[0].Date)
	}
	if resp.Releases[0].Season == nil || *resp.Releases[0].Season != 2 {
		t.Errorf("season not carried: %+v", resp.Releases[0])
	}
}

func TestExistingReleasesTitleResolvePath(t *testing.T) {
	var resolved string
	var gotAnime int
	h := existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			gotAnime = animeID
			if season != nil || episode != nil {
				t.Errorf("filters = (%v, %v), want none", season, episode)
			}
			return sampleExistingRows()[:1], nil
		},
		ResolveAnimeTitle: func(ctx context.Context, title string) (int, bool) {
			resolved = title
			return 42, true
		},
	})

	code, resp := getExisting(t, h, "title=Great+Show")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resolved != "Great Show" || gotAnime != 42 {
		t.Errorf("resolved %q -> anime %d, want Great Show -> 42", resolved, gotAnime)
	}
	if !resp.Found || resp.Exact {
		t.Errorf("envelope = %+v, want found and NOT exact (no filters)", resp)
	}

	// A title the matcher does not know: no resolution, quiet found:false.
	h = existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			t.Error("release seam called with no resolved anime")
			return nil, nil
		},
		ResolveAnimeTitle: func(ctx context.Context, title string) (int, bool) { return 0, false },
	})
	if _, resp = getExisting(t, h, "title=Unknown+Show"); !resp.OK || resp.Found {
		t.Errorf("unresolved title: %+v, want ok+not-found", resp)
	}

	// Title given but no ResolveAnimeTitle seam: the title path is simply off.
	h = existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			t.Error("release seam called without a resolver")
			return nil, nil
		},
	})
	if _, resp = getExisting(t, h, "title=Great+Show"); !resp.OK || resp.Found {
		t.Errorf("resolver-less title: %+v, want ok+not-found", resp)
	}
}

// A host that wired neither seam still boots (they are outside ok() on
// purpose) — and this endpoint must answer found:false, never break.
func TestExistingReleasesNilSeam(t *testing.T) {
	h := existingHandlers(Deps{})
	code, resp := getExisting(t, h, "anime_id=42&season=2&episodes=5")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if !resp.OK || resp.Found || resp.Exact || resp.Total != 0 {
		t.Errorf("nil seam: %+v, want bare ok:true found:false", resp)
	}
	if resp.Releases == nil || len(resp.Releases) != 0 {
		t.Errorf("releases should be an empty array, got %#v", resp.Releases)
	}

	// No anime_id, no title either.
	if _, resp = getExisting(t, existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			t.Error("seam called with nothing to resolve")
			return nil, nil
		},
	}), ""); resp.Found {
		t.Errorf("no params: %+v", resp)
	}
}

// The handler fetches one row OVER the cap: the extra row only proves the
// cap truncated, so the card can say "8+" instead of presenting the page
// length as the episode's total.
func TestExistingReleasesHasMore(t *testing.T) {
	over := make([]ExistingRelease, existingReleasesLimit+1)
	for i := range over {
		over[i] = ExistingRelease{
			ID:        int64(100 - i), // distinct ids, "newest" first
			Title:     "Great Show",
			CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour),
		}
	}
	var gotLimit int
	h := existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			gotLimit = limit
			return over, nil
		},
	})

	code, resp := getExisting(t, h, "anime_id=42")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if gotLimit != existingReleasesLimit+1 {
		t.Errorf("seam limit = %d, want %d (one over the cap)", gotLimit, existingReleasesLimit+1)
	}
	if len(resp.Releases) != existingReleasesLimit {
		t.Fatalf("releases = %d, want the cap %d", len(resp.Releases), existingReleasesLimit)
	}
	if resp.Total != existingReleasesLimit {
		t.Errorf("total = %d, want %d (rows returned, not the real count)", resp.Total, existingReleasesLimit)
	}
	if !resp.HasMore {
		t.Error("has_more = false with a truncated result")
	}
	// The sentinel row is dropped from the front-ordered page, not shown.
	if last := resp.Releases[existingReleasesLimit-1].ID; last != over[existingReleasesLimit-1].ID {
		t.Errorf("last kept id = %d, want %d", last, over[existingReleasesLimit-1].ID)
	}

	// Under the cap: nothing truncated, has_more stays false.
	h = existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			return sampleExistingRows(), nil
		},
	})
	_, resp = getExisting(t, h, "anime_id=42")
	if resp.HasMore {
		t.Error("has_more = true for an under-limit result")
	}
	if resp.Total != 2 || len(resp.Releases) != 2 {
		t.Errorf("under-limit total/len = %d/%d, want 2/2", resp.Total, len(resp.Releases))
	}
}

func TestExistingReleasesSeamError(t *testing.T) {
	h := existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			return nil, errors.New("db down")
		},
	})
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/community/requests/existing?anime_id=42", nil)
	h.ExistingReleases(c)
	if w.Code != 500 {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if ok, _ := body["ok"].(bool); ok {
		t.Error("error envelope says ok:true")
	}
	if body["error"] == "" || body["error"] == nil {
		t.Error("error envelope carries no message")
	}
}

// The season/episodes fields are the form's free text ("2", "1-3", "*"):
// only a lone integer becomes a filter, everything else means "don't narrow".
func TestParseEpisodeFilter(t *testing.T) {
	five, seven, zero := 5, 7, 0
	cases := []struct {
		in   string
		want *int
	}{
		{"5", &five},
		{"05", &five},
		{" 7 ", &seven},
		{"0", &zero},
		{"", nil},
		{"   ", nil},
		{"1-12", nil},
		{"1,3", nil},
		{"*", nil},
		{"abc", nil},
		{"-3", nil},
		{"S2", nil},
		{"2.5", nil},
	}
	for _, tc := range cases {
		got := parseEpisodeFilter(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseEpisodeFilter(%q) = %d, want nil", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseEpisodeFilter(%q) = nil, want %d", tc.in, *tc.want)
		case tc.want != nil && got != nil && *got != *tc.want:
			t.Errorf("parseEpisodeFilter(%q) = %d, want %d", tc.in, *got, *tc.want)
		}
	}

	// Endpoint-level: a range in season and a wildcard in episodes narrow
	// nothing, and exact stays false.
	var gotSeason, gotEpisode *int
	h := existingHandlers(Deps{
		ExistingReleases: func(ctx context.Context, animeID int, season, episode *int, limit int) ([]ExistingRelease, error) {
			gotSeason, gotEpisode = season, episode
			return sampleExistingRows(), nil
		},
	})
	_, resp := getExisting(t, h, "anime_id=42&season=1-3&episodes=*")
	if gotSeason != nil || gotEpisode != nil {
		t.Errorf("free-text filters leaked: (%v, %v)", gotSeason, gotEpisode)
	}
	if resp.Exact {
		t.Error("exact reported without both filters applied")
	}
}
