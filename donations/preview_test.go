package donations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dump both pages as standalone documents so they can be looked at.
//
// Same reason as forum/preview_test.go: these two templates render only on the
// RenderPage contract, loon-demo-site still wires the legacy one and serves its
// own copies (183 and 310 lines against these 566 and 979), so this markup
// executes nowhere a person can see it. Reading it is not the same as looking
// at it, and every defect the forum migration turned up was found by looking.
//
// Skipped unless DONATIONS_PREVIEW_DIR is set, because it writes files and
// proves nothing on its own.
//
//	DONATIONS_PREVIEW_CSS=a.css,b.css DONATIONS_PREVIEW_SPRITE=s.svg \
//	DONATIONS_PREVIEW_DIR=/tmp/p go test ./donations/ -run Preview
//
// CSS is INLINED and the sprite injected rather than linked: the output lands
// in a scratch directory far from both, and a <link> that 404s renders exactly
// like a stylesheet that defines nothing — which is the bug being looked for.
func TestWriteDonationsPreviews(t *testing.T) {
	dir := os.Getenv("DONATIONS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set DONATIONS_PREVIEW_DIR to write previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var css strings.Builder
	for _, p := range strings.Split(os.Getenv("DONATIONS_PREVIEW_CSS"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("preview CSS %s: %v", p, err)
		}
		css.WriteString("/* --- " + filepath.Base(p) + " --- */\n")
		css.Write(b)
		css.WriteString("\n")
	}
	var sprite []byte
	if sp := os.Getenv("DONATIONS_PREVIEW_SPRITE"); sp != "" {
		b, err := os.ReadFile(sp)
		if err != nil {
			t.Fatalf("preview sprite: %v", err)
		}
		sprite = b
	}

	for name, body := range map[string]string{
		"help_donate.html":  testRenderDonate(t, true, "help_donate.html", donatePageFixture()),
		"admin_donate.html": testRenderDonate(t, true, "admin_donate.html", adminDonateFixture()),
	} {
		doc := "<!DOCTYPE html>\n<html lang=\"en\" data-theme=\"dark\">\n<head>\n" +
			"<meta charset=\"utf-8\">\n<title>" + name + "</title>\n<style>\n" +
			css.String() + "</style>\n</head>\n<body class=\"theme-dark\">\n" +
			string(sprite) + "\n<div class=\"site-container container page\">\n" +
			body + "\n</div>\n</body>\n</html>\n"
		out := filepath.Join(dir, strings.TrimSuffix(name, ".html")+".preview.html")
		if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes of fragment)", out, len(body))
	}
}
