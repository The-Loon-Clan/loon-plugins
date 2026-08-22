package forum

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Dump each page as a standalone HTML file so it can be opened or screenshotted.
//
// These five templates render only on the RenderPage contract, and the one host
// that exists still wires the legacy BaseData one -- so it serves its OWN copies
// and these are never executed anywhere a person can look at them. That is how
// they came to use 144 class names that no stylesheet defines: nothing renders
// them, so nothing showed the gap.
//
// Reading CSS is not a substitute for looking at a page, so this exists to make
// looking possible. Skipped unless FORUM_PREVIEW_DIR is set, because it writes
// files and proves nothing on its own.
//
//	FORUM_PREVIEW_CSS=a.css,b.css FORUM_PREVIEW_DIR=/tmp/p go test ./forum/ -run Preview
//
// The CSS files are INLINED rather than linked: the output lands in a scratch
// directory far from the stylesheets, and a <link> that 404s renders exactly
// like a stylesheet that defines nothing -- the bug being investigated.
func TestWriteForumPreviews(t *testing.T) {
	dir := os.Getenv("FORUM_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set FORUM_PREVIEW_DIR to write previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var css strings.Builder
	for _, p := range strings.Split(os.Getenv("FORUM_PREVIEW_CSS"), ",") {
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

	// The host injects a sprite sheet and the markup references it with
	// <use href="#name">. Without it every icon is an empty box of the
	// right SIZE, which is exactly the case a screenshot cannot tell apart
	// from an icon that is genuinely missing.
	var sprite []byte
	if sp := os.Getenv("FORUM_PREVIEW_SPRITE"); sp != "" {
		b, err := os.ReadFile(sp)
		if err != nil {
			t.Fatalf("preview sprite: %v", err)
		}
		sprite = b
	}

	for _, pg := range previewPages() {
		body := renderDoc(t, pg.name, pg.data)
		doc := "<!DOCTYPE html>\n<html lang=\"en\" data-theme=\"dark\">\n<head>\n" +
			"<meta charset=\"utf-8\">\n<title>" + pg.name + "</title>\n<style>\n" +
			css.String() + "</style>\n</head>\n" +
			"<body class=\"theme-dark\">\n" + string(sprite) +
			"\n<div class=\"site-container container page\">\n" +
			body + "\n</div>\n</body>\n</html>\n"
		out := filepath.Join(dir, strings.TrimSuffix(pg.name, ".html")+".preview.html")
		if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes of fragment)", out, len(body))
	}
}

type previewPage struct {
	name string
	data map[string]any
}

// Fuller than the fixtures in views_test.go on purpose. One category and one
// post prove the template executes; they do not show a list that has to hold
// its columns, a pinned row beside a locked one, or a long title next to a
// short one, and those are what a stylesheet is judged on.
func previewPages() []previewPage {
	now := time.Now()
	ago := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	sp := func(s string) *string { return &s }
	ip := func(i int) *int { return &i }
	tp := func(t time.Time) *time.Time { return &t }

	cats := []*ForumCategory{
		{ID: 1, Name: "Announcements", Description: "Site news, releases and downtime notices.",
			Color: "blue", Icon: "megaphone", ThreadCount: 12, PostCount: 148,
			LastPostAt: tp(ago(2)), LastThreadID: ip(5),
			LastThreadTitle: sp("Scheduled maintenance this weekend"), LastPostUser: sp("kirisame")},
		{ID: 2, Name: "General Discussion", Description: "Anything that does not fit elsewhere.",
			Color: "green", Icon: "chat", ThreadCount: 340, PostCount: 5218,
			LastPostAt: tp(ago(1)), LastThreadID: ip(9),
			LastThreadTitle: sp("What are you watching this season?"), LastPostUser: sp("marisa")},
		{ID: 3, Name: "Requests & Support", Description: "Ask for help with the indexer, your account, or a release.",
			Color: "orange", Icon: "help", ThreadCount: 87, PostCount: 612,
			LastPostAt: tp(ago(9)), LastThreadID: ip(11),
			LastThreadTitle: sp("Newznab API returns 401 after key rotation"), LastPostUser: sp("reimu")},
		{ID: 4, Name: "Staff Room", Description: "Gated: moderators and above.",
			Color: "red", Icon: "shield", ReadRole: "moderator", WriteRole: "moderator",
			ThreadCount: 4, PostCount: 31},
	}

	threads := []*ForumThread{
		{ID: 5, CategoryID: 1, UserID: 42, Username: "kirisame", Role: "admin",
			Title: "Scheduled maintenance this weekend", Pinned: true, ReplyCount: 23,
			ThreadType: ForumThreadTypeDiscussion, CategoryName: "Announcements",
			LastPostAt: ago(2), CreatedAt: ago(72),
			LastPostUserID: ip(7), LastPostUsername: sp("marisa"), LastPostRole: sp("member")},
		{ID: 6, CategoryID: 1, UserID: 7, Username: "marisa", Role: "member",
			Title:      "A considerably longer thread title, of the sort that has to wrap without pushing the reply and last-post columns off the row",
			ReplyCount: 4, ThreadType: ForumThreadTypeDiscussion, CategoryName: "Announcements",
			LastPostAt: ago(5), CreatedAt: ago(30),
			LastPostUserID: ip(42), LastPostUsername: sp("kirisame"), LastPostRole: sp("admin")},
		{ID: 7, CategoryID: 1, UserID: 9, Username: "reimu", Role: "moderator",
			Title: "Closed: duplicate of the maintenance thread", Locked: true, ReplyCount: 0,
			ThreadType: ForumThreadTypeDiscussion, CategoryName: "Announcements",
			LastPostAt: ago(48), CreatedAt: ago(48)},
		{ID: 8, CategoryID: 1, UserID: 11, Username: "sakuya", Role: "member",
			Title: "Looking for a second uploader for the anime team", ReplyCount: 6,
			ThreadType: ForumThreadTypeRecruitment, CategoryName: "Announcements",
			LastPostAt: ago(12), CreatedAt: ago(96),
			LastPostUserID: ip(9), LastPostUsername: sp("reimu"), LastPostRole: sp("moderator")},
	}

	post := func(id int64, user string, role string, tier int, body string, edited bool) *forumPostView {
		p := &ForumPost{ID: id, ThreadID: 5, UserID: int(id), Username: user, UserRole: role,
			ReputationTier: tier, Body: body, CreatedAt: ago(int(id) * 3),
			UserJoinedAt: ago(9000), UserPostCount: 140 + int(id)*7}
		if edited {
			p.EditedAt = tp(ago(1))
		}
		return &forumPostView{ForumPost: p,
			BodyHTML: template.HTML("<p>" + body + "</p>" +
				"<p>A second paragraph, so the post body has more than one line of prose to set.</p>"),
			EditorHTML: template.HTML(`<div id="post-ed" style="border:1px dashed currentColor;padding:.5rem;opacity:.5;">[host editor]</div>`)}
	}

	posts := []*forumPostView{
		post(1, "kirisame", "admin", 3,
			"The indexer will be read-only from 02:00 UTC on Saturday while the storage move finishes.", false),
		post(2, "marisa", "member", 1,
			"Does that include the Newznab API, or just the web UI?", false),
		post(3, "reimu", "mod", 2,
			"Both. The API returns 503 with a Retry-After for the duration.", true),
	}

	widgets := map[string]any{
		"CSRFToken":       "preview-csrf",
		"PaginationHTML":  template.HTML(`<nav id="pg" style="opacity:.5;">[host pager]</nav>`),
		"ReplyEditorHTML": template.HTML(`<div id="reply-ed" style="border:1px dashed currentColor;padding:.5rem;opacity:.5;">[host reply editor]</div>`),
		"ReportModalHTML": template.HTML(`<div id="report-modal" hidden>[host report modal]</div>`),
		"CurrentUserID":   2,
		"IsAdmin":         true,
	}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range widgets {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	return []previewPage{
		{"community_forums.html", with(map[string]any{
			"Categories": cats,
			"Activity": []*ForumActivityItem{
				{PostID: 9, ThreadID: 5, ThreadTitle: "Scheduled maintenance this weekend",
					UserID: 42, Username: "kirisame", Role: "admin", CategoryID: 1,
					CategoryIcon: "megaphone", CategoryColor: "blue", CreatedAt: ago(2)},
				{PostID: 10, ThreadID: 9, ThreadTitle: "What are you watching this season?",
					UserID: 7, Username: "marisa", Role: "member", CategoryID: 2,
					CategoryIcon: "chat", CategoryColor: "green", CreatedAt: ago(4)},
				{PostID: 11, ThreadID: 11, ThreadTitle: "Newznab API returns 401 after key rotation",
					UserID: 9, Username: "reimu", Role: "moderator", CategoryID: 3,
					CategoryIcon: "help", CategoryColor: "orange", CreatedAt: ago(9)},
			},
			"Contributors": []*ForumContributor{
				{UserID: 42, Username: "kirisame", Role: "admin", PostCount: 812},
				{UserID: 7, Username: "marisa", Role: "member", PostCount: 431},
				{UserID: 9, Username: "reimu", Role: "moderator", PostCount: 288},
			},
			"TotalThreads": 443, "TotalPosts": 6009,
		})},
		{"community_category.html", with(map[string]any{
			"Category": cats[0], "Threads": threads,
			"Total": 12, "Page": 1, "TotalPages": 3,
		})},
		{"community_thread.html", with(map[string]any{
			"Thread": threads[0], "Posts": posts,
			"Total": 23, "Page": 1, "TotalPages": 2, "IsRecruitment": false,
		})},
		{"community_new_thread.html", with(map[string]any{
			"Categories": cats, "SelectedCategory": 2,
			"EditorHTML": template.HTML(`<div id="new-ed" style="border:1px dashed currentColor;padding:.5rem;opacity:.5;">[host editor]</div>`),
		})},
		{"forum_error.html", with(map[string]any{"Reason": "loadfailed"})},
		{"admin_forum_categories.html", with(map[string]any{
			"Categories": cats, "Colors": categoryColorList, "GateRoles": gateRoleList,
			"Flash": "", "Err": "",
		})},
	}
}
