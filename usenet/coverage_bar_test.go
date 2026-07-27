package usenet

import (
	"html/template"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func TestFmtPct(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0%"},
		{-1, "0%"},
		// The case the strip exists for. Prod showed 1.05% coverage on a group
		// the watermarks called complete; rounding a real sliver down to "0%"
		// would erase the only signal that the two disagree.
		{0.4, "<1%"},
		{0.99, "<1%"},
		{1, "1%"},
		{1.05, "1%"},
		{49.4, "49%"},
		{49.6, "50%"},
		// Never claim 100% until it really is complete-ish: an operator reading
		// "100%" stops looking, so the threshold is deliberately tight.
		{99.4, "99%"},
		{99.5, "100%"},
		{100, "100%"},
	}
	for _, c := range cases {
		if got := fmtPct(c.in); got != c.want {
			t.Errorf("fmtPct(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCoveredFraction(t *testing.T) {
	if got := coveredFraction(nil); got != 0 {
		t.Errorf("nil cells: got %v, want 0", got)
	}
	if got := coveredFraction([]float64{1, 1, 1, 1}); got != 1 {
		t.Errorf("full: got %v, want 1", got)
	}
	if got := coveredFraction([]float64{1, 0, 1, 0}); got != 0.5 {
		t.Errorf("half: got %v, want 0.5", got)
	}
	// Fractional fills must carry their real weight, not round per-cell —
	// otherwise a group covered by many narrow runs reports as empty.
	if got := coveredFraction([]float64{0.25, 0.25, 0, 0}); got != 0.125 {
		t.Errorf("fractional: got %v, want 0.125", got)
	}
}

// The watermark bar used to sit above the fetched-ranges bar and read solid
// green whenever the bookmarks met. It is gone; this pins that, because
// re-adding it is exactly the regression that made a dead group look healthy.
func TestCrawlersCoverageStripIsRangeBased(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	render := func(g crawlerGroupVM) string {
		var sb strings.Builder
		err := tmpl.ExecuteTemplate(&sb, "crawlers.html", map[string]any{
			"Stats": pluginapi.IndexStats{}, "Jobs": nil, "Builder": BuilderInfo{},
			"Fleet": nil, "Workers": nil, "Pass": passVM{}, "Backfill": passVM{},
			"Pending": nil, "Errors": nil, "Health": healthVM{},
			"IndexStats": indexStatsVM{}, "HostSink": true, "AutoRefresh": false,
			"Msg": "", "Err": "",
			"Backbones": []backboneVM{{Name: "netnews", Groups: []crawlerGroupVM{g}}},
			"Groups":    []crawlerGroupVM{g},
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}

	// A sparsely-covered group must show its percentage, not a green block.
	out := render(crawlerGroupVM{
		Name: "alt.binaries.anime", Cells: []int{0, 0, 0, 3}, Fragments: 1,
		CoveredFmt: "<1%", BackfillDone: true, FwdAt: "2026-07-27 07:12",
	})
	for _, want := range []string{"cov-cells", "cov-pct", "&lt;1% crawled", "BY THIS CRAWLER"} {
		if !strings.Contains(out, want) {
			t.Errorf("coverage strip missing %q", want)
		}
	}
	// The watermark-derived segments must not come back.
	for _, gone := range []string{"cov-have", "cov-back", "cov-skip", "cov-new"} {
		if strings.Contains(out, gone) {
			t.Errorf("watermark bar segment %q is back — it reads solid green on a dead group", gone)
		}
	}

	// A group with no recorded ranges says so in words. An empty track is
	// indistinguishable from a fully-unfetched one at 9px tall.
	out = render(crawlerGroupVM{Name: "alt.binaries.new", NoCoverage: true})
	if !strings.Contains(out, "no coverage recorded") {
		t.Error("a group with no ranges must say so, not render an empty track")
	}
}
