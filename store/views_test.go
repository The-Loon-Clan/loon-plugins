package store

import (
	"github.com/the-loon-clan/loon/core"
	"html/template"
	"strings"
	"testing"
)

// Lifted markup, so executing it is the only thing that proves the data these
// handlers pass still matches what it reads. html/template streams: a field
// the markup wants and the data lacks aborts the render part way through, and
// the caller gets a 200 carrying half a page with nothing logged.

func render(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	data["CSRFToken"] = "test-csrf"
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := sb.String()
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	// Body content only — the host wrapper owns the document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return s
}

func TestStorePageRenders(t *testing.T) {
	got := render(t, "store.html", map[string]any{
		"Items": []Item{
			{ID: 1, Name: "Shiny Badge", Description: "it shines", PointsCost: 500, Active: true, Stock: -1},
		},
		"Balance": 1200,
		"Error":   "",
		"Ok":      "",
	})
	for _, want := range []string{"Shiny Badge", "500", "test-csrf"} {
		if !strings.Contains(got, want) {
			t.Errorf("store page missing %q", want)
		}
	}
}

func TestStoreHistoryRenders(t *testing.T) {
	got := render(t, "store_history.html", map[string]any{
		"Entries":        []core.LedgerEntry{{Amount: -500, Balance: 700, Type: "spend_store_purchase", Description: "bought a badge"}},
		"Total":          1,
		"Balance":        700,
		"PaginationHTML": template.HTML(`<nav id="pg"></nav>`),
	})
	for _, want := range []string{"bought a badge", `<nav id="pg">`} {
		if !strings.Contains(got, want) {
			t.Errorf("history page missing %q", want)
		}
	}
}

func TestAdminStoreRenders(t *testing.T) {
	got := render(t, "admin_store.html", map[string]any{
		"Items": []Item{
			{ID: 1, Name: "Shiny Badge", PointsCost: 500, Active: true, Stock: -1},
		},
	})
	for _, want := range []string{"Shiny Badge", "test-csrf"} {
		if !strings.Contains(got, want) {
			t.Errorf("admin store missing %q", want)
		}
	}
}

// The empty state is what an operator sees on a fresh install, and a range
// over nothing is where a missing {{else}} shows up.
func TestStorePagesRenderEmpty(t *testing.T) {
	render(t, "store.html", map[string]any{"Items": []Item{}, "Balance": 0})
	render(t, "store_history.html", map[string]any{
		"Entries": []core.LedgerEntry{}, "Total": 0, "Balance": 0,
		"PaginationHTML": template.HTML("")})
	render(t, "admin_store.html", map[string]any{"Items": []Item{}})
}

// The tab strip, and the two ways a host can end up without one.
//
// Both pages are checked rather than one, because the strip is duplicated in
// their markup — the failure this catches is a host suppressing it and still
// getting a stray row on the page nobody re-read.
func TestTheTabStripIsOnByDefaultAndOffWhenSuppressed(t *testing.T) {
	for _, page := range []struct {
		name string
		data map[string]any
	}{
		{"store.html", map[string]any{"Items": []Item{}, "Balance": 0}},
		{"store_history.html", map[string]any{
			"Entries": []core.LedgerEntry{}, "Total": 0, "Balance": 0,
			"PaginationHTML": template.HTML("")}},
	} {
		// A host that never heard of the seam. The key is ABSENT rather than
		// false on purpose: that is the state every existing caller is in, and
		// the one a positive ShowTabs would have silently broken.
		if got := render(t, page.name, page.data); !strings.Contains(got, `class="nav tabs`) {
			t.Errorf("%s: no tab strip with the seam unset — every host that "+
				"does not know about SuppressTabs just lost its navigation", page.name)
		}

		page.data["HideTabs"] = true
		got := render(t, page.name, page.data)
		if strings.Contains(got, `class="nav tabs`) {
			t.Errorf("%s: the strip rendered with HideTabs set, so a host with its "+
				"own account bar gets two rows of tabs offering the same pages", page.name)
		}
		// The page still has to work without it — the strip is navigation, not
		// content, and suppressing it must not take the page with it.
		if !strings.Contains(got, "pts") {
			t.Errorf("%s: lost its body along with the strip", page.name)
		}
	}
}
