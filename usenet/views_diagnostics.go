package usenet

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The grouping diagnostics card.
//
// These counters are observations, not rules: nothing they name was dropped.
// The card exists to answer one question — which subject formats does the
// parser not understand yet — and its population is unbounded, one row per
// distinct normalised stem. The watch's first day produced 2,260 of them
// against 26 actual rules, which is how they came to be mistaken for rules.
//
// So the card pages, and every number it shows is stated against a
// denominator. A list that truncates silently reads as the whole set.

// diagChipVM is one instrument's filter chip.
type diagChipVM struct {
	Kind   string
	Rows   int
	URL    string
	Active bool
}

// diagVM is the paged diagnostics card.
type diagVM struct {
	Rows  []filterHitVM
	Kinds []diagChipVM
	Kind  string // selected instrument, "" = all

	Page, Pages int
	First, Last int // 1-based row range shown, for "26-50 of 2260"
	TotalRows   int
	TotalHits   string
	AllURL      string // the "all" chip
	PrevURL     string
	NextURL     string
}

// diagLink builds a link back into this tab. The page is one render with the
// tab selected client-side, so the fragment anchor is what returns the
// operator to the card they clicked in.
func diagLink(kind string, page int) string {
	q := url.Values{}
	if kind != "" {
		q.Set("dkind", kind)
	}
	if page > 1 {
		q.Set("dpage", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "?#filters"
	}
	return "?" + q.Encode() + "#filters"
}

// buildDiagVM turns a store page into the card. Split from the store read so
// the paging arithmetic — the part with the off-by-ones — is testable without
// a database.
//
// page is 1-based and already clamped by the caller; kind is trusted to be one
// the store returned, since an unknown kind simply yields no rows.
func buildDiagVM(dp diagPage, kind string, page int) diagVM {
	vm := diagVM{
		Kind: kind, Page: page, TotalRows: dp.TotalRows,
		TotalHits: fmtComma(dp.TotalHits),
		AllURL:    diagLink("", 1),
	}
	for _, r := range dp.Rows {
		vm.Rows = append(vm.Rows, filterHitVM{
			Kind: r.Kind, Rule: r.Rule, Sample: r.LastSample,
			Count: r.TotalCount, LastSeen: fmtTime(r.LastSeen),
		})
	}
	for _, k := range dp.Kinds {
		vm.Kinds = append(vm.Kinds, diagChipVM{
			Kind: k.Kind, Rows: k.Rows,
			URL: diagLink(k.Kind, 1), Active: k.Kind == kind,
		})
	}

	vm.Pages = (dp.TotalRows + diagPageSize - 1) / diagPageSize
	if len(vm.Rows) > 0 {
		vm.First = (page-1)*diagPageSize + 1
		vm.Last = vm.First + len(vm.Rows) - 1
	}
	if page > 1 {
		vm.PrevURL = diagLink(kind, page-1)
	}
	if page < vm.Pages {
		vm.NextURL = diagLink(kind, page+1)
	}
	return vm
}

// Range is the "showing X-Y of Z" line. Formatted in Go because these admin
// fragments are parsed without a FuncMap — an arithmetic helper in the
// template would fail the WHOLE render at runtime with nothing failing at
// compile time (which is exactly how this card shipped invisible once).
func (v diagVM) Range() string {
	if v.TotalRows == 0 {
		return "none recorded"
	}
	if v.TotalRows <= len(v.Rows) {
		return fmt.Sprintf("%d distinct stem(s)", v.TotalRows)
	}
	return fmt.Sprintf("%d-%d of %s distinct stem(s)", v.First, v.Last, fmtComma(int64(v.TotalRows)))
}

// parseDiagPage reads ?dpage=. Anything unparseable or below 1 is page 1 —
// the parameter is operator-supplied, so it is floored at the boundary rather
// than trusted into an offset.
func parseDiagPage(raw string) int {
	page, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || page < 1 {
		return 1
	}
	return page
}

// clampDiagPage pulls a page past the end back to the last one, so a stale
// bookmark shows the tail rather than an empty card.
func clampDiagPage(page, totalRows int) int {
	pages := (totalRows + diagPageSize - 1) / diagPageSize
	if page < 1 || pages == 0 {
		// Nothing recorded: there is no last page to fall back to, so page 1
		// stands and the card renders its empty state.
		return 1
	}
	if page > pages {
		return pages
	}
	return page
}

// diagnosticsVM reads one page of instrument counters.
//
// It reads twice when the requested page is past the end: the first read
// establishes how many rows the selected kind actually has, and only then can
// the page be clamped. That costs a second indexed query on the rare
// out-of-range request rather than showing an empty card on every stale
// bookmark.
func (p *Plugin) diagnosticsVM(ctx context.Context, kind string, page int) (diagVM, error) {
	if page < 1 {
		page = 1
	}
	dp, err := p.st.diagnosticHits(ctx, kind, diagPageSize, (page-1)*diagPageSize)
	if err != nil {
		return diagVM{}, err
	}
	if want := clampDiagPage(page, dp.TotalRows); want != page {
		page = want
		if dp, err = p.st.diagnosticHits(ctx, kind, diagPageSize, (page-1)*diagPageSize); err != nil {
			return diagVM{}, err
		}
	}
	return buildDiagVM(dp, kind, page), nil
}
