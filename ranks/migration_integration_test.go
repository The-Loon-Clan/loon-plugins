//go:build integration

package ranks

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// The groups migration is applied by loon's plugin-migration runner under
// `SET LOCAL search_path = "<plugin>", public`. These cases reproduce that
// against a real Postgres, because everything they assert is a property of the
// database rather than of Go: that unqualified CREATEs land in the plugin
// schema and not in public, that re-running is a no-op, and that the
// depth/cycle guards fire — the last of which already caught a trigger that
// only worked when the caller happened to have the plugin schema on its
// search_path.
//
// Nothing here asserts a SEED any more. The plugin's migrations create an empty
// schema; importing an existing site's data is a separate one-time operation
// that lives host-side (ADOPTION-MIGRATIONS.md).
//
// A scratch schema is used rather than "ranks" so the test cannot collide with
// a real plugin schema in a shared test database.
const testSchema = "ranks_migration_test"

func migrationDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("RANKS_TEST_DSN")
	if dsn == "" {
		t.Skip("RANKS_TEST_DSN not set; skipping integration test.")
	}
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA IF EXISTS " + testSchema + " CASCADE")
		_ = db.Close()
	})
	return db
}

// seedLegacyRanks builds the legacy fixture with the live production catalog.
//
// The fixture lives in the scratch schema, NOT in public: `go test ./...` runs
// packages in parallel against one shared database, so touching public.users or
// public.user_ranks here races pkg/storage/postgres, whose suites truncate
// exactly those tables. (It did — this test knocked over
// TestAgentTokenRepository_CreateAndLookup before being isolated.) Resolution
// still works the same way, because the migration reads the legacy tables
// through search_path rather than by schema name.
func seedLegacyRanks(t *testing.T, db *sqlx.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + testSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// users completes the legacy side for reconcile's orphan sweep, which joins
	// it to find memberships whose account is gone.
	if _, err := db.Exec(`
		CREATE TABLE ` + testSchema + `.users (id SERIAL PRIMARY KEY, username TEXT);
		INSERT INTO ` + testSchema + `.users (id, username) VALUES
		    (1764,'a'),(2141,'b'),(4242,'c'),(900,'d'),(901,'e'),(5150,'f'),(777,'g')`); err != nil {
		t.Fatalf("create legacy users fixture: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE ` + testSchema + `.user_ranks (
		    id SERIAL PRIMARY KEY, name TEXT NOT NULL UNIQUE,
		    color TEXT NOT NULL DEFAULT 'secondary', title_color TEXT NOT NULL DEFAULT '',
		    download_limit INT NOT NULL DEFAULT 100, api_limit INT NOT NULL DEFAULT 10000,
		    monthly_cost INT NOT NULL DEFAULT 0, duration_days INT NOT NULL DEFAULT 30,
		    sort_order INT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		CREATE TABLE ` + testSchema + `.user_rank_subscriptions (
		    id SERIAL PRIMARY KEY, user_id INT NOT NULL,
		    rank_id INT NOT NULL REFERENCES ` + testSchema + `.user_ranks(id) ON DELETE CASCADE,
		    expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    UNIQUE (user_id, rank_id))`); err != nil {
		t.Fatalf("create legacy fixture: %v", err)
	}
	// The audit table the admin user-detail page reads; the dual-write keeps
	// writing it until Stage 3 moves that page onto group_member_history.
	if _, err := db.Exec(`
		CREATE TABLE ` + testSchema + `.user_rank_history (
		    id BIGSERIAL PRIMARY KEY, user_id INT NOT NULL,
		    rank_id INT REFERENCES ` + testSchema + `.user_ranks(id) ON DELETE SET NULL,
		    action TEXT NOT NULL, details TEXT NOT NULL DEFAULT '',
		    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		t.Fatalf("create legacy history fixture: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO ` + testSchema + `.user_ranks (id, name, color, title_color, download_limit, api_limit, monthly_cost, duration_days, sort_order) VALUES
		 (1,'Free','secondary','#ffffff',100,1000,0,30,0),
		 (2,'Kirisame','primary','#0d6efd',150,1500,5000,30,1),
		 (3,'Shigure','info','#0dcaf0',250,2500,10000,30,2),
		 (4,'Samidare','warning','#ffc107',1000,10000,25000,30,3),
		 (5,'Arashi','success','#213c31',5000,50000,45000,30,4)`); err != nil {
		t.Fatalf("seed ranks: %v", err)
	}
	// One live subscription and one already-expired one: the expired row must
	// not migrate, or the cutover silently restores a lapsed benefit.
	if _, err := db.Exec(`
		INSERT INTO ` + testSchema + `.user_rank_subscriptions (user_id, rank_id, expires_at, created_at) VALUES
		 (1764, 5, NOW() + INTERVAL '9 days', NOW() - INTERVAL '21 days'),
		 (1764, 3, NOW() - INTERVAL '3 days', NOW() - INTERVAL '33 days')`); err != nil {
		t.Fatalf("seed subs: %v", err)
	}
}

// applyMigration applies EVERY embedded migration in filename order, one
// transaction each, the way loon's plugin-migration runner does. It used to
// hardcode 001, which made a second migration invisible to these tests: 002 did
// nothing here while being the fix for a live data regression.
func applyMigration(t *testing.T, db *sqlx.DB) {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found")
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		stmt := fmt.Sprintf("SET LOCAL search_path = %s, public;\n", testSchema) + string(body)
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s: %v", name, err)
		}
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}
}

func TestGroupsMigration_IsIdempotent(t *testing.T) {
	db := migrationDB(t)
	seedLegacyRanks(t, db)
	applyMigration(t, db)
	applyMigration(t, db) // a re-run is a boot on an already-migrated database

	// The three MEMBERSHIP tables stay empty, and the legacy fixture is still
	// seeded above precisely to prove it: the migration no longer looks at
	// user_ranks, so a database that happens to have it gets exactly what a
	// fresh one gets. Importing that data is a separate operation now
	// (ADOPTION-MIGRATIONS.md).
	for _, tbl := range []string{"group_entitlements", "group_members", "group_member_history"} {
		var n int
		if err := db.Get(&n, `SELECT count(*) FROM `+testSchema+`.`+tbl); err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("%s = %d rows; the migration must not seed from the host's tables", tbl, n)
		}
	}

	// groups holds the default earned ladder from 005 and NOTHING ELSE — four
	// rows after two applications, which is the idempotency this test is named
	// for. A seed that ran twice would show eight, or would have aborted on the
	// slug UNIQUE and taken boot down with it.
	var n int
	if err := db.Get(&n, `SELECT count(*) FROM `+testSchema+`.groups`); err != nil {
		t.Fatalf("groups: %v", err)
	}
	if n != 4 {
		t.Errorf("groups = %d rows after two applications, want the 4 seeded earned ranks", n)
	}
	var kinds string
	if err := db.Get(&kinds, `SELECT string_agg(DISTINCT kind, ',') FROM `+testSchema+`.groups`); err != nil {
		t.Fatalf("kinds: %v", err)
	}
	if kinds != "earned" {
		t.Errorf("seeded kinds = %q, want only \"earned\" — nothing may be imported from the host", kinds)
	}
}

func TestGroupsMigration_DepthAndCycleGuards(t *testing.T) {
	db := migrationDB(t)
	seedLegacyRanks(t, db)
	applyMigration(t, db)

	// This test builds its own catalog at known ids — which it should have done
	// all along: relying on a seed's ids made it a test of two things at once.
	// 005's default ladder has to go first, since it occupies ids from the same
	// sequence and the depth assertion below is written against exactly five.
	if _, err := db.Exec(`DELETE FROM ` + testSchema + `.groups`); err != nil {
		t.Fatalf("clear seeded ladder: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ` + testSchema + `.groups (id, slug, name, kind, visible, duration_days)
	                      VALUES (1,'a','A','paid',TRUE,30),(2,'b','B','paid',TRUE,30),
	                             (3,'c','C','paid',TRUE,30),(4,'d','D','paid',TRUE,30),
	                             (5,'e','E','paid',TRUE,30)`); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	// Every statement below runs WITHOUT the plugin schema on the search_path,
	// which is the case that caught the original bare-name trigger.
	q := func(sqlStr string, args ...interface{}) error {
		_, err := db.Exec(sqlStr, args...)
		return err
	}
	for _, s := range []string{
		`UPDATE ` + testSchema + `.groups SET parent_id=2 WHERE id=3`,
		`UPDATE ` + testSchema + `.groups SET parent_id=3 WHERE id=4`,
		`UPDATE ` + testSchema + `.groups SET parent_id=4 WHERE id=5`,
	} {
		if err := q(s); err != nil {
			t.Fatalf("building the chain failed: %v", err)
		}
	}
	var depths string
	if err := db.Get(&depths, `SELECT string_agg(id||':'||depth,' ' ORDER BY id) FROM `+testSchema+`.groups`); err != nil {
		t.Fatalf("depths: %v", err)
	}
	if depths != "1:0 2:0 3:1 4:2 5:3" {
		t.Fatalf("depths = %q, want \"1:0 2:0 3:1 4:2 5:3\"", depths)
	}

	for _, c := range []struct {
		name, sqlStr, wantErr string
	}{
		{"self-parent", `UPDATE ` + testSchema + `.groups SET parent_id=2 WHERE id=2`, "groups_no_self_parent"},
		{"descendant-parent exceeds depth", `UPDATE ` + testSchema + `.groups SET parent_id=5 WHERE id=2`, "depth"},
		{"nonexistent parent", `UPDATE ` + testSchema + `.groups SET parent_id=99999 WHERE id=2`, "does not exist"},
	} {
		err := q(c.sqlStr)
		if err == nil {
			t.Errorf("%s was accepted; a cycle or over-deep chain must be rejected", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s rejected with %q, want a %q failure", c.name, err, c.wantErr)
		}
	}
}
