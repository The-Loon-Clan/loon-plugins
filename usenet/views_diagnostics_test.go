package usenet

import (
	"strings"
	"testing"
)

func diagRows(n int) []filterHitRow {
	rows := make([]filterHitRow, n)
	for i := range rows {
		rows[i] = filterHitRow{Kind: "ungrouped", Rule: "stem#", TotalCount: int64(n - i)}
	}
	return rows
}

// The card's whole job is to not be mistaken for the rule list, and the
// number that does that work is the denominator. A page that says "25 stems"
// when there are 2,260 recreates the exact misreading the split was for.
func TestRangeStatesWhatWasNotShown(t *testing.T) {
	full := buildDiagVM(diagPage{Rows: diagRows(diagPageSize), TotalRows: 2260}, "", 1)
	if got := full.Range(); got != "1-25 of 2,260 distinct stem(s)" {
		t.Errorf("page 1 range = %q", got)
	}
	// Page 3 of the same set: the offset has to move with the page, or every
	// page claims to be the first.
	p3 := buildDiagVM(diagPage{Rows: diagRows(diagPageSize), TotalRows: 2260}, "", 3)
	if got := p3.Range(); got != "51-75 of 2,260 distinct stem(s)" {
		t.Errorf("page 3 range = %q", got)
	}
	// A set that fits on one page has nothing hidden, so it must not imply it.
	small := buildDiagVM(diagPage{Rows: diagRows(4), TotalRows: 4}, "", 1)
	if got := small.Range(); got != "4 distinct stem(s)" {
		t.Errorf("small range = %q", got)
	}
	if empty := (buildDiagVM(diagPage{}, "", 1)).Range(); empty != "none recorded" {
		t.Errorf("empty range = %q", empty)
	}
}

// The last page is short. Its Last must be the real final row, not
// page*pageSize — that would claim rows the table does not have.
func TestLastPageDoesNotOverstateItsRange(t *testing.T) {
	vm := buildDiagVM(diagPage{Rows: diagRows(7), TotalRows: 2257}, "", 91)
	if vm.First != 2251 || vm.Last != 2257 {
		t.Errorf("last page range = %d-%d, want 2251-2257", vm.First, vm.Last)
	}
	if vm.Pages != 91 {
		t.Errorf("pages = %d, want 91", vm.Pages)
	}
	if vm.NextURL != "" {
		t.Error("the last page offered a next link")
	}
	if vm.PrevURL == "" {
		t.Error("the last page has no prev link")
	}
}

func TestFirstPageHasNoPrev(t *testing.T) {
	vm := buildDiagVM(diagPage{Rows: diagRows(diagPageSize), TotalRows: 100}, "", 1)
	if vm.PrevURL != "" {
		t.Errorf("page 1 offered a prev link: %q", vm.PrevURL)
	}
	if vm.NextURL == "" {
		t.Error("page 1 of 4 has no next link")
	}
}

// Links carry the tab anchor: the admin page renders every tab at once and
// selects client-side, so a link without #filters lands the operator on the
// Providers tab wondering what they clicked.
func TestLinksKeepKindAndTab(t *testing.T) {
	vm := buildDiagVM(diagPage{
		Rows:      diagRows(diagPageSize),
		Kinds:     []diagKind{{Kind: "ungrouped", Rows: 2255}, {Kind: "merge_suspect", Rows: 92}},
		TotalRows: 2255, // the selected kind's count, as the store computes it
	}, "ungrouped", 2)

	for _, u := range []string{vm.PrevURL, vm.NextURL, vm.AllURL, vm.Kinds[0].URL} {
		if !strings.HasSuffix(u, "#filters") {
			t.Errorf("link %q does not return to the filters tab", u)
		}
	}
	if !strings.Contains(vm.NextURL, "dkind=ungrouped") || !strings.Contains(vm.NextURL, "dpage=3") {
		t.Errorf("next lost the selected kind or page: %q", vm.NextURL)
	}
	// Paging back to 1 drops the page param rather than writing dpage=1, so the
	// first page has one canonical URL.
	if vm.PrevURL != "?dkind=ungrouped#filters" {
		t.Errorf("prev to page 1 = %q", vm.PrevURL)
	}
	// The "all" chip clears the filter entirely.
	if vm.AllURL != "?#filters" {
		t.Errorf("all chip = %q, want no params", vm.AllURL)
	}
	if !vm.Kinds[0].Active || vm.Kinds[1].Active {
		t.Errorf("wrong chip marked active: %+v", vm.Kinds)
	}
}

// ?dpage= is operator-supplied. Garbage must not become a negative OFFSET.
func TestParseDiagPageFloorsAtOne(t *testing.T) {
	for _, raw := range []string{"", "0", "-5", "abc", "  ", "1e9x"} {
		if got := parseDiagPage(raw); got != 1 {
			t.Errorf("parseDiagPage(%q) = %d, want 1", raw, got)
		}
	}
	if got := parseDiagPage(" 7 "); got != 7 {
		t.Errorf("parseDiagPage(\" 7 \") = %d, want 7", got)
	}
}

// A stale bookmark past the end should show the tail, not an empty card.
func TestClampDiagPagePullsBackToTheLastPage(t *testing.T) {
	if got := clampDiagPage(500, 60); got != 3 {
		t.Errorf("page 500 of 60 rows = %d, want 3", got)
	}
	if got := clampDiagPage(2, 60); got != 2 {
		t.Errorf("an in-range page was moved: %d", got)
	}
	// No rows at all: there is no last page to fall back to, so page 1 stands
	// and the card renders its empty state.
	if got := clampDiagPage(9, 0); got != 1 {
		t.Errorf("empty set clamped to %d, want 1", got)
	}
}
