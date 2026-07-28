//go:build integration

package usenet

import (
	"context"
	"testing"
)

// Attribution now flushes per GROUP, so one pass issues many small writes for
// the same (poster, stage, reason). The upsert therefore has to ADD; a
// replacing upsert would leave only the last group's tally and make a busy
// watched poster look quiet — the exact misreading the watch exists to prevent.
//
// Real database, because the accumulation lives entirely in the ON CONFLICT
// clause: the Go side just hands over a map.
func TestPosterHitsAccumulateAcrossFlushes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	k := posterHitKey{"tsukihime", "ingest", "staged"}
	for i := 0; i < 4; i++ {
		if err := s.recordPosterHits(ctx, map[posterHitKey]*posterHitVal{
			k: {count: 250, sample: "first subject"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.posterHitRows(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.Poster != "tsukihime" {
			continue
		}
		found = true
		if r.Count != 1000 {
			t.Errorf("count = %d after 4 flushes of 250, want 1000 — "+
				"the upsert is replacing instead of accumulating", r.Count)
		}
	}
	if !found {
		t.Fatal("no row for the watched poster")
	}

	// A later flush carrying an empty sample must not blank the one on record:
	// the operator reads that sample to recognise the release.
	if err := s.recordPosterHits(ctx, map[posterHitKey]*posterHitVal{
		k: {count: 1, sample: ""},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.posterHitRows(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Poster == "tsukihime" && r.Sample == "" {
			t.Error("an empty sample overwrote the recorded one")
		}
	}
}
