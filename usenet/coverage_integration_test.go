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

// TestLowPriorityTierRule pins the operator's scheduling rule THROUGH THE
// DATABASE: normal groups always take the pass before low-priority ones, and
// low only ever gets whatever capacity is left over.
//
// The ordering itself is exhaustively unit-tested in TestOrderCrawlGroups
// (pure, no DB). What only a real Postgres shows is the plumbing around it:
// that setGroupTuning actually persists the tier, that the LEFT JOIN to
// newsgroup_state surfaces it per backbone, and that the flag survives the
// round trip. A fake would have to assume all three.
//
// HISTORY, because this test asserted the OPPOSITE for a while and someone
// will be tempted to put it back: 8b58253 shipped "low-priority groups get the
// pass once the normal tier is caught up", with a test for it. 046e33c then
// deliberately reversed the rule -- "low never preempts normal" -- because the
// caught-up heuristic let low-pri jump the queue whenever the normal tier
// momentarily had nothing new, and rewrote selection as orderCrawlGroups. It
// did not update this test, which then sat red on main asserting the removed
// behaviour. If low-pri should ever preempt again, that is a product decision:
// change orderCrawlGroups and this test together, and say so in the commit.
func TestLowPriorityTierRule(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const bb = "omicron"

	mustGroups(t, s, "n.one", "n.two", "l.big")
	if err := s.setGroupTuning(ctx, "l.big", 0, 0, TierLow); err != nil {
		t.Fatal(err)
	}

	// Normal tier behind: n.one has new articles waiting on the server.
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
		t.Fatalf("normal tier behind: want the 2 normal groups, got %+v", got)
	}

	// The case that used to expect the opposite. Even with the normal tier
	// fully caught up -- nothing new to fetch -- low-pri must NOT take the
	// slots ahead of them: normal groups still poll, low rides in leftovers.
	if err := s.updateGroupStateForBackbone(ctx, bb, "n.one", 1, 1100, 1100, 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = s.activeGroupsForBackbone(ctx, bb, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 groups under the cap, got %+v", got)
	}
	if got[0].LowPriority || got[1].LowPriority {
		t.Fatalf("a caught-up normal tier must still outrank low-priority, got %+v", got)
	}

	// Uncapped, the low-pri group appears -- LAST. This is what proves the
	// flag survived the round trip rather than the tier being filtered out.
	got, err = s.activeGroupsForBackbone(ctx, bb, 0) // 0 = no cap
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("uncapped: want all 3 groups, got %+v", got)
	}
	if got[0].LowPriority || got[1].LowPriority {
		t.Fatalf("uncapped: normal groups must lead, got %+v", got)
	}
	if !got[2].LowPriority || got[2].Name != "l.big" {
		t.Fatalf("uncapped: want l.big last and flagged low-pri, got %+v", got[2])
	}
}
