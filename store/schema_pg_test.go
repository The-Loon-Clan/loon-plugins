//go:build integration

package store

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// applyStoreSchema drops + recreates the plugin's "store" schema and
// applies its embedded migrations exactly as loon's
// core.applyPluginMigrations does — each file in its own tx with
// search_path scoped to the schema, so the unqualified table names in
// the .sql land in store.*. Keeping this in lock-step with the runner
// is deliberate: the committed test may not import a sibling plugin
// (the import-boundary lint), and RunPluginMigrations needs the whole
// registry (store Requires ranks), so we reproduce just the one-plugin
// apply here. The real runner is exercised separately at boot.
func applyStoreSchema(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS store CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA store`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	files, err := storeMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		body, err := storeMigrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			t.Fatalf("read %s: %v", f.Name(), err)
		}
		tx, err := db.BeginTxx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		// sqllint:allow migration body is an embedded .sql file, not user input
		if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = store, public;\n"+string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", f.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", f.Name(), err)
		}
	}
}

// TestStorePGRoundTrip verifies the embedded migration builds a usable
// store schema and the schema-qualified PGStore round-trips against real
// Postgres — the stock-claim atomicity and the compensation helpers can
// only be checked against a live DB.
// testStoreDB gives each test its OWN database.
//
// These tests cannot share the suite's database. The 002 seed reads
// public.user_ranks and public.site_settings by hardcoded name, and asserts on
// exactly the rows the fixture puts there — so against a fully migrated schema
// it sees the site's real ranks instead and every expectation is wrong. Worse,
// the fixture used to DROP those two tables to get a clean slate, which fails
// once real foreign keys point at user_ranks, and would have destroyed the
// tables outright on any database that let it.
//
// A private database is the only isolation that actually holds: the plugin
// insists on public.*, so the host tables cannot simply move to another schema.
func testStoreDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("INDEXER_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("INDEXER_TEST_DB_DSN not set")
	}
	admin, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()

	// Name it after the test, sanitised: readable if one ever leaks.
	name := "storetest_" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return '_'
	}, t.Name())

	// sqllint:allow test fixture; name is derived from t.Name() and sanitised above
	_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name)
	// sqllint:allow test fixture; see above
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Skipf("cannot create a private database (%v) — these tests need one, "+
			"because they assert on the exact contents of public.user_ranks", err)
	}
	t.Cleanup(func() {
		a, err := sqlx.Connect("postgres", dsn)
		if err != nil {
			return
		}
		defer a.Close()
		// sqllint:allow test fixture; see above
		_, _ = a.Exec(`DROP DATABASE IF EXISTS ` + name)
	})

	db, err := sqlx.Connect("postgres", swapDBName(dsn, name))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// swapDBName rewrites the database in a postgres URL.
func swapDBName(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

func TestStorePGRoundTrip(t *testing.T) {
	db := testStoreDB(t)
	applyStoreSchema(t, db)

	ctx := context.Background()
	store := NewPGStore(db)

	it := &Item{Name: "VIP 30d", Description: "test item", PointsCost: 100,
		RewardType: "rank", RewardRef: "5", RewardDays: 30, Stock: 2, Active: true}
	if err := store.CreateItem(ctx, it); err != nil {
		t.Fatalf("create: %v", err)
	}
	if it.ID == 0 || it.CreatedAt.IsZero() {
		t.Fatalf("create did not populate RETURNING fields: %+v", it)
	}

	got, err := store.GetItem(ctx, it.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "VIP 30d" || got.PointsCost != 100 || got.Stock != 2 || got.RewardRef != "5" {
		t.Fatalf("get mismatch: %+v", got)
	}

	// An inactive item must not appear in the active-only listing.
	inactive := &Item{Name: "hidden", PointsCost: 50, RewardType: "rank", RewardRef: "5", Stock: -1, Active: false}
	if err := store.CreateItem(ctx, inactive); err != nil {
		t.Fatalf("create inactive: %v", err)
	}
	active, err := store.ListItems(ctx, true)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ID != it.ID {
		t.Fatalf("active list = %d items, want just the active one", len(active))
	}

	// Stock 2 → claim, claim, then sold out.
	for i, wantOK := range []bool{true, true, false} {
		ok, err := store.ClaimStock(ctx, it.ID)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if ok != wantOK {
			t.Fatalf("claim %d ok=%v, want %v", i, ok, wantOK)
		}
	}
	// Restore one unit back to stock.
	if err := store.RestoreStock(ctx, it.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if g, _ := store.GetItem(ctx, it.ID); g.Stock != 1 {
		t.Fatalf("stock after restore = %d, want 1", g.Stock)
	}

	if err := store.RecordPurchase(ctx, 42, it.ID, 100); err != nil {
		t.Fatalf("record purchase: %v", err)
	}
	var n int
	if err := db.GetContext(ctx, &n, `SELECT count(*) FROM store.purchases WHERE user_id=42`); err != nil {
		t.Fatalf("count purchases: %v", err)
	}
	if n != 1 {
		t.Fatalf("purchases = %d, want 1", n)
	}

	if err := store.DeleteItem(ctx, it.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetItem(ctx, it.ID); err == nil {
		t.Fatal("GetItem after delete should error (no rows)")
	}
}

// seedHostTables creates the host tables the 002 seed reads, shaped like
// production (verified against the live schema), and fills them with the rows
// the /profile Points card used to sell from.
//
// It removes them again on cleanup, which is load-bearing rather than tidy:
// the integration DB is shared across tests, 002 is applied by every
// applyStoreSchema, and leaving user_ranks behind makes the seed fire for
// tests that expect an empty catalog — TestStorePGRoundTrip counts its own
// items and would see these three too, passing on a fresh database and failing
// on the next run.
func seedHostTables(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	// No teardown: the database itself is dropped by testStoreDB's cleanup.
	// This used to DROP the two tables, which fails once real foreign keys
	// reference user_ranks and would have destroyed a shared database's schema.
	stmts := []string{
		`CREATE TABLE public.user_ranks (
			id SERIAL PRIMARY KEY, name TEXT, color TEXT, download_limit INT,
			api_limit INT, monthly_cost INT, sort_order INT,
			created_at TIMESTAMPTZ DEFAULT NOW(), duration_days INT, title_color TEXT)`,
		`CREATE TABLE public.site_settings (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO public.user_ranks (id,name,download_limit,api_limit,monthly_cost,duration_days) VALUES
			(1,'Free',20,100,0,0), (2,'Kirisame',150,1000,5000,30), (3,'Arashi',5000,10000,45000,30)`,
		`INSERT INTO public.site_settings (key,value) VALUES ('points_invite_cost','50000')`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil { // sqllint:allow test fixture, literal DDL
			t.Fatalf("seed host tables: %v", err)
		}
	}
}

// The 002 seed carries the profile card's hardcoded store into the catalog.
// It is the reason the move doesn't silently empty the store, and it reads
// host tables across a schema boundary — so it gets a real database.
// applyProfileImport runs the ADOPTION import that used to be migration 002.
// It reads the file from disk rather than the embedded FS because it is no
// longer part of the plugin — a plugin's migrations create its own schema and
// nothing else, and carrying an existing site's data across is a separate,
// deliberate operation (ADOPTION-MIGRATIONS.md).
//
// These two tests came with it. They are the only coverage the importer has,
// and both properties are ones a hand-run SQL file gets wrong easily: that
// re-running does not duplicate, and that it does not overwrite a price an
// admin has since changed.
func applyProfileImport(t *testing.T, db *sqlx.DB) {
	t.Helper()
	body, err := os.ReadFile("../../deploy/import/store_from_profile.sql")
	if err != nil {
		t.Fatalf("read importer: %v", err)
	}
	// sqllint:allow the importer is a repo file, not user input
	if _, err := db.Exec("SET search_path = store, public;\n" + string(body)); err != nil {
		t.Fatalf("run importer: %v", err)
	}
}

func TestImportCarriesProfileStoreIntoCatalog(t *testing.T) {
	db := testStoreDB(t)
	seedHostTables(t, db)
	applyStoreSchema(t, db)
	applyProfileImport(t, db)

	type row struct {
		Name       string `db:"name"`
		PointsCost int    `db:"points_cost"`
		RewardType string `db:"reward_type"`
		RewardRef  string `db:"reward_ref"`
		RewardDays int    `db:"reward_days"`
	}
	var got []row
	if err := db.Select(&got, `SELECT name, points_cost, reward_type, reward_ref, reward_days
	                             FROM store.items ORDER BY sort_order`); err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want invite + 2 paid ranks, got %d: %+v", len(got), got)
	}

	// The invite, priced from the setting the profile handler read.
	if got[0].Name != "Invite" || got[0].PointsCost != 50000 || got[0].RewardType != "invite" || got[0].RewardRef != "1" {
		t.Errorf("invite item wrong: %+v", got[0])
	}
	// Ranks keep the price they had on the card, and point at their rank id.
	if got[1].Name != "Kirisame" || got[1].PointsCost != 5000 || got[1].RewardRef != "2" || got[1].RewardDays != 30 {
		t.Errorf("Kirisame item wrong: %+v", got[1])
	}
	if got[2].Name != "Arashi" || got[2].PointsCost != 45000 || got[2].RewardRef != "3" {
		t.Errorf("Arashi item wrong: %+v", got[2])
	}
	// The free rank must NOT become an item: the card only ever showed ranks
	// with a cost, and a 0-point item would be a "buy" button on nothing.
	for _, r := range got {
		if r.Name == "Free" {
			t.Error("the free rank was seeded as a purchasable item")
		}
	}
}

// The catalog owns the price now, so re-running the seed must not reset an
// admin's edit — otherwise every deploy would silently undo /admin/store.
func TestImportIsIdempotentAndPreservesAdminPricing(t *testing.T) {
	db := testStoreDB(t)
	seedHostTables(t, db)
	applyStoreSchema(t, db)
	applyProfileImport(t, db)

	if _, err := db.Exec(`UPDATE store.items SET points_cost = 1 WHERE name = 'Kirisame'`); err != nil {
		t.Fatalf("re-price: %v", err)
	}

	// Re-run the importer twice. An operator re-running an import by hand is
	// the expected case, not an accident, so it has to be safe.
	applyProfileImport(t, db)
	applyProfileImport(t, db)

	var n int
	if err := db.Get(&n, `SELECT count(*) FROM store.items`); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("the importer duplicated items on re-run: %d", n)
	}
	var cost int
	if err := db.Get(&cost, `SELECT points_cost FROM store.items WHERE name = 'Kirisame'`); err != nil {
		t.Fatal(err)
	}
	if cost != 1 {
		t.Errorf("the importer clobbered an admin's price: %d — the catalog is supposed to own it", cost)
	}
}
