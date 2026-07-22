package usenet

import "testing"

// TestEmbeddedNewsgroupsParse: the pack file is embedded and read at boot, so a
// malformed line would surface as a startup error on every install. An EMPTY
// pack is legal — the mechanism ships before the curated data does.
func TestEmbeddedNewsgroupsParse(t *testing.T) {
	groups, err := embeddedNewsgroups()
	if err != nil {
		t.Fatalf("shipped seed/newsgroups.tsv does not parse: %v", err)
	}
	for _, g := range groups {
		if g.Name == "" {
			t.Error("group with an empty name")
		}
		if !seedPacks[g.Pack] {
			t.Errorf("group %q: pack %q is not one of the known packs", g.Name, g.Pack)
		}
	}
}

// TestSeedRecordsSkipsCommentsAndBlanks pins the file conventions both packs
// rely on: # comments, blank lines, and a minimum column count.
func TestSeedRecordsSkipsCommentsAndBlanks(t *testing.T) {
	// The shipped newsgroups file is currently all comments, which makes it the
	// perfect fixture for "comments produce no records".
	recs, err := seedRecords(seedData, newsgroupSeedPath, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range recs {
		if len(r) == 0 || r[0] == "" {
			t.Errorf("comment or blank line leaked through as a record: %#v", r)
		}
	}

	// The junk file exercises the multi-column path.
	junk, err := seedRecords(seedData, junkSeedPath, 4)
	if err != nil {
		t.Fatalf("junk rules: %v", err)
	}
	if len(junk) < 6 {
		t.Errorf("got %d junk records, want the full shipped set", len(junk))
	}
	for _, r := range junk {
		if len(r) < 4 {
			t.Errorf("record with %d columns survived the minimum-column check: %#v", len(r), r)
		}
	}
}

// TestSeedRecordsRejectsShortRecord: a truncated line must fail loudly rather
// than silently seeding a half-formed rule.
func TestSeedRecordsRejectsShortRecord(t *testing.T) {
	if _, err := seedRecords(seedData, junkSeedPath, 99); err == nil {
		t.Fatal("expected an error when records have fewer than the required columns")
	}
}

func TestColHelper(t *testing.T) {
	rec := []string{"a", "  b  ", "c"}
	if got := col(rec, 0); got != "a" {
		t.Errorf("col 0 = %q", got)
	}
	if got := col(rec, 1); got != "b" {
		t.Errorf("col 1 = %q, want trimmed", got)
	}
	if got := col(rec, 9); got != "" {
		t.Errorf("out-of-range col = %q, want empty", got)
	}
}
