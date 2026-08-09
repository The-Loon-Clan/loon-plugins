package requests

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseTitleMeta(t *testing.T) {
	cases := []struct {
		name                              string
		title                             string
		wantRes, wantSource, wantCategory string
	}{
		{"1080p bluray", "Attack on Titan S04 1080p BluRay", "1080p", "BluRay", "Anime"},
		{"2160p web-dl", "Frieren 2160p WEB-DL", "2160p", "WEB-DL", "Anime"},
		{"remux before bluray", "Show BluRay Remux 1080p", "1080p", "BluRay Remux", "Anime"},
		{"bdremux alias", "Show BDRemux 720p", "720p", "BluRay Remux", "Anime"},
		{"bdrip not bluray", "Show 480p BDRip", "480p", "BDRip", "Anime"},
		{"webrip", "Show WEBRip 1080p", "1080p", "WEBRip", "Anime"},
		{"dvd", "Old OVA DVD", "", "DVD", "Anime"},
		{"movie category", "Some Gekijouban Movie 1080p BluRay", "1080p", "BluRay", "Movie"},
		{"no metadata", "Just A Title", "", "", "Anime"},
		{"case insensitive", "show 1080P bluray", "1080p", "BluRay", "Anime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, src, cat := parseTitleMeta(tc.title)
			if res != tc.wantRes {
				t.Errorf("resolution = %q, want %q", res, tc.wantRes)
			}
			if src != tc.wantSource {
				t.Errorf("source = %q, want %q", src, tc.wantSource)
			}
			if cat != tc.wantCategory {
				t.Errorf("category = %q, want %q", cat, tc.wantCategory)
			}
		})
	}
}

func TestParseOptionalInt(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantNil bool
		wantVal int
	}{
		{"empty", "", true, 0},
		{"whitespace", "   ", true, 0},
		{"non-numeric", "abc", true, 0},
		{"trailing junk", "12x", true, 0},
		{"valid", "42", false, 42},
		{"valid with spaces", "  7  ", false, 7},
		{"negative", "-5", false, -5},
		{"zero", "0", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOptionalInt(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseOptionalInt(%q) = %d, want nil", tc.in, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseOptionalInt(%q) = nil, want %d", tc.in, tc.wantVal)
			}
			if *got != tc.wantVal {
				t.Errorf("parseOptionalInt(%q) = %d, want %d", tc.in, *got, tc.wantVal)
			}
		})
	}
}

func TestStripHTMLTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<b>hi</b>", "hi"},
		{"no tags here", "no tags here"},
		{`<a href="x">link</a> text`, "link text"},
		{"<div><span>nested</span></div>", "nested"},
		{"", ""},
		// The regex is a blunt <...> strip, not a real parser: a "< ... >"
		// run is treated as a tag and removed. Documents actual behavior.
		{"a < b and c > d", "a  d"},
	}
	for _, tc := range cases {
		if got := stripHTMLTags(tc.in); got != tc.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// wantStr asserts a gin.H key holds the expected string value.
func wantStr(t *testing.T, r gin.H, key, want string) {
	t.Helper()
	got, _ := r[key].(string)
	if got != want {
		t.Errorf("result[%q] = %q, want %q", key, got, want)
	}
}

func TestEnrichScrapeResult(t *testing.T) {
	t.Run("season and episode", func(t *testing.T) {
		r := gin.H{}
		enrichScrapeResult(r, "Cool Show S02E05 1080p BluRay")
		wantStr(t, r, "season", "02")
		wantStr(t, r, "episodes", "05")
		wantStr(t, r, "resolution", "1080p")
		wantStr(t, r, "source", "BluRay")
	})
	t.Run("volume range as season", func(t *testing.T) {
		r := gin.H{}
		enrichScrapeResult(r, "Manga Series Vol.1-5")
		wantStr(t, r, "season", "1-5")
	})
	t.Run("season only", func(t *testing.T) {
		r := gin.H{}
		enrichScrapeResult(r, "Show Season 3 720p")
		wantStr(t, r, "season", "3")
		wantStr(t, r, "resolution", "720p")
	})
}

func TestScrapeNyaaHTML(t *testing.T) {
	html := `
	<h3 class="panel-title">Test Anime S01E03 1080p BluRay</h3>
	<span>Seeders:</span> <span>42</span>
	<kbd>0123456789ABCDEF0123456789abcdef01234567</kbd>
	<li class="x"> <a href="y">episode.mkv</a> <span class="file-size">(1.2 GiB)</span></li>
	`
	res := scrapeNyaaHTML(html, func(s string) string { return s })
	wantStr(t, res, "title", "Test Anime S01E03 1080p BluRay")
	if got, _ := res["seed_count"].(int); got != 42 {
		t.Errorf("seed_count = %d, want 42", got)
	}
	// info hash is lowercased.
	wantStr(t, res, "info_hash", "0123456789abcdef0123456789abcdef01234567")
	// enrichScrapeResult also runs on the title.
	wantStr(t, res, "season", "01")
	wantStr(t, res, "episodes", "03")
	// file list parsed.
	files, ok := res["files"].([]gin.H)
	if !ok || len(files) != 1 {
		t.Fatalf("files = %v, want 1 entry", res["files"])
	}
	wantStr(t, files[0], "name", "episode.mkv")
	wantStr(t, files[0], "size", "1.2 GiB")
}

func TestScrapeNekoBTHTML(t *testing.T) {
	html := `
	<title>Some Show S02E03 - nekoBT</title>
	<span class="rounded px-1 text-xs">ABCDEF0123456789ABCDEF0123456789ABCDEF01</span>
	`
	res := scrapeNekoBTHTML(html)
	wantStr(t, res, "title", "Some Show S02E03")
	wantStr(t, res, "info_hash", "abcdef0123456789abcdef0123456789abcdef01")
	wantStr(t, res, "season", "02")
}

func TestScrapeNyaaHTMLEmpty(t *testing.T) {
	// A page that matches nothing must not panic; title stays unset.
	res := scrapeNyaaHTML("<html><body>nothing useful</body></html>", func(s string) string { return s })
	if _, ok := res["title"]; ok {
		t.Errorf("expected no title, got %v", res["title"])
	}
}
