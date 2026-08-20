package comments

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The redirect target is the one piece of this plugin an attacker controls: it
// arrives in a form field, and trusting it turns every comment form into an
// open redirect wearing the site's own domain.
func TestBackToRefusesOffsiteTargets(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/release/12", "/release/12"},
		{"/series/x?s=1&e=2", "/series/x?s=1&e=2"},
		{"/", "/"},
		// The whole point.
		{"https://evil.example/phish", "/"},
		{"//evil.example/phish", "/"},
		{"http://evil.example", "/"},
		// Not a path at all.
		{"evil.example", "/"},
		{"", "/"},
		{"   ", "/"},
		// A scheme-relative URL with whitespace in front, which a naive
		// HasPrefix("/") check on the untrimmed value would have accepted.
		{"  //evil.example", "/"},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/p/comments/post", nil)
		c.Request.PostForm = map[string][]string{"back": {tc.in}}
		if got := backTo(c); got != tc.want {
			t.Errorf("backTo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A comment's state decides what it shows and what it offers, and getting it
// wrong is silent: a removed comment that still renders its body is a
// moderation action that did nothing visible.
func TestCommentState(t *testing.T) {
	now := time.Now()
	live := Comment{CreatedAt: now}
	if live.Deleted() || live.Edited() {
		t.Error("a fresh comment reports as edited or deleted")
	}
	edited := Comment{CreatedAt: now, EditedAt: &now}
	if !edited.Edited() || edited.Deleted() {
		t.Error("an edited comment is not reporting exactly edited")
	}
	gone := Comment{CreatedAt: now, DeletedAt: &now}
	if !gone.Deleted() {
		t.Error("a removed comment does not report as deleted")
	}
}

// The relative time is what every row shows, and the boundaries are where it
// reads wrong — "60m ago" instead of "1h", or a date for something from this
// morning.
func TestAgo(t *testing.T) {
	ago := tmplFuncs()["ago"].(func(time.Time) string)
	for _, tc := range []struct {
		back time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{90 * time.Minute, "1h ago"},
		{25 * time.Hour, "1d ago"},
	} {
		if got := ago(time.Now().Add(-tc.back)); got != tc.want {
			t.Errorf("ago(-%s) = %q, want %q", tc.back, got, tc.want)
		}
	}
	// Older than a week falls back to a date rather than an ever-growing day
	// count, which nobody can read at a glance past about ten.
	old := time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC)
	if got := ago(old); got != "4 Mar 2020" {
		t.Errorf("ago(old) = %q, want the date", got)
	}
}
