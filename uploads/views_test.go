package uploads

import (
	"strings"
	"testing"
)

func fullVM() vm {
	return vm{
		ActiveTab: tabPublic,
		Public: []Upload{
			{ID: 1, Title: "A Release", Kind: "nzb", SizeBytes: 1610612736,
				CreatedAt: "10 Aug 2026", Queued: true},
			{ID: 2, Title: "Hidden One", Kind: "nzb", SizeBytes: 524288,
				CreatedAt: "09 Aug 2026", Anonymous: true, Deleted: true},
		},
		PublicTotal:      2,
		PublicPagination: "<nav>pager</nav>",
		PrivateNZB: []Upload{
			{ID: 30, Title: "A Private NZB", Kind: "private-nzb", SizeBytes: 1048576,
				CreatedAt: "08 Aug 2026"},
		},
		PrivateNZBTotal: 1,
		PrivateNZBPager: "<nav>npager</nav>",
		PrivateTorrent: []Upload{
			{ID: 40, Title: "A Private Torrent", Kind: "private-torrent",
				CreatedAt: "07 Aug 2026", InfoHash: "abc123def456", KeptPrivate: true},
		},
		PrivateTorrentTotal: 1,
		PrivateTorrentPager: "<nav>tpager</nav>",
		CSRFToken:           "test-csrf",
		Flash:               "uploadupdated",
	}
}

