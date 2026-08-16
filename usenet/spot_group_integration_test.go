//go:build integration

package usenet

import (
	"context"
	"testing"
)

// A spot group inserted the way the admin button (and the ops change file)
// inserts it: name, kind, active — and nothing else.
//
// back_watermark is the only nullable column on newsgroups and has no default,
// so such a row arrives with NULL. Scanning it into an int64 fails the ENTIRE
// query, and the failure is total rather than partial: the pass reports an
// error and reads no spots at all, forever. That is exactly how the first live
// run ended, and a mock store could never have caught it.
func TestSpotGroupsToleratesANullBackWatermark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	db := s.db.DB()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroups (name, kind, active) VALUES ('free.pt', 'spots', TRUE)
		 ON CONFLICT (name) DO UPDATE SET kind = 'spots', active = TRUE, back_watermark = NULL`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { db.ExecContext(ctx, `DELETE FROM newsgroups WHERE name = 'free.pt'`) })

	// Prove the precondition: the column really is NULL, so this test cannot
	// quietly stop covering the case if a default is added later.
	var isNull bool
	if err := db.QueryRowContext(ctx,
		`SELECT back_watermark IS NULL FROM newsgroups WHERE name = 'free.pt'`).Scan(&isNull); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !isNull {
		t.Fatal("back_watermark is no longer nullable — this test no longer covers what it was written for")
	}

	groups, err := s.spotGroups(ctx)
	if err != nil {
		t.Fatalf("spotGroups: %v", err)
	}
	var found bool
	for _, g := range groups {
		if g.Name != "free.pt" {
			continue
		}
		found = true
		// 0 is the unseeded sentinel indexSpotGroup seeds from server_high.
		if g.BackWatermark != 0 {
			t.Errorf("BackWatermark = %d, want 0 (the unseeded sentinel)", g.BackWatermark)
		}
	}
	if !found {
		t.Error("the spot group was not returned")
	}
}

// The crawler must never see a spot group: its assembler would stage millions
// of one-article subjects that can never complete a set.
func TestSpotGroupsAreHiddenFromTheCrawler(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	db := s.db.DB()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO newsgroups (name, kind, active) VALUES ('free.pt', 'spots', TRUE)
		 ON CONFLICT (name) DO UPDATE SET kind = 'spots', active = TRUE`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { db.ExecContext(ctx, `DELETE FROM newsgroups WHERE name = 'free.pt'`) })

	groups, err := s.activeGroupsForBackbone(ctx, "bb", 100, false)
	if err != nil {
		t.Fatalf("activeGroupsForBackbone: %v", err)
	}
	for _, g := range groups {
		if g.Name == "free.pt" {
			t.Fatal("the crawler picked up a spot group — its assembler would stage millions of dead subjects")
		}
	}
}
