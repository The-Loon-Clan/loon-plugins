//go:build integration

package ranks

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// The portability guarantee: this plugin's migrations must apply to a database
// that has never had the host's legacy rank tables.
//
// That is every fresh loon site once the plugin is lifted to loon-plugins, and
// it is also what has to be true before the legacy tables can be dropped here.
// This used to fail at PARSE time on `FROM user_ranks`, then briefly passed
// only because the seed was wrapped in a to_regclass guard. The seed is gone
// entirely now, so the property holds by construction rather than by guard —
// which is the point of ADOPTION-MIGRATIONS.md.
func TestMigrations_ApplyWithNoLegacyTables(t *testing.T) {
	db := migrationDB(t)
	// Deliberately NOT seedLegacyRanks: a bare schema, as a fresh site has.
	// No empty-stand-in-schema machinery is needed any more either — the
	// migrations do not look at the search_path's second entry at all, so it no
	// longer matters that this shared database's public schema happens to
	// contain user_ranks.
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + testSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	applyMigration(t, db) // must not error

	// The tables exist and are empty — the plugin is usable, just unseeded.
	for _, tbl := range []string{"groups", "group_entitlements", "group_members", "group_member_history"} {
		var n int
		if err := db.Get(&n, `SELECT count(*) FROM `+testSchema+`.`+tbl); err != nil {
			t.Fatalf("%s missing after migration: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows on a fresh install, want 0", tbl, n)
		}
	}

	// The sequence is still initialised, so the first insert cannot collide.
	// This runs outside the guard precisely so an unseeded catalog gets it.
	var seq int64
	if err := db.Get(&seq, `SELECT last_value FROM `+testSchema+`.groups_id_seq`); err != nil {
		t.Fatalf("sequence not initialised: %v", err)
	}
	if seq < 1 {
		t.Errorf("groups_id_seq = %d, want >= 1", seq)
	}

	// And the store works end to end against it: a fresh site can create its
	// first tier without the migration having seeded anything.
	st := NewPGStore(core.NewStorage(db).SchemaDB(testSchema))
	g := &Group{Name: "First", Slug: "first", Kind: "paid", Visible: true, DurationDays: 30}
	if err := st.CreateGroup(context.Background(), g); err != nil {
		t.Fatalf("CreateGroup on a fresh install: %v", err)
	}
	if g.ID == 0 {
		t.Error("CreateGroup returned no id")
	}
}
