//go:build integration

package usenet

import (
	"context"
	"testing"
)

// The deltas are computed by a window function in SQL, so they only exist
// against a real database — and they are the entire point of the table. A
// cumulative counter without its delta says "Redis has evicted four million
// keys since it booted", which is true, unactionable, and indistinguishable
// between a crawler that is fine and one that is destroying every release.
func TestStagingCensusDeltas(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, c := range []stagingCensus{
		{ReadyDepth: 10, Sampled: 10, LiveCandidates: 10, EvictedKeys: 1000, ExpiredKeys: 50,
			MaxMemoryPolicy: "allkeys-lru", MemUsedBytes: 1 << 30, MemMaxBytes: 4 << 30},
		{ReadyDepth: 40000, Sampled: 500, LiveCandidates: 460, FossilDropped: 40,
			EvictedKeys: 9400, ExpiredKeys: 62, MaxMemoryPolicy: "allkeys-lru",
			MemUsedBytes: 4 << 30, MemMaxBytes: 4 << 30},
	} {
		if err := s.recordStagingCensus(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.stagingCensusRows(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d samples, want 2", len(rows))
	}
	// Newest first.
	newest := rows[0]
	if newest.ReadyDepth != 40000 || newest.FossilDropped != 40 {
		t.Fatalf("newest sample is not the second insert: %+v", newest)
	}
	if newest.EvictedDelta != 8400 {
		t.Errorf("evicted delta = %d, want 8400 (9400-1000) — without this the "+
			"card shows a cumulative total that never looks alarming",
			newest.EvictedDelta)
	}
	if newest.ExpiredDelta != 12 {
		t.Errorf("expired delta = %d, want 12", newest.ExpiredDelta)
	}
	if !newest.Starved() || !newest.EvictionRisk() {
		t.Error("a starved, evicting sample must trip both signals")
	}
	if got := newest.MemPct(); got != 100 {
		t.Errorf("MemPct = %v, want 100", got)
	}
}

// A Redis restart zeroes the cumulative counters. Reporting the drop as a
// negative delta would render as a nonsensical "-4,000,000 evictions" and, worse,
// invite the reader to treat a restart as recovery.
func TestCensusSurvivesACounterReset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, ev := range []int64{5000, 12} { // second row: Redis restarted
		if err := s.recordStagingCensus(ctx, stagingCensus{EvictedKeys: ev}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.stagingCensusRows(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].EvictedDelta != 0 {
		t.Errorf("delta after a counter reset = %d, want 0", rows[0].EvictedDelta)
	}
}

// The series must stay bounded; a diagnostic table that grows without limit
// becomes the next incident.
func TestStagingCensusPrune(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.recordStagingCensus(ctx, stagingCensus{ReadyDepth: 1}); err != nil {
		t.Fatal(err)
	}
	// Nothing is old enough yet: prune must be a no-op, not a truncate.
	if n, err := s.pruneStagingCensus(ctx, 14); err != nil || n != 0 {
		t.Fatalf("prune removed %d rows (err %v), want 0 — it is deleting live samples", n, err)
	}
	if rows, err := s.stagingCensusRows(ctx, 10); err != nil || len(rows) != 1 {
		t.Fatalf("sample lost to prune: %d rows, err %v", len(rows), err)
	}
}
