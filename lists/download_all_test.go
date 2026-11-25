package lists

import (
	"archive/zip"
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// downloadAllFixture serves one public list of three releases through
// DownloadAllList, with gate as the host's per-item hook.
func downloadAllFixture(t *testing.T, gate func(c *gin.Context, nzbID int64) error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	saved := deps
	t.Cleanup(func() { deps = saved })
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, _ string, body template.HTML) {
			c.Writer.WriteString(string(body))
		},
		RenderError:     func(c *gin.Context, code int, msg string) { c.String(code, msg) },
		Viewer:          func(*gin.Context) (int, string, bool) { return 7, "m", true },
		DownloadAllowed: func(*gin.Context) bool { return true },
		ByID: func(context.Context, int) (*List, error) {
			return &List{ID: 1, Name: "picks", Public: true, UserID: 9}, nil
		},
		Items: func(context.Context, int) ([]Item, error) {
			return []Item{{ID: 1, Filename: "a"}, {ID: 2, Filename: "b"}, {ID: 3, Filename: "c"}}, nil
		},
		NzbData:            func(_ context.Context, id int64) ([]byte, error) { return []byte{byte(id)}, nil },
		Gunzip:             func(b []byte) ([]byte, error) { return b, nil },
		IncrementDownloads: func(context.Context, int) error { return nil },
		DownloadItem:       gate,
	})
	r := gin.New()
	r.GET("/lists/:id/download-all", (&Handlers{}).DownloadAllList)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/lists/1/download-all", nil))
	return w
}

func zipNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("not a zip (status %d): %v", w.Code, err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// "Download all" used to stream every item with nothing counted against the
// member's daily limit: one ZIP was a way round it (audit 2026-11 SEC-032).
// The host's DownloadItem now sees every item first.
func TestDownloadAllListAsksTheHostPerItem(t *testing.T) {
	// No hook: every item, as before.
	if got := zipNames(t, downloadAllFixture(t, nil)); strings.Join(got, ",") != "a.nzb,b.nzb,c.nzb" {
		t.Fatalf("without a hook = %v", got)
	}

	// Skip one, then run out of allowance: the ZIP holds what was allowed.
	seen := 0
	w := downloadAllFixture(t, func(_ *gin.Context, id int64) error {
		seen++
		switch id {
		case 2:
			return ErrSkipItem
		case 3:
			return ErrDownloadLimit
		}
		return nil
	})
	if got := zipNames(t, w); strings.Join(got, ",") != "a.nzb" {
		t.Fatalf("with skip + limit = %v, want only a.nzb", got)
	}
	if seen != 3 {
		t.Fatalf("hook saw %d items, want 3", seen)
	}

	// Nothing left at all: a page that says so, not an empty ZIP.
	w = downloadAllFixture(t, func(*gin.Context, int64) error { return ErrDownloadLimit })
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "used today") {
		t.Fatalf("limit on the first item = %d %q", w.Code, w.Body.String())
	}

	// Everything skipped: the list's empty state, not a 200 with no body.
	w = downloadAllFixture(t, func(*gin.Context, int64) error { return ErrSkipItem })
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "nothing in that list") {
		t.Fatalf("all skipped = %d %q", w.Code, w.Body.String())
	}
}
