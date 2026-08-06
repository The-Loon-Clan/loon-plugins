package lists

import (
	"html/template"
	"strings"
	"testing"
	"time"
)

// Every fragment is EXECUTED here, not just compiled.
//
// html/template streams: a field the markup reads and the data does not carry
// aborts the render part way through. Left to a handler that would be a 200
// carrying half a page, with nothing logged. These four tests are what make
// the plugin's own List type safe to narrow — if a template still wants a
// field that was left behind on the host's record, it fails right here.

func sampleList(id int, name string) List {
	return List{
		ID: id, Name: name, Description: "desc " + name, Public: true,
		Username: "kirisame", ItemCount: 4, CoverURL: "/covers/" + name + ".jpg",
		DownloadCount: 9, FollowCount: 2,
		CreatedAt: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		UserID:    id * 10,
	}
}

// renderOK executes one fragment and fails loudly if it truncates.
func renderOK(t *testing.T, name string, vm any) string {
	t.Helper()
	out, err := render(name, vm)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := string(out)
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	// The fragment is body content. The host wrapper owns the document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", "<body", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return s
}

func TestUserListsFragment(t *testing.T) {
	got := renderOK(t, "user_lists.html", userListsVM{
		Lists:    []List{sampleList(1, "alpha"), sampleList(2, "beta")},
		Followed: []List{sampleList(3, "gamma")},
	})
	// Both owned lists AND the followed one — a streamed abort usually loses
	// whichever comes second.
	for _, want := range []string{"alpha", "beta", "gamma", "/lists/1", "kirisame"} {
		if !strings.Contains(got, want) {
			t.Errorf("user_lists missing %q", want)
		}
	}
}

func TestListDetailFragment(t *testing.T) {
	l := sampleList(7, "mylist")
	got := renderOK(t, "list_detail.html", listDetailVM{
		List: &l,
		Items: []Item{{ID: 1, Filename: "a.nzb", Card: template.HTML(`<div id="c1">A</div>`)},
			{ID: 2, Filename: "b.nzb", Card: template.HTML(`<div id="c2">B</div>`)}},
		IsFollowing: false,
		// A signed-in viewer who is NOT the owner: the only combination that
		// shows the report control, and the one that silently vanished when
		// these templates moved off the host's page data.
		IsOwner:     false,
		ViewerID:    999,
		NzbCardCSS:  template.HTML(`<style id="cardcss"></style>`),
		ReportModal: template.HTML(`<div id="reportmodal"></div>`),
	})
	for _, want := range []string{
		"mylist",
		`<div id="c1">`, `<div id="c2">`, // both host-rendered cards landed
		`id="cardcss"`, `id="reportmodal"`, // host chrome injected, not re-implemented
		"Report list", // gated on ViewerID; was silently lost with $.User
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list_detail missing %q", want)
		}
	}
}

func TestReleaseListsFragment(t *testing.T) {
	got := renderOK(t, "release_lists.html", releaseListsVM{
		Nzb:   &NzbRef{ID: 42, Title: "Some.Release.1080p"},
		Lists: []List{sampleList(1, "alpha")},
	})
	for _, want := range []string{"Some.Release.1080p", "alpha", "/lists/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("release_lists missing %q", want)
		}
	}
}

func TestCommunityWatchlistsFragment(t *testing.T) {
	got := renderOK(t, "community_watchlists.html", watchlistsVM{
		Lists:      []List{sampleList(1, "alpha"), sampleList(2, "beta")},
		NzbCardCSS: template.HTML(`<style id="cardcss"></style>`),
	})
	for _, want := range []string{"alpha", "beta", `id="cardcss"`} {
		if !strings.Contains(got, want) {
			t.Errorf("community_watchlists missing %q", want)
		}
	}
}

// An empty result must still render the whole page — the empty state is the
// case an operator sees on a fresh install, and a range over nothing is where
// a missing {{else}} shows up.
func TestFragmentsRenderEmpty(t *testing.T) {
	renderOK(t, "user_lists.html", userListsVM{})
	renderOK(t, "community_watchlists.html", watchlistsVM{})
	l := sampleList(1, "empty")
	renderOK(t, "list_detail.html", listDetailVM{List: &l})
	renderOK(t, "release_lists.html", releaseListsVM{Nzb: &NzbRef{ID: 1, Title: "x"}})
}
