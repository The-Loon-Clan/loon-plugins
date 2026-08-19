//go:build integration

package achievements

import (
	"context"
	"io/fs"
	"os"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// profile_visibility against a real Postgres.
//
// The MemStore can only restate what this file assumes. What it cannot check
// is the half that lives in the schema: that migration 003 applies at all, that
// the column DEFAULT is FALSE (the row a future writer creates without naming
// hidden must mean SHOWN), and that the upsert's conflict target is the one the
// table actually has. A visibility flag that silently defaults the wrong way is
// exactly the kind of bug a struct-cloning double reports as passing.
//
// A scratch schema, not "achievements", so this cannot collide with a real
// plugin schema in a shared test database.
const visibilityTestSchema = "achievements_visibility_test"

func visibilityStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("ACHIEVEMENTS_TEST_DSN")
	if dsn == "" {
		t.Skip("ACHIEVEMENTS_TEST_DSN not set; skipping integration test.")
	}
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA IF EXISTS " + visibilityTestSchema + " CASCADE")
		_ = db.Close()
	})
	if _, err := db.Exec("DROP SCHEMA IF EXISTS " + visibilityTestSchema + " CASCADE"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := db.Exec("CREATE SCHEMA " + visibilityTestSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// The plugin's OWN migrations, from the same embedded FS loon applies, in
	// the same order — so this exercises the shipped files rather than a
	// hand-written copy of the table. (001's data lift is guarded by
	// to_regclass on the old rewards tables and no-ops here.)
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		tx, err := db.Beginx()
		if err != nil {
			t.Fatalf("begin %s: %v", name, err)
		}
		if _, err := tx.Exec("SET LOCAL search_path = " + visibilityTestSchema + ", public"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("search_path for %s: %v", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}
	return NewPGStore(core.NewStorage(db).SchemaDB(visibilityTestSchema))
}

func TestProfileVisibilityRoundTrip(t *testing.T) {
	s := visibilityStore(t)
	ctx := context.Background()

	// A member who has never chosen: shown. This is the read every profile
	// render does, and on a site where nobody has opted out it is the ONLY
	// read — so "no row" must not be an error.
	hidden, err := s.ProfileHidden(ctx, 5)
	if err != nil {
		t.Fatalf("ProfileHidden (no row): %v", err)
	}
	if hidden {
		t.Error("a member with no row is hidden; badges are public by default")
	}

	// Opt out (INSERT branch).
	if err := s.SetProfileHidden(ctx, 5, true); err != nil {
		t.Fatalf("SetProfileHidden(true): %v", err)
	}
	if hidden, err = s.ProfileHidden(ctx, 5); err != nil || !hidden {
		t.Errorf("ProfileHidden = %v, %v; want true, nil", hidden, err)
	}

	// One member's choice is their own.
	if other, err := s.ProfileHidden(ctx, 6); err != nil || other {
		t.Errorf("member 6 = %v, %v; want false, nil — one member's opt-out reached another", other, err)
	}

	// Opt back in (UPDATE branch of the same upsert). A second INSERT would
	// raise 23505 here rather than update, which is the mistake this catches.
	if err := s.SetProfileHidden(ctx, 5, false); err != nil {
		t.Fatalf("SetProfileHidden(false): %v", err)
	}
	if hidden, err = s.ProfileHidden(ctx, 5); err != nil || hidden {
		t.Errorf("ProfileHidden = %v, %v; want false, nil — opting back in did not stick", hidden, err)
	}
}

// The column default, which no Go path sets.
//
// Any future writer that inserts a profile_visibility row without naming
// hidden — a backfill, an admin tool, a later migration — must produce a SHOWN
// member. If that default were TRUE, one such statement would silently hide
// everybody it touched.
func TestProfileVisibilityColumnDefaultIsShown(t *testing.T) {
	s := visibilityStore(t)
	ctx := context.Background()

	if _, err := s.exec(ctx, `INSERT INTO profile_visibility (user_id) VALUES ($1)`, 77); err != nil {
		t.Fatalf("insert with defaults: %v", err)
	}
	hidden, err := s.ProfileHidden(ctx, 77)
	if err != nil {
		t.Fatalf("ProfileHidden: %v", err)
	}
	if hidden {
		t.Error("profile_visibility.hidden defaults to TRUE — a row created without it hides the member")
	}
}
