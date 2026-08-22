package communities

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dump every page as a standalone document so it can be looked at.
//
// Same reason as forum/preview_test.go and donations': these templates render
// only on the RenderPage contract, loon-demo-site still wires the legacy one
// and serves its own copies, so this markup executes nowhere a person can see
// it. Every defect the forum migration turned up was found by looking, not by
// reading — a pinned marker that rendered nothing, three JS states with no
// rule, an error page that did not exist.
//
// Skipped unless COMMUNITIES_PREVIEW_DIR is set.
//
//	COMMUNITIES_PREVIEW_CSS=a.css COMMUNITIES_PREVIEW_SPRITE=s.svg \
//	COMMUNITIES_PREVIEW_DIR=/tmp/p go test ./communities/ -run Preview
func TestWriteCommunitiesPreviews(t *testing.T) {
	dir := os.Getenv("COMMUNITIES_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set COMMUNITIES_PREVIEW_DIR to write previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var css strings.Builder
	for _, p := range strings.Split(os.Getenv("COMMUNITIES_PREVIEW_CSS"), ",") {
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
	if sp := os.Getenv("COMMUNITIES_PREVIEW_SPRITE"); sp != "" {
		b, err := os.ReadFile(sp)
		if err != nil {
			t.Fatalf("preview sprite: %v", err)
		}
		sprite = b
	}

	pager := template.HTML(`<nav id="pg" style="opacity:.5;">[host pager]</nav>`)
	editor := template.HTML(`<div id="md-ed" style="border:1px dashed currentColor;padding:.5rem;opacity:.5;">[host editor]</div>`)
	rules := []*CommunityRule{
		{ID: 1, Position: 1, Title: "Be kind", Body: "No flaming, no personal attacks."},
		{ID: 2, Position: 2, Title: "Tag spoilers", Body: ""},
	}
	mods := []*CommunityMod{{UserID: 8, Username: "mod-one"}, {UserID: 9, Username: "mod-two"}}

	// Fuller than the fixtures in views_test.go: one row proves the template
	// executes, it does not show a list holding its columns.
	pages := []struct {
		name string
		vm   pageVM
	}{
		{"communities_index.html", &communitiesIndexVM{
			Communities: []*Community{sampleCommunity(), sampleCommunity(), sampleCommunity()},
			Total:       3, Pagination: pager,
		}},
		{"community_new.html", &communityNewVM{
			Error: "That slug is reserved. Please pick another.",
			Slug:  "admin", Name: "Admins", Description: "d",
		}},
		{"community_view.html", &communityViewVM{
			Community:       sampleCommunity(),
			Threads:         []*CommunityThread{sampleThread(1), sampleThread(2), sampleThread(3)},
			Total:           3,
			Pagination:      pager,
			Rules:           rules,
			Mods:            mods,
			Role:            CommunityViewerRole{IsSubscriber: true},
			Flash:           Flash{Code: "saved"},
			SidebarHTML:     template.HTML("<p>Anything the operator wants in the rail.</p>"),
			DescriptionHTML: template.HTML("<p>A place to talk about the season.</p>"),
		}},
		{"community_new_thread_c.html", &communityNewThreadVM{
			Community: sampleCommunity(), Editor: editor,
		}},
		{"community_thread_c.html", &communityThreadVM{
			Community: sampleCommunity(), Thread: sampleThread(1),
			BodyHTML:    template.HTML("<p>That opening was great.</p><p>A second paragraph, so the body has more than one line of prose to set.</p>"),
			Posts:       []threadPostVM{samplePost(10), samplePost(11)},
			Total:       2,
			Pagination:  pager,
			Rules:       rules,
			Mods:        mods,
			Role:        CommunityViewerRole{IsMod: true},
			SidebarHTML: template.HTML("<p>side</p>"),
		}},
		{"community_join_requests.html", &communityJoinRequestsVM{
			Community: sampleCommunity(),
			Requests:  []*CommunityJoinRequest{sampleRequest()},
			Invites:   []*CommunityInvite{sampleInvite()},
		}},
		{"community_settings.html", &communitySettingsVM{
			Community: sampleCommunity(), Flash: Flash{Code: "saved"},
		}},
	}

	for _, pg := range pages {
		body := testRender(t, pg.name, pg.vm)
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
