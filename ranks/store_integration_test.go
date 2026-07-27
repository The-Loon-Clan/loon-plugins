//go:build integration

package ranks

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// Dual-write and reconcile are properties of the SQL, not of Go: they turn on
// cross-schema resolution, one transaction spanning both models, ON CONFLICT
// semantics, and a FK cascade. A fake store could only restate the assumptions
// under test, so these run against a real Postgres.
//
// The plugin's tables go in the scratch schema used by the migration tests, and
// the legacy fixture goes there too — `go test ./...` runs packages in parallel
// against one database, so writing public.user_ranks here would race
// pkg/storage/postgres. Resolution is unaffected: the mirror is reached through
// search_path either way.
//
// The one thing this cannot exercise is the literal `public.` qualification in
// the mirror SQL. storeFixture rewrites it to the scratch schema, so a
// regression that pointed the mirror at the wrong SCHEMA would slip through
// while every other property stays covered.

func storeFixture(t *testing.T) (*PGStore, *sqlx.DB) {
	t.Helper()
	db := migrationDB(t)
	seedLegacyRanks(t, db)
	applyMigration(t, db)
	// The catalog is seeded HERE, by the test, because the migration no longer
	// does it — importing an existing site's tiers is a separate operation now
	// (ADOPTION-MIGRATIONS.md). These ids and names match the production
	// catalog the tests below refer to by number, notably 5 = Arashi.
	if _, err := db.Exec(`INSERT INTO ` + testSchema + `.groups
		(id, slug, name, kind, visible, color, title_color, cost_points, duration_days, sort_order) VALUES
		 (1,'free','Free','paid',TRUE,'secondary','#ffffff',0,30,0),
		 (2,'kirisame','Kirisame','paid',TRUE,'primary','#0d6efd',5000,30,1),
		 (3,'shigure','Shigure','paid',TRUE,'info','#0dcaf0',10000,30,2),
		 (4,'samidare','Samidare','paid',TRUE,'warning','#ffc107',25000,30,3),
		 (5,'arashi','Arashi','paid',TRUE,'success','#213c31',45000,30,4)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	// Entitlements for the paid tiers, matching what the catalog confers in
	// production — the badge/display tests read these back.
	if _, err := db.Exec(`INSERT INTO ` + testSchema + `.group_entitlements (group_id, key, val)
		SELECT id, 'download.daily', CASE id WHEN 2 THEN 150 WHEN 3 THEN 250 WHEN 4 THEN 1000 WHEN 5 THEN 5000 END
		  FROM ` + testSchema + `.groups WHERE id > 1
		UNION ALL
		SELECT id, 'api.daily', CASE id WHEN 2 THEN 1500 WHEN 3 THEN 2500 WHEN 4 THEN 10000 WHEN 5 THEN 50000 END
		  FROM ` + testSchema + `.groups WHERE id > 1
		UNION ALL
		SELECT id, 'dm.initiate', 1 FROM ` + testSchema + `.groups WHERE id > 1
		ON CONFLICT (group_id, key) DO NOTHING`); err != nil {
		t.Fatalf("seed entitlements: %v", err)
	}
	if _, err := db.Exec(`SELECT setval(pg_get_serial_sequence('` + testSchema + `.groups','id'),
	                      (SELECT MAX(id) FROM ` + testSchema + `.groups))`); err != nil {
		t.Fatalf("bump sequence: %v", err)
	}
	return NewPGStore(core.NewStorage(db).SchemaDB(testSchema)), db
}

func TestExpireMemberships_SweepsLapsedAndSparesPermanent(t *testing.T) {
	st, db := storeFixture(t)
	ctx := context.Background()

	// A lapsed paid membership, and a permanent one on an assigned group.
	if _, err := db.Exec(`INSERT INTO ` + testSchema + `.group_members (user_id, group_id, expires_at)
	                      VALUES (900, 5, NOW() - INTERVAL '1 hour')`); err != nil {
		t.Fatalf("seed lapsed: %v", err)
	}
	staff := &Group{Name: "Staff", Slug: "staff", Kind: "assigned", Visible: false, DurationDays: 30}
	if err := st.CreateGroup(ctx, staff); err != nil {
		t.Fatalf("create staff: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+testSchema+`.group_members (user_id, group_id, expires_at)
	                      VALUES (901, $1, NULL)`, staff.ID); err != nil {
		t.Fatalf("seed permanent: %v", err)
	}

	expired, err := st.ExpireMemberships(ctx)
	if err != nil {
		t.Fatalf("ExpireMemberships: %v", err)
	}
	if len(expired) != 1 || expired[0].UserID != 900 {
		t.Fatalf("expired = %+v, want exactly the lapsed membership", expired)
	}

	// The NULL-expiry membership must survive: without `expires_at IS NOT NULL`
	// in the predicate the first tick deletes every staff assignment.
	var perm int
	_ = db.QueryRow(`SELECT count(*) FROM `+testSchema+`.group_members WHERE user_id=901 AND group_id=$1`, staff.ID).Scan(&perm)
	if perm != 1 {
		t.Fatal("a permanent membership was swept by the expiry job")
	}

	// ...and the sweep was recorded, which is what the admin audit panel reads.
	var hist int
	_ = db.QueryRow(`SELECT count(*) FROM ` + testSchema + `.group_member_history WHERE user_id=900 AND action='expired'`).Scan(&hist)
	if hist != 1 {
		t.Error("expiry was not recorded in history")
	}
}
