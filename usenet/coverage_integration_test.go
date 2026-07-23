//go:build integration

package usenet

import (
	"context"
	"testing"
	"time"
)

// Integration tests for the coverage SQL. These exist because of a real bug they
// would have caught: stats() kept reading the legacy per-GROUP watermark columns
// after the crawl moved to per-BACKBONE state, so the crawlers page showed
// backfill frozen at zero forever while the crawl was in fact making progress.
// Nothing in the unit tests could see that — the query compiled, returned rows,
// and every number in them was stale.
//
//	go test -tags=integration -count=1 -run Coverage ./usenet/
//
// with USENET_TEST_DSN pointing at a throwaway Postgres.

// mustGroups adds the named groups and activates them: stats() only reports
// active groups, so an inactive one would make every assertion below vacuous.
func mustGroups(t *testing.T, s *PGStore, names ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.upsertGroups(ctx, names); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := s.setGroupActive(ctx, n, true); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStatsReadsPerBackboneState pins the fix: what the page shows must be what
// the crawl actually wrote.
func TestStatsReadsPerBackboneState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustGroups(t, s, "alt.binaries.anime")

	// Exactly what a crawl pass writes: server bounds plus a forward watermark.
	if err := s.updateGroupStateForBackbone(ctx, "omicron", "alt.binaries.anime",
		1000, 9000, 5000, 4999, time.Now()); err != nil {
		t.Fatal(err)
	}
	// …then a backfill pass lowering the back watermark.
	if err := s.updateBackWatermarkForBackbone(ctx, "omicron", "alt.binaries.anime",
		3000, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}

	st, err := s.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 1 {
		t.Fatalf("got %d group rows, want 1", len(st.Groups))
	}
	g := st.Groups[0]
	if g.Backbone != "omicron" {
		t.Errorf("backbone = %q, want omicron", g.Backbone)
	}
	if g.HighWatermark != 5000 || g.BackWatermark != 3000 {
		t.Errorf("watermarks = fwd %d / back %d, want 5000 / 3000 — stats is reading stale columns",
			g.HighWatermark, g.BackWatermark)
	}
	if g.ServerLow != 1000 || g.ServerHigh != 9000 {
		t.Errorf("server bounds = %d..%d, want 1000..9000", g.ServerLow, g.ServerHigh)
	}
	// Remaining backfill drives the ETA, so it has to be real.
	if want := int64(3000 - 1000); st.TotalBackfillRemaining != want {
		t.Errorf("backfill remaining = %d, want %d", st.TotalBackfillRemaining, want)
	}
	if cov := g.Coverage(); !cov.Known || cov.HavePct <= 0 {
		t.Errorf("coverage bar not derived: %+v", cov)
	}
}

// TestStatsSeparatesBackbones: two backbones must never share a row. Article
// numbers from one describe different articles on the other, so a merged bar
// would be a number with no meaning.
func TestStatsSeparatesBackbones(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustGroups(t, s, "alt.binaries.anime")
	for _, bb := range []struct {
		name             string
		low, high, water int64
	}{
		{"omicron", 1000, 9000, 5000},
		{"usenetfarm", 1, 400, 300},
	} {
		if err := s.updateGroupStateForBackbone(ctx, bb.name, "alt.binaries.anime",
			bb.low, bb.high, bb.water, bb.water-1, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 2 {
		t.Fatalf("got %d rows for one group on two backbones, want 2", len(st.Groups))
	}
	seen := map[string]int64{}
	for _, g := range st.Groups {
		seen[g.Backbone] = g.HighWatermark
	}
	if seen["omicron"] != 5000 || seen["usenetfarm"] != 300 {
		t.Errorf("watermarks bled between backbones: %v", seen)
	}
}

// TestAllCoveredRangesKeying: coverage cells are drawn from this map, so a
// mis-keyed row would paint one backbone's progress onto another's bar.
func TestAllCoveredRangesKeying(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustGroups(t, s, "alt.binaries.anime", "alt.binaries.tv")
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.anime", 100, 199); err != nil {
		t.Fatal(err)
	}
	// Adjacent run: must merge with the one above rather than becoming a second row.
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.anime", 200, 299); err != nil {
		t.Fatal(err)
	}
	// Same numbers, different backbone — must stay a separate entry.
	if err := s.recordFetchedRangeFor(ctx, "usenetfarm", "alt.binaries.anime", 100, 199); err != nil {
		t.Fatal(err)
	}
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.tv", 500, 599); err != nil {
		t.Fatal(err)
	}

	all, err := s.allCoveredRanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	anime := all[coverKey{"omicron", "alt.binaries.anime"}]
	if len(anime) != 1 {
		t.Fatalf("adjacent runs did not merge: %+v", anime)
	}
	if anime[0].Start != 100 || anime[0].End != 299 {
		t.Errorf("merged run = %d..%d, want 100..299", anime[0].Start, anime[0].End)
	}
	if other := all[coverKey{"usenetfarm", "alt.binaries.anime"}]; len(other) != 1 {
		t.Errorf("the other backbone's identical range was absorbed: %+v", other)
	}
	if tv := all[coverKey{"omicron", "alt.binaries.tv"}]; len(tv) != 1 {
		t.Errorf("second group missing from the map: %+v", tv)
	}

	// The page reads this map straight into the sparkline.
	cells := cellLevels(coverageCells(anime, 0, 999, 10))
	if cells[0] != 0 || cells[1] == 0 || cells[2] == 0 || cells[3] != 0 {
		t.Errorf("cells %v do not locate the 100..299 run", cells)
	}
}

// TestResetBackfillClearsRanges: without dropping the ranges, "reset backfill"
// re-arms the watermark and then the very next pass computes an empty gap list
// (everything is still marked covered) and immediately marks the group done —
// a button that looks like it worked and does nothing.
func TestResetBackfillClearsRanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustGroups(t, s, "alt.binaries.anime")
	if err := s.updateGroupStateForBackbone(ctx, "omicron", "alt.binaries.anime",
		0, 1000, 900, 899, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.anime", 0, 899); err != nil {
		t.Fatal(err)
	}
	if err := s.markBackfillDoneForBackbone(ctx, "omicron", "alt.binaries.anime"); err != nil {
		t.Fatal(err)
	}

	if err := s.resetBackfillForGroup(ctx, "alt.binaries.anime"); err != nil {
		t.Fatal(err)
	}

	all, err := s.allCoveredRanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if runs := all[coverKey{"omicron", "alt.binaries.anime"}]; len(runs) != 0 {
		t.Errorf("ranges survived the reset (%+v) — backfill would find no gaps and stop again", runs)
	}
	gaps, err := s.backfillGapsFor(ctx, "omicron", "alt.binaries.anime", 0, 899)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Start != 0 || gaps[0].End != 899 {
		t.Errorf("gaps after reset = %+v, want the whole span back", gaps)
	}

	st, err := s.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 1 || st.Groups[0].BackfillDone {
		t.Error("group still reads as backfill-complete after a reset")
	}
}
