package tracker

import (
	"fmt"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func renderFixture(t *testing.T) *Plugin {
	t.Helper()
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return p
}

// The admin page renders to completion with real rows.
//
// html/template STREAMS, so a field the view model lacks truncates the page at
// that row rather than erroring — which is why the assertions include content from
// the LAST table on the page.
func TestAdminPageRenders(t *testing.T) {
	p := renderFixture(t)
	seen := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	added := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	out, err := p.exec(adminVM{
		Now: seen,
		Members: []memberVM{
			{Aggregate: &Aggregate{UserID: 7, Uploaded: 5 << 30, Downloaded: 1 << 30,
				ActiveCount: 2, TorrentCount: 9, SnatchedCount: 4, LastSeen: &seen},
				Username: "ameNZB", Entitled: true, Ratio: 5},
			// No access and no activity: the row that must render "—" for the
			// ratio rather than "0.00", and must be the one carrying a tag.
			{Aggregate: &Aggregate{UserID: 8}, Username: "lurker"},
		},
		Torrents: []*Torrent{
			{InfoHash: strings.Repeat("a", 40), Name: "Some.Release-GRP",
				Size: 3 << 30, FileCount: 12, Seeders: 4, Leechers: 1, Snatches: 20, AddedAt: added},
		},
		Total: 1,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	for _, want := range []string{
		"Members", "ameNZB", "lurker",
		"5.0 GiB", "1.0 GiB", // bytes rendered, not raw integers
		"5.00",              // the ratio
		"no tracker access", // the EXCEPTION is what gets flagged — see below
		// From the LAST table, so its presence proves the render reached the end.
		"Torrents", "Some.Release-GRP", "3.0 GiB",
		"4/1", // the swarm tag
		// The final cell of the final row. Anything earlier can pass while the
		// stream truncated on the way to it, which is the failure this whole
		// test exists for. renderFixture wires no deps, so `since` falls back
		// to the absolute stamp.
		"2026-08-01 12:00",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q — if the earlier rows rendered, the template aborted mid-stream", want)
		}
	}
	// Entitlement is shown by EXCEPTION, which is a deliberate change from the
	// green "entitled" badge that used to sit on every row.
	//
	// A column that reads the same on every row carries no information — except
	// on the row where it does not, and that row is the entire reason a mod
	// opened this page: somebody still accumulating ratio after their access was
	// revoked. Marking only the exception makes it the thing that stands out
	// instead of the thing that blends in.
	//
	// Asserted as a COUNT rather than mere presence, because "no tracker access"
	// appearing once proves nothing about which row it landed on: two members
	// here, one entitled and one not.
	if n := strings.Count(html, "no tracker access"); n != 1 {
		t.Errorf("the access tag appears %d times for one unentitled member of two", n)
	}
	// A zero ratio must read as "—", never "0.00": an inactive member has no
	// ratio rather than a ratio of nothing.
	if strings.Contains(html, "0.00") {
		t.Error("a zero ratio rendered as 0.00 instead of —")
	}
	// Raw byte integers must not leak through the formatter.
	if strings.Contains(html, "5368709120") {
		t.Error("a raw byte count reached the page")
	}
}

// The idle case must SAY it is idle. Provision registers no routes without Redis,
// so an empty table would read as "a tracker nobody uses" when the truth is "a
// tracker that cannot run" — and those need different actions.
func TestIdlePageExplainsItself(t *testing.T) {
	p := renderFixture(t)
	out, err := p.exec(adminVM{Now: time.Now(), Idle: true})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, "idle") || !strings.Contains(html, "Redis") {
		t.Error("the idle page does not say why the tracker is not running")
	}
	// And it must not also render empty tables underneath, which would muddle the
	// message it just gave.
	if strings.Contains(html, "No torrents registered") {
		t.Error("the idle page also rendered the empty tables")
	}
}

// An empty-but-working tracker says what makes a row appear, rather than showing a
// blank table an operator has to interpret.
func TestEmptyPageSaysWhatToExpect(t *testing.T) {
	p := renderFixture(t)
	out, err := p.exec(adminVM{Now: time.Now()})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, "tracker.access") {
		t.Error("the empty members table does not mention the entitlement that populates it")
	}
	if !strings.Contains(html, "No torrents registered") {
		t.Error("the empty torrents table has no message")
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {-1, "0 B"}, {512, "512 B"},
		{1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1 << 30, "1.0 GiB"}, {1 << 40, "1.0 TiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The member fragments must EXECUTE against the real PageData.
//
// This is the test the restructure was for. The first version of these pages
// rendered templates out of the host's set, and those referenced
// Totals.ActiveCount and Totals.TorrentCount — fields this plugin does not have.
// html/template streams, so a missing field does not fail the request: it
// truncates the page mid-row and returns 200. Nothing at compile time catches it
// and it looks like a rendering glitch rather than a wiring bug.
func TestMemberFragmentsRenderWithRealData(t *testing.T) {
	SetDeps(Deps{
		RenderPage:   func(*gin.Context, string, template.HTML) {},
		CSRFToken:    func(*gin.Context) string { return "tok" },
		RelativeTime: func(time.Time) string { return "3 hours ago" },
	})
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	data := PageData{
		Torrents: []*Torrent{{
			InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "Some Release",
			Size: 5 << 30, FileCount: 12, Seeders: 4, Leechers: 2, Snatches: 7, AddedAt: now,
		}},
		Total: 1,
		Rows: []*UserStat{{
			InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "Some Release",
			Uploaded: 3 << 30, Downloaded: 1 << 30, LeftBytes: 0, LastSeen: now,
		}},
		Totals:      Totals{Uploaded: 3 << 30, Downloaded: 1 << 30, Seeding: 2, Leeching: 1, Snatched: 5},
		Passkey:     "deadbeefdeadbeefdeadbeefdeadbeef",
		AnnounceURL: "https://example.test/api/tracker/announce/deadbeefdeadbeefdeadbeefdeadbeef",
		CSRFToken:   "tok",
	}

	for _, tc := range []struct{ tmpl, wants string }{
		{"tracker_list.html", "Some Release"},
		{"tracker_stats.html", "Rotate passkey"},
	} {
		t.Run(tc.tmpl, func(t *testing.T) {
			var sb strings.Builder
			// Executing a template with a field the model lacks is an ERROR here
			// (not silent) only because we check it — which is the point.
			if err := p.tmpl.ExecuteTemplate(&sb, tc.tmpl, data); err != nil {
				t.Fatalf("%s: %v", tc.tmpl, err)
			}
			out := sb.String()
			if !strings.Contains(out, tc.wants) {
				t.Errorf("%s did not render %q", tc.tmpl, tc.wants)
			}
			// A truncated stream is the failure mode: assert the LAST thing in
			// the fragment made it out, not merely that something did.
			if !strings.Contains(out, "3.00") {
				t.Errorf("%s: ratio missing — Totals.Ratio did not render: %s", tc.tmpl, out)
			}
			if strings.Contains(out, "ActiveCount") || strings.Contains(out, "TorrentCount") {
				t.Errorf("%s leaked a field name into the output", tc.tmpl)
			}
		})
	}
}

// The rotate form carries the host's token. Without it the POST is refused by
// CSRF and the button silently does nothing.
func TestStatsFragmentCarriesTheCSRFToken(t *testing.T) {
	SetDeps(Deps{
		RenderPage:   func(*gin.Context, string, template.HTML) {},
		CSRFToken:    func(*gin.Context) string { return "the-token" },
		RelativeTime: func(time.Time) string { return "now" },
	})
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "tracker_stats.html", PageData{
		Totals: Totals{}, CSRFToken: "the-token",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `name="_csrf" value="the-token"`) {
		t.Error("the rotate form has no CSRF token; the POST would be refused")
	}
}

// The TORRENT PAGE links to the release it was made from — and only then.
//
// This link used to be the list's torrent name, and moved when the torrent got
// a page of its own: the name now goes to that page (which every torrent has),
// and the release cross-link lives where there is room to label it. The
// guarantee is unchanged and still worth holding.
//
// Three cases, because the failure modes differ and two of them are silent. A
// torrent uploaded directly has no nzb_id; a host that wires no ReleaseURL seam
// has no page to point at. Either one emitting <a href=""> gives a member a link
// that reloads the page they are on, which reads as the site being broken rather
// than as there being nothing to link to.
func TestTorrentPageLinksToItsReleaseOnlyWhenThereIsOne(t *testing.T) {
	id := int64(4242)
	for _, tc := range []struct {
		name     string
		nzbID    *int64
		seam     func(int64) string
		wantLink bool
	}{
		{"a torrent made from a release, on a host that has release pages",
			&id, func(n int64) string { return fmt.Sprintf("/release/%d", n) }, true},
		{"a torrent uploaded directly, so there is no release behind it",
			nil, func(n int64) string { return fmt.Sprintf("/release/%d", n) }, false},
		{"a host that wired no seam, so it has nowhere to point",
			&id, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetDeps(Deps{
				RenderPage:   func(*gin.Context, string, template.HTML) {},
				CSRFToken:    func(*gin.Context) string { return "tok" },
				RelativeTime: func(time.Time) string { return "now" },
				ReleaseURL:   tc.seam,
			})
			t.Cleanup(func() { deps = nil })

			p := &Plugin{}
			if err := p.parseTemplates(); err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			if err := p.tmpl.ExecuteTemplate(&sb, "tracker_torrent.html", PageData{
				Torrent: &Torrent{
					InfoHash: strings.Repeat("b", 40), Name: "Linked.Release-GRP",
					Size: 1 << 30, FileCount: 3, NzbID: tc.nzbID, AddedAt: time.Now(),
				},
			}); err != nil {
				t.Fatal(err)
			}
			out := sb.String()
			if got := strings.Contains(out, `href="/release/4242"`); got != tc.wantLink {
				t.Errorf("release link present = %v, want %v", got, tc.wantLink)
			}
			// Whichever branch ran, the name itself must be on the page. An
			// empty <a> or a dropped <span> would both still pass the check
			// above.
			if !strings.Contains(out, "Linked.Release-GRP") {
				t.Error("the torrent name did not render at all")
			}
			if strings.Contains(out, `href=""`) {
				t.Error("an empty href reached the page — a link that reloads this page")
			}
		})
	}
}

// The torrent page is the answer to "a member should not have to type a hash".
// Every per-torrent action hangs off it, and the promotions panel is the one
// that only exists when a sibling plugin answered.
func TestTorrentPageOffersItsActionsAndItsHistory(t *testing.T) {
	SetDeps(Deps{
		RenderPage:   func(*gin.Context, string, template.HTML) {},
		CSRFToken:    func(*gin.Context) string { return "tok" },
		RelativeTime: func(time.Time) string { return "now" },
	})
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 40)
	tor := &Torrent{InfoHash: hash, Name: "Some.Torrent-GRP", Size: 1 << 30,
		FileCount: 4, Seeders: 2, Leechers: 1, Snatches: 7, AddedAt: time.Now()}

	render := func(data PageData) string {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, "tracker_torrent.html", data); err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}

	// With magic: the cast link carries the hash, and the history is drawn —
	// all three states, since "expired" and "terminated" look the same to a
	// member and must not to an operator.
	ends := time.Now().Add(24 * time.Hour)
	out := render(PageData{Torrent: tor, HasMagic: true, Promotions: []pluginapi.TorrentPromotion{
		{Caster: "bob", Scope: "private", UpRatio: 1, DownRatio: 0, Until: ends, Active: true},
		{Caster: "alice", Scope: "public", UpRatio: 2, DownRatio: 1, Until: time.Now().Add(-time.Hour)},
		{Scope: "user", UpRatio: 1, DownRatio: 0, Terminated: true},
	}})
	for _, want := range []string{
		`href="/p/magic?hash=` + hash,     // no hash to type — the whole point
		`href="/tracker/download/` + hash, // and the .torrent from the same page
		hash,                              // shown once, for a client that wants it
		"Some.Torrent-GRP", "bob", "alice",
		">active<", ">expired<", ">terminated<",
		"a departed member", // the caster whose account is gone
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the torrent page is missing %q", want)
		}
	}

	// Without magic: no panel and no button, rather than a link to a page this
	// host does not serve.
	out = render(PageData{Torrent: tor})
	for _, absent := range []string{"/p/magic", "Promotions"} {
		if strings.Contains(out, absent) {
			t.Errorf("a host with no magic plugin still rendered %q", absent)
		}
	}
	// The page still has to be worth opening without it.
	if !strings.Contains(out, "Some.Torrent-GRP") || !strings.Contains(out, "/tracker/download/") {
		t.Error("the page lost its own content along with the promotions panel")
	}

	// Magic installed but nothing cast yet: the panel says so, because that is
	// exactly the torrent somebody would want to promote.
	out = render(PageData{Torrent: tor, HasMagic: true})
	if !strings.Contains(out, "No promotions have been cast") {
		t.Error("an unpromoted torrent got no empty state")
	}
}

// The list is how the page is reached, so the name has to point at it.
func TestTorrentListNameLinksToTheTorrentPage(t *testing.T) {
	SetDeps(Deps{
		RenderPage:   func(*gin.Context, string, template.HTML) {},
		CSRFToken:    func(*gin.Context) string { return "tok" },
		RelativeTime: func(time.Time) string { return "now" },
	})
	t.Cleanup(func() { deps = nil })

	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("d", 40)
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "tracker_list.html", PageData{
		Torrents: []*Torrent{{InfoHash: hash, Name: "Row.Name-GRP", Size: 1 << 30, AddedAt: time.Now()}},
		Total:    1,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `href="/tracker/t/`+hash+`"`) {
		t.Error("the list's torrent name does not reach the torrent page")
	}
}
