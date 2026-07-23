//go:build integration

package usenet

import (
	"context"
	"strings"
	"testing"
)

// TestBlacklistCRUDValidates: patterns are validated on the way IN so an admin
// sees "invalid regex" while typing, instead of storing a rule that looks active
// and is silently inert.
func TestBlacklistCRUDValidates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.addBlacklistRule(ctx, "([unclosed", "title"); err == nil {
		t.Error("stored an uncompilable pattern")
	}
	if err := s.addBlacklistRule(ctx, "fine", "psoter"); err == nil {
		t.Error("stored an unknown field")
	}
	if err := s.addBlacklistRule(ctx, "   ", "title"); err == nil {
		t.Error("stored an empty pattern")
	}
	if err := s.addBlacklistRule(ctx, "  (?i)spam  ", "poster"); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}

	rules, err := s.blacklistRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].Pattern != "(?i)spam" {
		t.Errorf("pattern = %q, want it trimmed", rules[0].Pattern)
	}
	if !rules[0].Enabled {
		t.Error("a new rule should be enabled — an operator who adds one means it")
	}

	if err := s.toggleBlacklistRule(ctx, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	rules, _ = s.blacklistRules(ctx)
	if rules[0].Enabled {
		t.Error("toggle did not disable the rule")
	}

	if err := s.deleteBlacklistRule(ctx, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ = s.blacklistRules(ctx); len(rules) != 0 {
		t.Errorf("delete left %d rules", len(rules))
	}
}

// TestFilterHitsAccumulateAcrossFlushes is the property the page depends on:
// counts ADD, so a rule that fires rarely is still visible after a week. A
// flush that replaced instead of summing would show only the last pass.
func TestFilterHitsAccumulateAcrossFlushes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := map[filterHitKey]*filterHitVal{
		{"junk", "bare-token"}:   {count: 10, sample: "AbC123"},
		{"blacklist", "spammer"}: {count: 2, sample: "Some.Release"},
	}
	if err := s.recordFilterHits(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Second pass: no new sample for the junk rule, so the old one must survive
	// rather than being blanked.
	second := map[filterHitKey]*filterHitVal{
		{"junk", "bare-token"}: {count: 5, sample: ""},
	}
	if err := s.recordFilterHits(ctx, second); err != nil {
		t.Fatal(err)
	}

	rows, err := s.filterHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Busiest first — the page reads this order straight out.
	if rows[0].Rule != "bare-token" || rows[0].TotalCount != 15 {
		t.Errorf("top row = %s/%d, want bare-token/15", rows[0].Rule, rows[0].TotalCount)
	}
	if rows[0].LastSample != "AbC123" {
		t.Errorf("sample = %q, want the earlier one kept when a pass supplies none", rows[0].LastSample)
	}
	if rows[0].FirstSeen.After(rows[0].LastSeen) {
		t.Error("first_seen_at is after last_seen_at")
	}

	if err := s.resetFilterHits(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ = s.filterHitRows(ctx); len(rows) != 0 {
		t.Errorf("reset left %d rows", len(rows))
	}
}

// TestRecordFilterHitsEmptyIsNoop: a pass that dropped nothing must not touch
// the table (and must not error).
func TestRecordFilterHitsEmptyIsNoop(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.recordFilterHits(ctx, nil); err != nil {
		t.Fatalf("empty flush errored: %v", err)
	}
	if rows, _ := s.filterHitRows(ctx); len(rows) != 0 {
		t.Errorf("empty flush wrote %d rows", len(rows))
	}
}

// TestFilterHitsLongSampleStored: the sample column takes what the truncator
// produces, including multi-byte text.
func TestFilterHitsLongSampleStored(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	sample := truncateSample(strings.Repeat("あ", 400))
	if err := s.recordFilterHits(ctx, map[filterHitKey]*filterHitVal{
		{"junk", "long"}: {count: 1, sample: sample},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.filterHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastSample != sample {
		t.Errorf("stored sample did not round-trip: %+v", rows)
	}
}
