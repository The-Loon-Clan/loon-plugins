package usenet

import (
	"strconv"
	"testing"
)

// walkPastDead is the whole safety argument of the sweep: everything it
// spares, it spares for a reason, and each reason gets a row here. The base
// case is a single-file set claiming 5 parts, holding 2, whose span [100,200]
// the walk has fully fetched — dead by definition, because the missing three
// parts were in the fetched range and did not appear.
func TestWalkPastDead(t *testing.T) {
	const now, grace = int64(10_000), int64(900)
	meta := func(touched int64, lo, hi string) map[string]string {
		m := map[string]string{"total_parts": "5"}
		if touched > 0 {
			m["touched_at"] = strconv.FormatInt(touched, 10)
		}
		if lo != "" {
			m["art_lo"], m["art_hi"] = lo, hi
		}
		return m
	}
	covered := []articleRange{{Start: 50, End: 300}}

	for _, tc := range []struct {
		name string
		meta map[string]string
		held int
		cov  []articleRange
		dead bool
	}{
		{"stale, short, span fully fetched: dead", meta(now-1000, "100", "200"), 2, covered, true},
		{"inside grace: a set the crawl may still be filling", meta(now-100, "100", "200"), 2, covered, false},
		{"no staleness information at all", meta(0, "100", "200"), 2, covered, false},
		{"span unknown (pre-span set): not judgeable", meta(now-1000, "", ""), 2, covered, false},
		{"gap in coverage: the missing parts may still arrive", meta(now-1000, "100", "200"), 2,
			[]articleRange{{Start: 150, End: 300}}, false},
		{"no coverage recorded", meta(now-1000, "100", "200"), 2, nil, false},
		{"held meets the bound: completeness is the builder's call", meta(now-1000, "100", "200"), 5, covered, false},
		{"held exceeds the bound", meta(now-1000, "100", "200"), 7, covered, false},
		{"fragmented coverage that still covers the span", meta(now-1000, "100", "200"), 2,
			[]articleRange{{Start: 90, End: 150}, {Start: 151, End: 250}}, true},
	} {
		if got := walkPastDead(tc.meta, tc.held, tc.cov, now, grace); got != tc.dead {
			t.Errorf("%s: dead=%v, want %v", tc.name, got, tc.dead)
		}
	}
}

// Multi-backbone groups must drop out of the judgeable map entirely: their
// sets' spans mix two article-number spaces, and judging against either one
// invents gaps or coverage that does not exist.
func TestJudgeableCoverageSkipsMultiBackboneGroups(t *testing.T) {
	got := judgeableCoverage(map[coverKey][]articleRange{
		{backbone: "b1", group: "a.b.single"}: {{Start: 1, End: 100}},
		{backbone: "b1", group: "a.b.dual"}:   {{Start: 1, End: 100}},
		{backbone: "b2", group: "a.b.dual"}:   {{Start: 500, End: 600}},
	})
	if _, ok := got["a.b.single"]; !ok {
		t.Error("single-backbone group missing from the judgeable map")
	}
	if _, ok := got["a.b.dual"]; ok {
		t.Error("group covered on two backbones survived — its spans mix number spaces")
	}
}

// salvageTally must split a dead set's gaps the way the health job splits a
// stored NZB (data vs par2 by subject), because healthVerdict scores both by
// the same rule — a divergence here would salvage what health then condemns.
func TestSalvageTally(t *testing.T) {
	art := func(fn, part, segTotal int, subject string) stagedArticle {
		return stagedArticle{
			FileNum: fn, PartNum: part, SegTotal: segTotal, Subject: subject,
			FileParts: true, TotalFiles: 2,
		}
	}
	for _, tc := range []struct {
		name                                      string
		arts                                      []stagedArticle
		total, missData, par2Claimed, par2Missing int
		verdict                                   string
	}{
		{"data gap covered by surviving par2: broken (salvageable)",
			[]stagedArticle{
				art(1, 1, 4, "Show [01/02]"), art(1, 2, 4, "Show [01/02]"), art(1, 3, 4, "Show [01/02]"),
				art(2, 1, 2, "Show.vol0+1.par2 [02/02]"), art(2, 2, 2, "Show.vol0+1.par2 [02/02]"),
			}, 6, 1, 2, 0, healthBroken},
		{"data gaps with no par2 at all: dead",
			[]stagedArticle{
				art(1, 1, 5, "Show [01/02]"), art(1, 2, 5, "Show [01/02]"), art(1, 3, 5, "Show [01/02]"),
			}, 5, 2, 0, 0, healthDead},
		{"par2-only gaps: healthy — every data segment is present",
			[]stagedArticle{
				art(1, 1, 2, "Show [01/02]"), art(1, 2, 2, "Show [01/02]"),
				art(2, 1, 3, "Show.vol0+1.par2 [02/02]"),
			}, 5, 0, 3, 2, healthHealthy},
		{"claim below what is held: trust the articles, no phantom gap",
			[]stagedArticle{
				art(1, 1, 0, "Show [01/02]"), art(1, 2, 0, "Show [01/02]"),
			}, 2, 0, 0, 0, healthHealthy},
		{"gaps in data AND par2: judged against SURVIVING par2 only",
			[]stagedArticle{
				art(1, 1, 4, "Show [01/02]"), art(1, 2, 4, "Show [01/02]"),
				art(2, 1, 4, "Show.vol0+1.par2 [02/02]"),
			}, 8, 2, 4, 3, healthDead},
	} {
		total, missData, p2c, p2m := salvageTally(tc.arts)
		if total != tc.total || missData != tc.missData || p2c != tc.par2Claimed || p2m != tc.par2Missing {
			t.Errorf("%s: tally = (%d,%d,%d,%d), want (%d,%d,%d,%d)", tc.name,
				total, missData, p2c, p2m, tc.total, tc.missData, tc.par2Claimed, tc.par2Missing)
			continue
		}
		if v := healthVerdict(missData, p2c, p2m); v != tc.verdict {
			t.Errorf("%s: verdict = %q, want %q", tc.name, v, tc.verdict)
		}
	}
}
