// Package pgtest gives a plugin's store a real Postgres to be tested against.
//
// WHY IT EXISTS. Most of what a plugin's storage layer decides is decided IN
// SQL — the ownership check inside an UPDATE's WHERE, the ON CONFLICT that
// makes a second purchase extend an unlock rather than truncate it, the
// DISTINCT ON that picks the newest row. None of that is reachable from a unit
// test, and a hand-written in-memory double can only restate what its author
// already believed. Those are exactly the rules worth being sure about, because
// they are the ones standing between a forged form post and somebody else's
// data.
//
// This repo already had thirty-one integration tests when this was written, and
// every one of them opened its own connection, invented its own environment
// variable — ACHIEVEMENTS_TEST_DSN, NEWS_TEST_DSN, RANKS_TEST_DSN, and six more
// — and either hand-wrote a CREATE TABLE that could drift from the shipped
// migration or copied fifty lines of schema setup from a neighbour. That is the
// duplication this replaces.
//
// HOW TO USE IT, in full:
//
//	//go:build integration
//
//	func testStore(t *testing.T) *PGStore {
//		return NewPGStore(pgtest.SchemaDB(t, "cosmetics_test", migrations))
//	}
//
// The second argument is a scratch schema name, NOT the plugin's real one, so a
// test run cannot drop a schema somebody is using. The third is the plugin's
// own embedded migrations FS — the shipped files, applied in the order loon
// applies them, so a test exercises the real schema rather than a copy of it
// that stopped matching two migrations ago.
//
// WITH NO DATABASE the helpers skip rather than fail, which is what lets
// `go test ./...` stay useful on a laptop with nothing running. That is also
// the trap: a skipped test is a green test. `make itest` exists so the skip is
// a choice rather than the default, and CI runs it.
package pgtest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// EnvDSN is the one environment variable to set. It is the same name the host
// repo's `make itest` exports, so one throwaway database serves both.
const EnvDSN = "LOON_TEST_DSN"

// legacyEnv are the per-plugin variables that existed before this package.
//
// Kept as fallbacks rather than removed: somebody has these in a shell profile
// or a CI secret, and a rename that silently turns their integration tests into
// skips is the worst possible outcome — the suite would go green by doing less.
var legacyEnv = []string{
	"ACHIEVEMENTS_TEST_DSN",
	"BACKUP_TEST_DSN",
	"EVENTS_TEST_DSN",
	"NEWS_TEST_DSN",
	"RANKS_TEST_DSN",
	"REWARDS_TEST_DSN",
	"TICKETS_TEST_DSN",
	"TRACKER_TEST_DSN",
	"USENET_TEST_DSN",
	"INDEXER_TEST_DB_DSN",
}

// DSN returns the test database's connection string, or skips the test.
func DSN(t *testing.T) string {
	t.Helper()
	if dsn := strings.TrimSpace(os.Getenv(EnvDSN)); dsn != "" {
		return dsn
	}
	for _, name := range legacyEnv {
		if dsn := strings.TrimSpace(os.Getenv(name)); dsn != "" {
			t.Logf("using %s; prefer %s, which `make itest` sets", name, EnvDSN)
			return dsn
		}
	}
	t.Skipf("%s is not set — run `make itest`, or export it to point at a throwaway Postgres", EnvDSN)
	return ""
}

// Connect opens a pool against the test database and closes it after the test.
func Connect(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("postgres", DSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping test database: %v — is it up? `make itest` starts one", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// SchemaDB builds a scratch schema, applies a plugin's migrations into it, and
// returns the scoped handle a PGStore takes.
//
// The schema is dropped BEFORE the run as well as after. Dropping only after
// looks equivalent and is not: a test binary killed part way through (a
// timeout, a panic, ctrl-C) leaves the schema behind, and the next run would
// then apply migrations on top of populated tables and fail somewhere far from
// the cause.
//
// Migrations are applied with `SET LOCAL search_path` rather than by rewriting
// the SQL, because that is how the host applies them — a migration that only
// works when its table names are qualified would pass here and fail in
// production, which is the one thing this must not do.
func SchemaDB(t *testing.T, schema string, migrations fs.FS) *core.SchemaDB {
	t.Helper()
	if err := validSchemaName(schema); err != nil {
		t.Fatalf("pgtest.SchemaDB: %v", err)
	}
	db := Connect(t)

	// The four statements below concatenate an identifier, because Postgres
	// will not accept a schema name as a bind parameter. validSchemaName above
	// is the parameterisation, and it runs before any of them; it has its own
	// test, since it is the whole of the guard.
	drop := "DROP SCHEMA IF EXISTS " + schema + " CASCADE"
	// sqllint:allow schema passed validSchemaName
	if _, err := db.Exec(drop); err != nil {
		t.Fatalf("reset schema %s: %v", schema, err)
	}
	// sqllint:allow schema passed validSchemaName
	t.Cleanup(func() { _, _ = db.Exec(drop) })
	// sqllint:allow schema passed validSchemaName
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}

	if migrations != nil {
		applyMigrations(t, db, schema, migrations)
	}
	return core.NewStorage(db).SchemaDB(schema)
}

// applyMigrations runs migrations/*.sql in name order, one transaction each.
func applyMigrations(t *testing.T, db *sqlx.DB, schema string, migrations fs.FS) {
	t.Helper()
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(names) == 0 {
		// Silence here would mean a store tested against an empty schema, and
		// the failure would arrive as "relation does not exist" from whichever
		// query ran first.
		t.Fatalf("no migrations/*.sql in the supplied FS — is the //go:embed pattern right?")
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
		// sqllint:allow schema passed validSchemaName
		if _, err := tx.Exec("SET LOCAL search_path = " + schema + ", public"); err != nil {
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
}

// Truncate empties the named tables and resets their sequences, for a test that
// wants a clean slate without paying for the migrations again.
//
// RESTART IDENTITY matters more than it looks: without it the ids keep climbing
// between tests, and a test that hard-codes an id passes alone and fails in a
// suite. CASCADE because these tables reference each other.
//
// Through WithTx, so the search_path is set with SET LOCAL and dies with the
// transaction. A bare `SET search_path` here would land on whichever pooled
// connection this got and stay there for whoever picked it up next — which is
// a test that passes or fails depending on connection assignment.
func Truncate(t *testing.T, db *core.SchemaDB, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	for _, name := range tables {
		if err := validSchemaName(name); err != nil {
			t.Fatalf("pgtest.Truncate: %v", err)
		}
	}
	stmt := "TRUNCATE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	err := db.WithTx(context.Background(), func(tx *sqlx.Tx) error {
		// sqllint:allow validated identifiers, never member input
		_, err := tx.Exec(stmt)
		return err
	})
	if err != nil {
		t.Fatalf("truncate %v: %v", tables, err)
	}
}

// validSchemaName refuses anything that is not a plain lowercase identifier.
//
// These names are concatenated into SQL because a schema name cannot be a bind
// parameter. That is the one place in this repo where a string reaches a
// statement unparameterised, so it is worth being explicit: the allowed set is
// [a-z0-9_], must start with a letter, and everything else is refused before it
// gets near a query.
func validSchemaName(s string) error {
	if s == "" {
		return fmt.Errorf("empty identifier")
	}
	if s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("identifier %q must start with a lowercase letter", s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("identifier %q may only hold [a-z0-9_]", s)
		}
	}
	if len(s) > 63 {
		return fmt.Errorf("identifier %q is longer than Postgres allows", s)
	}
	return nil
}