func render(t *testing.T, v vm) string {
	t.Helper()
	var b strings.Builder
	if err := pageTmpl.ExecuteTemplate(&b, "uploads.html", v); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

// A failed save must not arrive in the success colour. It did for a long time:
// the flash block was hardcoded to green, so "that did not save" and "upload
// updated" looked identical at a glance, and a member would have read a lost
// write as a saved one. The code is what lets the template tell them apart.
func TestFailureFlashIsNotShownAsSuccess(t *testing.T) {
	v := fullVM()
	v.Flash = "savefailed"
	out := render(t, v)
	if !strings.Contains(out, "did not save") {
		t.Error("a failed save said nothing")
	}
	if !strings.Contains(out, "notice--warning") {
		t.Error("a failed save rendered as a success notice")
	}
	v.Flash = "uploadupdated"
	if out := render(t, v); !strings.Contains(out, "notice--success") {
		t.Error("a successful save did not render as a success notice")
	}
}

func TestPageRenders(t *testing.T) {
	out := render(t, fullVM())
	for _, want := range []string{
		"A Release", "/release/1", "1.5 GB", // size is humanised, not raw bytes
		"10 Aug 2026", "queued",
		"Hidden One", "anonymous",
		"<nav>pager</nav>", "Upload updated.",
		"Delete", "Restore", // a live row offers delete, a deleted row offers restore
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// html/template streams, so a field the markup reads and the handler forgot
// truncates the page silently. An empty VM must still render a whole page.
func TestPageRendersEmpty(t *testing.T) {
	out := render(t, vm{ActiveTab: tabPublic, CSRFToken: "t"})
	if !strings.Contains(out, "not uploaded anything publicly yet") {
		t.Errorf("empty state missing:\n%s", out)
	}
	// The sweeping-actions card must NOT appear with nothing to sweep — offering
	// "delete all" to somebody with no uploads is noise at best.
	if strings.Contains(out, "Delete all") {
		t.Error("bulk actions offered with an empty list")
	}
}

// Every POST form carries the token. Counting rather than spot-checking,
// because the bug this catches is a form ADDED later without one.
//
// The host's csrf-js would inject a missing input at submit time, so this is
// not the difference between working and 403 on that host — it is the
// difference between working everywhere and working only where that partial
// ships and JavaScript runs.
func TestEveryPostFormCarriesCSRF(t *testing.T) {
	out := render(t, fullVM())
	forms := strings.Count(out, `method="POST"`)
	tokens := strings.Count(out, `name="_csrf"`)
	if forms == 0 {
		t.Fatal("no POST forms rendered — the test would pass vacuously")
	}
	if forms != tokens {
		t.Errorf("%d POST form(s) but %d _csrf field(s) — one relies on the host's JS", forms, tokens)
	}
}

// The irreversible action must be gated in the markup as well as the handler:
// a confirm field the handler demands, and a browser confirm the member sees.
func TestPermanentAnonymousIsGuarded(t *testing.T) {
	out := render(t, fullVM())
	i := strings.Index(out, `value="true-anonymous"`)
	if i < 0 {
		t.Fatal("the permanent action is not offered at all")
	}
	form := out[max(0, i-700):i]
	if !strings.Contains(form, `name="confirm" value="permanent"`) {
		t.Error("no confirm field — the handler refuses the action, so the button would silently do nothing")
	}
	if !strings.Contains(form, "onsubmit") {
		t.Error("no browser confirmation on an irreversible action")
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		512: "512 B", 1536: "1.5 KB", 1048576: "1.0 MB",
		1610612736: "1.5 GB", 1099511627776: "1.0 TB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// The flash goes into a URL and comes back out into the page, so it must not be
// able to carry markup or break out of the query string.
func TestFlashEscaping(t *testing.T) {
	got := urlQueryEscape(`<script>alert(1)</script> & more`)
	for _, bad := range []string{"<", ">", "&", `"`} {
		if strings.Contains(got, bad) {
			t.Errorf("escaped flash still contains %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "%3C") {
		t.Errorf("expected percent-encoding, got %s", got)
	}
}

// Each tab must render its OWN content. This is the test that would have caught
// the regression this page shipped with for one commit: the first cut served a
// single merged list and ignored ?tab=, so the account-settings links straight
// to ?tab=private-nzb and ?tab=private-torrent silently showed the public list
// instead. Three tabs, three sources, three pagers.
func TestEachTabRendersItsOwnContent(t *testing.T) {
	cases := []struct {
		tab      string
		wantSome []string
		wantNone []string
	}{
		{tabPublic,
			[]string{"A Release", "<nav>pager</nav>", "Public uploads"},
			[]string{"A Private NZB", "A Private Torrent", "abc123def456"}},
		{tabPrivateNZB,
			[]string{"A Private NZB", "<nav>npager</nav>", "/community/request/30?private=1"},
			[]string{"A Release", "A Private Torrent"}},
		{tabPrivateTorrent,
			[]string{"A Private Torrent", "<nav>tpager</nav>", "abc123def456", "Keep private"},
			[]string{"A Release", "A Private NZB"}},
	}
	for _, tc := range cases {
		t.Run(tc.tab, func(t *testing.T) {
			v := fullVM()
			v.ActiveTab = tc.tab
			out := render(t, v)
			for _, w := range tc.wantSome {
				if !strings.Contains(out, w) {
					t.Errorf("tab %s is missing %q", tc.tab, w)
				}
			}
			for _, w := range tc.wantNone {
				if strings.Contains(out, w) {
					t.Errorf("tab %s leaked another tab's content: %q", tc.tab, w)
				}
			}
			// Every tab shows all three counts, so a member can see there is
			// something in the others without clicking through.
			for _, count := range []string{">2<", ">1<"} {
				if !strings.Contains(out, count) {
					t.Errorf("tab %s does not show the other tabs' counts", tc.tab)
				}
			}
		})
	}
}

// The torrent tab's visibility form is a POST like any other and needs its
// token — it was added after the first CSRF count test was written, which is
// exactly the case that test exists to catch.
func TestTorrentTabFormCarriesCSRF(t *testing.T) {
	v := fullVM()
	v.ActiveTab = tabPrivateTorrent
	out := render(t, v)
	forms := strings.Count(out, `method="POST"`)
	tokens := strings.Count(out, `name="_csrf"`)
	if forms == 0 {
		t.Fatal("no POST form on the torrent tab — the test would pass vacuously")
	}
	if forms != tokens {
		t.Errorf("%d POST form(s) but %d _csrf field(s)", forms, tokens)
	}
}
