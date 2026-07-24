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

// TestRangesSurviveRealArticleNumbers is the regression test for a bug every
// other range test missed by using cosy small numbers: article numbers on major
// backbones are in the BILLIONS, and the merge query's "$4 + 1" arithmetic made
// Postgres infer the params as int4 — so recording coverage failed with a 22003
// range error on every batch of every large group, and backfill refetched the
// same spans forever. Reproduced against PG 13 before fixing; pinned here with
// numbers a real binaries group actually has.
func TestRangesSurviveRealArticleNumbers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustGroups(t, s, "alt.binaries.anime")

	const base = int64(41_000_000_000) // > 2^31 by a wide margin
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.anime", base, base+2999); err != nil {
		t.Fatalf("recordFetchedRangeFor with billion-range article numbers: %v", err)
	}
	// Adjacent batch must merge, exercising the +1/-1 arithmetic at scale.
	if err := s.recordFetchedRangeFor(ctx, "omicron", "alt.binaries.anime", base+3000, base+5999); err != nil {
		t.Fatalf("adjacent record: %v", err)
	}

	all, err := s.allCoveredRanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runs := all[coverKey{"omicron", "alt.binaries.anime"}]
	if len(runs) != 1 || runs[0].Start != base || runs[0].End != base+5999 {
		t.Fatalf("merged run = %+v, want [%d..%d] as one row", runs, base, base+5999)
	}

	gaps, err := s.backfillGapsFor(ctx, "omicron", "alt.binaries.anime", base-10_000, base+5999)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].End != base-1 {
		t.Fatalf("gap complement at scale = %+v, want one gap ending at %d", gaps, base-1)
	}

	// Watermarks at the same scale, exercising updateGroupStateForBackbone.
	if err := s.updateGroupStateForBackbone(ctx, "omicron", "alt.binaries.anime",
		base-10_000, base+5999, base+5999, base-1, time.Now()); err != nil {
		t.Fatalf("watermarks at billion scale: %v", err)
	}
}

// TestAnyBackfillPending pins what "idle" means for the health job: pending
// while any backbone of any ACTIVE group has history left, idle once every
// backbone is done. The query this replaced read the dead legacy columns on
// newsgroups, which made a fresh install look permanently caught-up (health ran
// during backfill) and an upgraded one permanently busy (idle health never ran).
func TestAnyBackfillPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustGroups(t, s, "alt.binaries.anime")

	// No state rows at all: nothing pending.
	if p, err := s.anyBackfillPending(ctx); err != nil || p {
		t.Fatalf("fresh install: pending=%v err=%v, want false/nil", p, err)
	}

	// A crawled group with history left below its back watermark: pending.
	if err := s.updateGroupStateForBackbone(ctx, "omicron", "alt.binaries.anime",
		1000, 9000, 5000, 4999, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.anyBackfillPending(ctx); !p {
		t.Fatal("history remains but pending=false — health would steal connections from backfill")
	}

	// Done on the only backbone: idle again.
	if err := s.markBackfillDoneForBackbone(ctx, "omicron", "alt.binaries.anime"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.anyBackfillPending(ctx); p {
		t.Fatal("backfill complete but pending=true — idle health would never run")
	}

	// An INACTIVE group's leftover state must not count.
	if err := s.setGroupActive(ctx, "alt.binaries.anime", false); err != nil {
		t.Fatal(err)
	}
	if err := s.updateGroupStateForBackbone(ctx, "usenetfarm", "alt.binaries.anime",
		1, 400, 300, 299, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.anyBackfillPending(ctx); p {
		t.Fatal("inactive group's state counted as pending work")
	}
}

// TestLowPriorityTierRule pins the operator's scheduling rule: normal groups
// with known new articles own the pass; once the normal tier is caught up,
// the low-priority backlog takes the slots (and normal groups ride along as
// polls, so their next arrivals are noticed). The original "ORDER BY
// low_priority LIMIT n" starved low-pri groups FOREVER once n normal groups
// existed — observed on prod 2026-07-24.
func TestLowPriorityTierRule(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const bb = "omicron"

	mustGroups(t, s, "n.one", "n.two", "l.big")
	if err := s.setGroupTuning(ctx, "l.big", 0, 0, true); err != nil {
		t.Fatal(err)
	}

	// Normal tier behind: n.one has 100 new articles on the server.
	if err := s.updateGroupStateForBackbone(ctx, bb, "n.one", 1, 1100, 1000, 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.updateGroupStateForBackbone(ctx, bb, "n.two", 1, 500, 500, 500, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.updateGroupStateForBackbone(ctx, bb, "l.big", 1, 9000, 10, 10, time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.activeGroupsForBackbone(ctx, bb, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].LowPriority || got[1].LowPriority {
		t.Fatalf("normal tier behind: want the 2 normal groups first, got %+v", got)
	}

	// Normal tier catches up: the low-pri backlog must now own the pass, with
	// a normal group riding along as a poll.
	if err := s.updateGroupStateForBackbone(ctx, bb, "n.one", 1, 1100, 1100, 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = s.activeGroupsForBackbone(ctx, bb, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].LowPriority {
		t.Fatalf("normal tier caught up: want the low-pri group first, got %+v", got)
	}
	if got[1].LowPriority {
		t.Fatalf("leftover slot should poll a normal group, got %+v", got[1])
	}
}
