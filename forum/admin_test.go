package forum

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// categoryStyle guards two injection surfaces: color becomes a CSS class
// suffix (closed palette) and icon a bi-<name> class (charset-restricted).
func TestCategoryStyle(t *testing.T) {
	cases := []struct {
		icon, color         string
		wantIcon, wantColor string
	}{
		{"megaphone", "green", "megaphone", "green"},
		{"chat-square-text", "yellow", "chat-square-text", "yellow"},
		{"", "", "chat-square-text", "blue"},                        // defaults
		{"Bad Icon", "blue", "chat-square-text", "blue"},            // space + upper rejected
		{"x\" onmouseover=\"1", "blue", "chat-square-text", "blue"}, // attr smuggling rejected
		{"star", "mauve", "star", "blue"},                           // unknown color falls back
		{"star", "red; background:url(x)", "star", "blue"},          // css smuggling rejected
	}
	gin.SetMode(gin.TestMode)
	for _, cse := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		form := url.Values{"icon": {cse.icon}, "color": {cse.color}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		icon, color := categoryStyle(c)
		if icon != cse.wantIcon || color != cse.wantColor {
			t.Errorf("categoryStyle(%q,%q) = (%q,%q), want (%q,%q)",
				cse.icon, cse.color, icon, color, cse.wantIcon, cse.wantColor)
		}
	}
}
