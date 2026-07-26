//go:build integration

package usenet

import (
	"context"
	"testing"
)

// The stored half. Everything asserted here is a property of the SQL — the
// day-bucketed primary key, the additive upsert, and the sample's
// don't-overwrite-with-empty rule — so it needs a real Postgres rather than a
// fake that would only restate the assumptions.

func outcomeRow(t *testing.T, s *PGStore, reason buildOutcome) (count int64, sample string, ok bool) {
	t.Helper()
	err := s.db.DB().QueryRow(
		`SELECT total_count, last_sample FROM `+s.db.Schema()+
			`.build_outcomes WHERE day = CURRENT_DATE AND reason = $1`,
		string(reason)).Scan(&count, &sample)
	if err != nil {
		return 0, "", false
	}
	return count, sample, true
}

func TestRecordBuildOutcomes_AccumulatesIntoTodaysRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := map[buildOutcome]*outcomeVal{
		outcomeIncomplete: {count: 400, sample: "waiting.on.parts"},
		outcomeBuilt:      {count: 3, sample: "a.real.release"},
	}
	if err := s.recordBuildOutcomes(ctx, first); err != nil {
		t.Fatalf("first flush: %v", err)
	}

	// A second pass on the same day must ADD, not replace — otherwise the
	// day's total is just whatever the last pass happened to see.
	second := map[buildOutcome]*outcomeVal{
		outcomeIncomplete: {count: 350, sample: "later.sample"},
	}
	if err := s.recordBuildOutcomes(ctx, second); err != nil {
		t.Fatalf("second flush: %v", err)
	}

	n, sample, ok := outcomeRow(t, s, outcomeIncomplete)
	if !ok {
		t.Fatal("no incomplete row")
	}
	if n != 750 {
		t.Errorf("incomplete = %d, want 750 (400 + 350)", n)
	}
	// The later non-empty sample wins, matching filter_hits: the newest
	// evidence is the more useful one when a bucket is being investigated.
	if sample != "later.sample" {
		t.Errorf("sample = %q, want later.sample", sample)
	}
	if n, _, _ := outcomeRow(t, s, outcomeBuilt); n != 3 {
		t.Errorf("built = %d, want 3 — a flush must not disturb other reasons", n)
	}
}

// An empty sample must not blank a stored one. A pass can note a reason with no
// subject available; losing the existing evidence to that would defeat the
// column's purpose.
func TestRecordBuildOutcomes_EmptySampleDoesNotOverwrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.recordBuildOutcomes(ctx, map[buildOutcome]*outcomeVal{
		outcomeJunk: {count: 1, sample: "keep.this.one"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordBuildOutcomes(ctx, map[buildOutcome]*outcomeVal{
		outcomeJunk: {count: 1, sample: ""},
	}); err != nil {
		t.Fatal(err)
	}

	n, sample, _ := outcomeRow(t, s, outcomeJunk)
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if sample != "keep.this.one" {
		t.Errorf("sample = %q, want the earlier one preserved", sample)
	}
}

func TestRecordBuildOutcomes_EmptyMapIsANoOp(t *testing.T) {
	s := testStore(t)
	if err := s.recordBuildOutcomes(context.Background(), nil); err != nil {
		t.Errorf("nil map returned %v, want nil — an idle pass writes nothing", err)
	}
	if _, _, ok := outcomeRow(t, s, outcomeBuilt); ok {
		t.Error("a no-op flush created a row")
	}
}
