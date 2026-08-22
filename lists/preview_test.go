package lists

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dump every page as a standalone document so it can be looked at.
//
// Same reason as forum's, donations' and communities': no host renders these,
// so the markup executes nowhere a person can see it. That is how twenty class
// names ended up in here that no stylesheet defines — nothing draws them, so
// nothing showed the gap.
//
//	LISTS_PREVIEW_CSS=a.css LISTS_PREVIEW_SPRITE=s.svg \
//	LISTS_PREVIEW_DIR=/tmp/p go test -count=1 ./lists/ -run Preview
//
// USE -count=1. These read the host's stylesheets, which live outside the
// package, so Go's test cache cannot know they changed: a second run after a
// CSS edit replays the previous log — "wrote ..." and all — without executing,
// and the dumped pages are silently the old ones. That cost a wrong conclusion
// once: a grid fix looked like it had not worked when it had.
func TestWriteListsPreviews(t *testing.T) {
	dir := os.Getenv("LISTS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set LISTS_PREVIEW_DIR to write previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var css strings.Builder
	for _, p := range strings.Split(os.Getenv("LISTS_PREVIEW_CSS"), ",") {
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
	if sp := os.Getenv("LISTS_PREVIEW_SPRITE"); sp != "" {
		b, err := os.ReadFile(sp)
		if err != nil {
			t.Fatalf("preview sprite: %v", err)
		}
		sprite = b
	}

	// A cover-less list beside one with a cover, because the placeholder is
	// its own layout; and enough rows for the grid to have to wrap.
	card := func(n string) template.HTML {
		return template.HTML(`<div class="nzb-card" style="border:1px solid currentColor;padding:.75rem;">` + n + `</div>`)
	}
	many := []List{}
	for i := 1; i <= 7; i++ {
		l := sampleList(i, []string{"Anime 2026", "Long list name that has to wrap somewhere sensible",
			"Docs", "Music", "Retro", "Shorts", "Misc"}[i-1])
		if i%3 == 0 {
			l.CoverURL = ""
		}
		many = append(many, l)
	}

	l := sampleList(7, "mylist")
	pages := []struct {
		name string
		vm   any
	}{
		{"user_lists.html", userListsVM{Lists: many[:4], Followed: many[4:]}},
		{"list_detail.html", listDetailVM{
			List: &l,
			Items: []Item{
				{ID: 1, Filename: "Some.Release.2026.1080p.WEB.nzb", Card: card("card one")},
				{ID: 2, Filename: "Another.Release.2026.720p.nzb", Card: card("card two")},
				{ID: 3, Filename: "A.Third.One.nzb", Card: card("card three")},
			},
			IsOwner: false, ViewerID: 999,
			NzbCardCSS:  template.HTML("<style>/* host card css */</style>"),
			ReportModal: template.HTML(`<div id="reportmodal" hidden></div>`),
		}},
		{"release_lists.html", releaseListsVM{
			Nzb: &NzbRef{ID: 42, Title: "Some.Release.2026.1080p.WEB-DL"}, Lists: many[:4],
		}},
		{"community_watchlists.html", watchlistsVM{
			Lists:      many,
			NzbCardCSS: template.HTML("<style>/* host card css */</style>"),
		}},
		{"list_error.html", map[string]any{"Reason": "notfound"}},
	}

	for _, pg := range pages {
		body := renderOK(t, pg.name, pg.vm)
		doc := "<!DOCTYPE html>\n<html lang=\"en\" data-theme=\"dark\">\n<head>\n" +
			"<meta charset=\"utf-8\">\n<title>" + pg.name + "</title>\n<style>\n" +
			css.String() + "</style>\n</head>\n<body class=\"theme-dark\">\n" +
			string(sprite) + "\n<div class=\"site-container container page\">\n" +
			body + "\n</div>\n</body>\n</html>\n"
		out := filepath.Join(dir, strings.TrimSuffix(pg.name, ".html")+".preview.html")
		if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes of fragment)", out, len(body))
	}
}
