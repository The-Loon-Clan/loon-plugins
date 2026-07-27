//go:build integration

package usenet

import (
	"context"
	"strings"
	"testing"
)

// resetWatermark rewinds the FORWARD mark to the start of what this crawler
// actually fetched. The guards matter more than the happy path: the wrong
// target re-reads hundreds of millions of articles against a metered provider.
func TestResetWatermarkIntegration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	db := s.db.DB()

	seed := func(group string, low, high, mark int64, ranges [][2]int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO newsgroup_state (backbone, group_name, high_watermark, server_low, server_high, backfill_done)
			 VALUES ($1,$2,$3,$4,$5,TRUE)
			 ON CONFLICT (backbone, group_name) DO UPDATE SET high_watermark = EXCLUDED.high_watermark`,
			"bb", group, mark, low, high); err != nil {
			t.Fatal(err)
		}
		for _, r := range ranges {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end) VALUES ($1,$2,$3,$4)`,
				"bb", group, r[0], r[1]); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The real shape: an INHERITED range seeded at adoption (migration 020/021)
	// plus the span this crawler fetched. The target must be the crawler's own
	// start, not the inherited one — on prod the inherited span is 793M
	// articles the OLD crawler indexed and this one never touched.
	seed("adopted.group", 1000, 2000000, 1_900_000, [][2]int64{
		{1000, 1_499_999},      // inherited
		{1_500_000, 1_900_000}, // this crawler
	})
	got, err := s.resetWatermark(ctx, "bb", "adopted.group", resetForward)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got.NewMark != 1_500_000 {
		t.Errorf("target = %d, want 1500000 (the crawler's own start, not the inherited 1000)", got.NewMark)
	}
	if got.Articles != 400_000 {
		t.Errorf("articles = %d, want 400000", got.Articles)
	}
	var mark int64
	if err := db.QueryRowContext(ctx,
		`SELECT high_watermark FROM newsgroup_state WHERE backbone='bb' AND group_name='adopted.group'`).
		Scan(&mark); err != nil {
		t.Fatal(err)
	}
	if mark != 1_500_000 {
		t.Errorf("persisted mark = %d, want 1500000", mark)
	}

	// backfill_done must be untouched, or the backfill job wakes up and starts
	// walking backwards through years of history.
	var done bool
	if err := db.QueryRowContext(ctx,
		`SELECT backfill_done FROM newsgroup_state WHERE backbone='bb' AND group_name='adopted.group'`).
		Scan(&done); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("backfill_done was cleared — that restarts a multi-year backfill")
	}

	// A heavily-fragmented group (mid-backfill) can have its highest
	// range_start ABOVE the forward mark. Rewinding there would move the
	// watermark FORWARD and silently skip everything between. Must refuse.
	seed("fragmented.group", 1000, 9_000_000, 2_000_000, [][2]int64{
		{1000, 5000}, {8_000_000, 8_500_000},
	})
	if _, err := s.resetWatermark(ctx, "bb", "fragmented.group", resetForward); err == nil {
		t.Error("fragmented group: want a refusal, got success")
	} else if !strings.Contains(err.Error(), "not behind the current watermark") {
		t.Errorf("fragmented group: unhelpful error %q", err)
	}

	// No coverage recorded: nothing to rewind to.
	seed("bare.group", 1000, 9000, 5000, nil)
	if _, err := s.resetWatermark(ctx, "bb", "bare.group", resetForward); err == nil {
		t.Error("group with no ranges: want a refusal, got success")
	} else if !strings.Contains(err.Error(), "no recorded coverage") {
		t.Errorf("bare group: unhelpful error %q", err)
	}

	// Unknown group must not silently succeed.
	if _, err := s.resetWatermark(ctx, "bb", "does.not.exist", resetForward); err == nil {
		t.Error("unknown group: want a refusal, got success")
	}
}

// resetHistory reopens the backfill over the gaps below the forward mark. It is
// the repair for a blind spot inherited from a PREVIOUS crawler, which is a
// different failure from the plugin-era parser bug resetForward addresses — so
// the two are independent, and a group can need one without the other.
func TestResetWatermarkHistoryScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	db := s.db.DB()

	// Only the top slice is recorded as fetched; everything below it is gap.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroup_state (backbone, group_name, high_watermark, back_watermark,
		                              server_low, server_high, backfill_done)
		 VALUES ('bb','hist.group', 2000000, 500, 1000, 2000000, TRUE)
		 ON CONFLICT (backbone, group_name) DO UPDATE SET backfill_done = TRUE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
		 VALUES ('bb','hist.group', 1900000, 2000000)`); err != nil {
		t.Fatal(err)
	}

	got, err := s.resetWatermark(ctx, "bb", "hist.group", resetHistory)
	if err != nil {
		t.Fatalf("history reset: %v", err)
	}
	// window 1000..2000000 = 1,999,000; covered 1,900,000..2,000,000 = 100,001
	if want := int64(1999000 - 100001); got.Articles != want {
		t.Errorf("gap = %d, want %d — it must exclude what is already recorded as fetched", got.Articles, want)
	}

	var back int64
	var done bool
	if err := db.QueryRowContext(ctx,
		`SELECT back_watermark, backfill_done FROM newsgroup_state
		  WHERE backbone='bb' AND group_name='hist.group'`).Scan(&back, &done); err != nil {
		t.Fatal(err)
	}
	if done {
		t.Error("backfill_done still TRUE — the backfill will never run")
	}
	if back != 2000000 {
		t.Errorf("back_watermark = %d, want the forward mark 2000000", back)
	}

	// The forward mark must NOT move: these are independent repairs, and
	// silently doing both would charge for a re-read nobody asked for.
	var fwd int64
	if err := db.QueryRowContext(ctx,
		`SELECT high_watermark FROM newsgroup_state WHERE backbone='bb' AND group_name='hist.group'`).
		Scan(&fwd); err != nil {
		t.Fatal(err)
	}
	if fwd != 2000000 {
		t.Errorf("forward mark moved to %d during a history reset", fwd)
	}

	// A fully-covered group must refuse: reopening would find no gaps and
	// immediately re-mark itself done, which looks like success and is not.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroup_state (backbone, group_name, high_watermark, back_watermark,
		                              server_low, server_high, backfill_done)
		 VALUES ('bb','covered.group', 5000, 100, 1000, 5000, TRUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
		 VALUES ('bb','covered.group', 1000, 5000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resetWatermark(ctx, "bb", "covered.group", resetHistory); err == nil {
		t.Error("fully-covered group: want a refusal, got success")
	} else if !strings.Contains(err.Error(), "already recorded as fetched") {
		t.Errorf("unhelpful error: %v", err)
	}
}
