package dailyreward

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The claim handler answers two callers: the widget's fetch, which wants JSON
// and to stay where it is, and a plain form POST from a browser with no
// JavaScript, which needs the redirect it has always had.
//
// The branch is an explicit X-Requested-With rather than Accept, and that is
// the part worth pinning. A browser submitting a form sends an Accept header
// listing several types — including application/json in some browsers — so
// matching on Accept would hand the no-JS reader a page of raw JSON instead of
// taking them back to the site.
func TestClaimAnswersFetchAndFormsDifferently(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"the widget's fetch", map[string]string{"X-Requested-With": "fetch"}, true},
		{"a plain form POST", nil, false},
		// The shapes a real browser sends for a form submit. None may be
		// mistaken for the widget.
		{"browser form Accept", map[string]string{
			"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		}, false},
		{"browser Accept listing json", map[string]string{
			"Accept": "text/html,application/json;q=0.9,*/*;q=0.8",
		}, false},
		// jQuery's convention is XMLHttpRequest, not fetch. It is not what the
		// widget sends, and guessing on its behalf would be inventing a caller.
		{"some other XHR library", map[string]string{"X-Requested-With": "XMLHttpRequest"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/plugin/dailyreward/claim", nil)
			for k, v := range tc.headers {
				c.Request.Header.Set(k, v)
			}
			if got := wantsJSON(c); got != tc.want {
				t.Errorf("wantsJSON = %v, want %v", got, tc.want)
			}
		})
	}
}
