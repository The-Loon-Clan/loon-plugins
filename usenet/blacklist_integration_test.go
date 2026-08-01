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

	rows, err := s.ruleHitRows(ctx)
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
	if rows, _ = s.ruleHitRows(ctx); len(rows) != 0 {
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
	if rows, _ := s.ruleHitRows(ctx); len(rows) != 0 {
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
	rows, err := s.ruleHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastSample != sample {
		t.Errorf("stored sample did not round-trip: %+v", rows)
	}
}

// The two populations in filter_hits must not leak into each other's read.
// Before the split, one unfiltered SELECT returned both, which is how 2,257
// grouping observations came to be read as junk rules.
func TestRuleAndDiagnosticReadsAreDisjoint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	hits := map[filterHitKey]*filterHitVal{
		{"junk", "bare-token"}:            {count: 900, sample: "AbC123"},
		{"blacklist", "spammer"}:          {count: 5, sample: "Some.Release"},
		{"ungrouped", "two-blue.vol#"}:    {count: 400, sample: "two-blue.vol029"},
		{"ungrouped", "star-down.vol#"}:   {count: 300, sample: "star-down.vol001"},
		{"ungrouped", "omega-file.vol#"}:  {count: 200, sample: "omega-file.vol7"},
		{"merge_suspect", "E01|E02"}:      {count: 100, sample: "Show E01 / E02"},
		{"parse_dropped", "no-part-info"}: {count: 1, sample: "raw"},
	}
	if err := s.recordFilterHits(ctx, hits); err != nil {
		t.Fatal(err)
	}

	rules, err := s.ruleHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("rule read returned %d rows, want the 2 rules only: %+v", len(rules), rules)
	}
	for _, r := range rules {
		if r.Kind != "junk" && r.Kind != "blacklist" {
			t.Errorf("a %q row reached the rules card", r.Kind)
		}
	}

	// Unfiltered: every instrument, busiest first, with the totals the card
	// needs to state what it did not show.
	all, err := s.diagnosticHits(ctx, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if all.TotalRows != 5 || all.TotalHits != 1001 {
		t.Errorf("totals = %d rows/%d hits, want 5/1001", all.TotalRows, all.TotalHits)
	}
	if len(all.Rows) != 2 {
		t.Fatalf("LIMIT ignored: got %d rows", len(all.Rows))
	}
	if all.Rows[0].Rule != "two-blue.vol#" {
		t.Errorf("not busiest-first: %+v", all.Rows)
	}
	for _, r := range all.Rows {
		if r.Kind == "junk" || r.Kind == "blacklist" {
			t.Errorf("a rule row reached the diagnostics card: %+v", r)
		}
	}
	// The chips carry per-instrument counts, not the global total.
	byKind := map[string]diagKind{}
	for _, k := range all.Kinds {
		byKind[k.Kind] = k
	}
	if byKind["ungrouped"].Rows != 3 || byKind["ungrouped"].Hits != 900 {
		t.Errorf("ungrouped chip = %+v, want 3 rows/900 hits", byKind["ungrouped"])
	}
	if len(all.Kinds) != 3 {
		t.Errorf("got %d chips, want 3 instruments: %+v", len(all.Kinds), all.Kinds)
	}

	// OFFSET pages within a kind, and the totals narrow to that kind — the
	// pager's page count is computed from them, so a global total here would
	// offer pages that render empty.
	page2, err := s.diagnosticHits(ctx, "ungrouped", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page2.TotalRows != 3 || page2.TotalHits != 900 {
		t.Errorf("kind-filtered totals = %d/%d, want 3/900", page2.TotalRows, page2.TotalHits)
	}
	if len(page2.Rows) != 1 || page2.Rows[0].Rule != "omega-file.vol#" {
		t.Errorf("page 2 of ungrouped = %+v", page2.Rows)
	}
	// An unknown kind is a no-op, not an error: ?dkind= is operator-supplied.
	if bogus, err := s.diagnosticHits(ctx, "nope", 25, 0); err != nil || len(bogus.Rows) != 0 || bogus.TotalRows != 0 {
		t.Errorf("unknown kind: rows=%d err=%v", len(bogus.Rows), err)
	}
}

// The prune bounds the instruments without touching rule state. A junk rule's
// lifetime count is what an operator reads to decide whether it earns its
// position — a rule that has gone quiet is the one most worth seeing.
func TestPruneDropsStaleDiagnosticsOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.recordFilterHits(ctx, map[filterHitKey]*filterHitVal{
		{"junk", "quiet-rule"}:      {count: 10, sample: "x"},
		{"ungrouped", "old-stem#"}:  {count: 20, sample: "y"},
		{"ungrouped", "live-stem#"}: {count: 30, sample: "z"},
	}); err != nil {
		t.Fatal(err)
	}
	// Age the quiet rule AND one stem past the horizon. Only the stem may go.
	if _, err := s.db.DB().ExecContext(ctx,
		`UPDATE filter_hits SET last_seen_at = now() - interval '90 days'
		  WHERE rule IN ('quiet-rule', 'old-stem#')`); err != nil {
		t.Fatal(err)
	}

	n, err := s.pruneFilterDiagnostics(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want exactly the 1 stale stem", n)
	}
	rules, err := s.ruleHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].TotalCount != 10 {
		t.Errorf("the prune touched rule state: %+v", rules)
	}
	live, err := s.diagnosticHits(ctx, "", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if live.TotalRows != 1 || live.Rows[0].Rule != "live-stem#" {
		t.Errorf("prune kept the wrong stems: %+v", live.Rows)
	}
}

// Prod lost whole passes of counters to this: a Usenet subject with a byte
// that is not valid UTF-8 reaches a text column, Postgres refuses the value,
// and because the flush is ONE batched statement every count in it goes too.
//
//	pq: invalid byte sequence for encoding "UTF8": 0xe1 0x6e 0x61
//
// A real database is the only place this can be proven: the driver sends the
// bytes untouched and the rejection happens server-side, so no mock sees it.
func TestFilterHitsSurviveInvalidUTF8(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const bad = "Espa\xf1a - Cancio\xe1n.mkv"
	f := newFilterHits()
	f.note("junk", "long_alnum_run", bad)
	// Subject-derived text in the RULE column — the instrument counters do
	// exactly this, so the key must survive as well as the sample.
	f.noteN("ungrouped", "stem-"+bad, 42, bad)

	if err := s.recordFilterHits(ctx, f.drain()); err != nil {
		t.Fatalf("flush rejected: %v", err)
	}

	rules, err := s.ruleHitRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].TotalCount != 1 {
		t.Errorf("rule counter lost: %+v", rules)
	}
	diag, err := s.diagnosticHits(ctx, "", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if diag.TotalRows != 1 || diag.TotalHits != 42 {
		t.Errorf("instrument counter lost: %d row(s), %d hit(s)", diag.TotalRows, diag.TotalHits)
	}
}
