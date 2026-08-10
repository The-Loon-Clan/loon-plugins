package uploads

import (
	"strings"
	"testing"
)

func fullVM() vm {
	return vm{
		Uploads: []Upload{
			{ID: 1, Title: "A Release", Kind: "nzb", SizeBytes: 1610612736,
				CreatedAt: "10 Aug 2026", Anonymous: false, Deleted: false, Queued: true},
			{ID: 2, Title: "Hidden One", Kind: "private-torrent", SizeBytes: 524288,
				CreatedAt: "09 Aug 2026", Anonymous: true, Deleted: true},
		},
		Total: 2, Page: 1, CSRFToken: "test-csrf",
		PaginationHTML: "<nav>pager</nav>",
		Flash:          "upload updated",
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

func TestPageRenders(t *testing.T) {
	out := render(t, fullVM())
	for _, want := range []string{
		"A Release", "/release/1", "1.5 GB", // size is humanised, not raw bytes
		"10 Aug 2026", "queued",
		"Hidden One", "anonymous",
		"<nav>pager</nav>", "upload updated",
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
	out := render(t, vm{Page: 1, CSRFToken: "t"})
	if !strings.Contains(out, "not uploaded anything yet") {
		t.Errorf("empty state missing:\n%s", out)
	}
	// The sweeping-actions card must NOT appear with nothing to sweep — offering
	// "delete all" to somebody with no uploads is noise at best.
	if strings.Contains(out, "Delete all") {
		t.Error("bulk actions offered with an empty list")
	}
}

// Every POST form needs the token or it 403s on submit. Counting rather than
// spot-checking, because the bug this catches is a form ADDED later without
// one — which is exactly how the discord settings form shipped broken.
func TestEveryPostFormCarriesCSRF(t *testing.T) {
	out := render(t, fullVM())
	forms := strings.Count(out, `method="POST"`)
	tokens := strings.Count(out, `name="_csrf"`)
	if forms == 0 {
		t.Fatal("no POST forms rendered — the test would pass vacuously")
	}
	if forms != tokens {
		t.Errorf("%d POST form(s) but %d _csrf field(s) — one would 403 on submit", forms, tokens)
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
