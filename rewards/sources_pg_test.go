//go:build integration

package rewards

// The source catalogue's SQL semantics. These two lived in
// achievements_pg_test.go until the achievements plugin moved out; they are
// about reward_sources, which stays here, so they stayed too.

import (
	"context"
	"testing"
)

// The catalogue is configuration, and this is what makes that true rather than
// a claim: the seed runs into an EMPTY table and never again.
//
// If it re-ran, a host changing its seed would silently overwrite what an
// operator edited, and rows they deliberately deleted would come back on the
// next deploy — at which point it is not configuration, it is a default with
// extra steps.
func TestSeedSourcesRunsOnceAndThenLeavesConfigurationAlone(t *testing.T) {
	db := testDB(t)
	st := testStore(t, db)
	p := &Plugin{admin: st}
	ctx := context.Background()

	seed := SourceCatalog{
		{Key: "posts.created", Label: "Posts created", Group: "Forum",
			Fires: true, Counts: true, Unit: "post", Units: "posts"},
		{Key: "uploads.created", Label: "Uploads", Group: "Uploads",
			Fires: true, Counts: true, Unit: "upload", Units: "uploads"},
	}

	n, err := p.seedSources(ctx, seed)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 2 {
		t.Fatalf("seeded %d, want 2", n)
	}

	// An operator edits the vocabulary: renames one, turns the other off.
	if _, err := db.Exec(`UPDATE rewards.reward_sources SET label = 'Forum posts' WHERE key = 'posts.created'`); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	if _, err := db.Exec(`UPDATE rewards.reward_sources SET enabled = false WHERE key = 'uploads.created'`); err != nil {
		t.Fatalf("operator disable: %v", err)
	}

	// A later boot, with a seed the host has since grown.
	grown := append(seed, SourceDef{Key: "comments.created", Label: "Comments",
		Group: "Comments", Fires: true, Counts: true, Unit: "comment", Units: "comments"})
	if n, err = p.seedSources(ctx, grown); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n != 0 {
		t.Errorf("the seed ran again and wrote %d row(s) — an operator's catalogue is not theirs "+
			"if a deploy can rewrite it", n)
	}

	cat, err := p.Catalogue(ctx)
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	if len(cat) != 1 {
		t.Fatalf("catalogue has %d enabled source(s), want 1 — the disabled row came back", len(cat))
	}
	if cat[0].Label != "Forum posts" {
		t.Errorf("label = %q, want the operator's %q", cat[0].Label, "Forum posts")
	}
}

// The CHECKs are the schema's copy of SourceDef.Valid, so a row written by any
// other route cannot offer a dropdown entry that does nothing.
func TestSchemaRefusesAnUnusableSource(t *testing.T) {
	db := testDB(t)

	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, fires, counts)
		VALUES ('inert', 'Inert', false, false)`); err == nil {
		t.Error("a source that neither fires nor counts was accepted — it would sit in a " +
			"dropdown doing nothing")
	}
	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, counts, unit)
		VALUES ('unnamed', 'Unnamed', true, '')`); err == nil {
		t.Error("a counter with no unit was accepted — every achievement on it would " +
			"suggest a blank name")
	}
	if _, err := db.Exec(`
		INSERT INTO rewards.reward_sources (key, label, fires)
		VALUES ('ok', 'Fires only', true)`); err != nil {
		t.Errorf("a fires-only source was refused: %v", err)
	}
}
