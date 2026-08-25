package requests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// countingTorznab answers every search and counts how often it is asked —
// which is the whole point of the cache: the same missing episode clicked
// by every member who wants it must be ONE upstream question per day.
type countingTorznab struct{ calls int }

func (c *countingTorznab) Available() bool { return true }
func (c *countingTorznab) Search(ctx context.Context, query string, season, episode int) ([]pluginapi.TorznabResult, error) {
	c.calls++
	return []pluginapi.TorznabResult{{Title: "[Grp] Show - 05", Seeders: 3}}, nil
}

func torSearchReq(t *testing.T, h *Handlers, url string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	h.SearchTorrents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return out
}

func TestSearchTorrentsCachesForADay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tz := &countingTorznab{}
	h := &Handlers{deps: Deps{Torznab: tz}}

	first := torSearchReq(t, h, "/search?q=Show+05")
	if first["count"].(float64) != 1 {
		t.Fatalf("first answer wrong: %v", first)
	}
	torSearchReq(t, h, "/search?q=Show+05")
	torSearchReq(t, h, "/search?q=show+05") // case folds into the same key
	if tz.calls != 1 {
		t.Errorf("upstream asked %d times for one query; the cache should have answered", tz.calls)
	}

	// A different query is a different question.
	torSearchReq(t, h, "/search?q=Other+Show")
	if tz.calls != 2 {
		t.Errorf("distinct query did not reach upstream (calls=%d)", tz.calls)
	}

	// refresh=1 — the manual search button — bypasses and rewrites.
	torSearchReq(t, h, "/search?q=Show+05&refresh=1")
	if tz.calls != 3 {
		t.Errorf("refresh=1 did not reach upstream (calls=%d)", tz.calls)
	}

	// An entry older than the TTL is dead.
	key := "show 05|5070"
	h.torCacheMu.Lock()
	e := h.torCache[key]
	e.at = e.at.Add(-25 * 60 * 60 * 1e9) // 25h ago
	h.torCache[key] = e
	h.torCacheMu.Unlock()
	torSearchReq(t, h, "/search?q=Show+05")
	if tz.calls != 4 {
		t.Errorf("an expired entry was served (calls=%d)", tz.calls)
	}
}
