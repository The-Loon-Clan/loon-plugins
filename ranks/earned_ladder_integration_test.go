//go:build integration

package ranks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// The default earned ladder (migration 005), against a real database.
//
// Everything worth proving here is a property of the SQL — a NOT EXISTS guard,
// a sort_order computed from the live catalog, an ON CONFLICT — so a fake would
// only restate the assumptions. And the case that matters most is the one the
// other migration tests structurally cannot reach: applying 005 to a catalog
// that ALREADY HAS PAID TIERS, which is every real upgrade and is not what
// applyMigration sets up, since the migrations have to run before anything can
// be inserted.

// applyLadderMigration applies 001..004, lets the caller shape the catalog, and
// then applies 005 — the ordering a running site actually experiences.
func applyLadderMigration(t *testing.T, db *sqlx.DB, shape func()) {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	const ladder = "005_earned_ladder.sql"
	apply := func(name string) {
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
	for _, name := range names {
		if name == ladder {
			continue
		}
		apply(name)
	}
	if shape != nil {
		shape()
	}
	apply(ladder)
}

// freshLadderSchema is a bare plugin schema, as a site has before 005 runs.
func freshLadderSchema(t *testing.T) *sqlx.DB {
	t.Helper()
	db := migrationDB(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + testSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

// seedPaidTiers installs the shape of a site that has been selling ranks for a
// while — the ameNZB catalog, sort_order 0..4, no earned tier anywhere.
func seedPaidTiers(t *testing.T, db *sqlx.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO ` + testSchema + `.groups
		(slug, name, kind, visible, color, cost_points, duration_days, sort_order) VALUES
		 ('free','Free','paid',TRUE,'secondary',0,30,0),
		 ('kirisame','Kirisame','paid',TRUE,'primary',5000,30,1),
		 ('shigure','Shigure','paid',TRUE,'info',10000,30,2),
		 ('samidare','Samidare','paid',TRUE,'warning',25000,30,3),
		 ('arashi','Arashi','paid',TRUE,'success',45000,30,4)`); err != nil {
		t.Fatalf("seed paid tiers: %v", err)
	}
}

// THE REGRESSION THIS FILE EXISTS FOR.
//
// Every seeded rung must sort BELOW every rank the site already had, and that
// is not a matter of taste. BadgesForBatch returns badges most-prominent-first
// by sort_order and single-badge consumers take the head; the Discord bot is
// one, and it maps that head slug to a guild role and then strips every OTHER
// configured rank role from the member. A rung seeded above the paid tiers
// therefore makes the next five-minute role sync resolve an unconfigured slug
// to the empty string and remove the role of the members who are PAYING for it.
// On the site this was written against, all three paid-rank holders were in the
// first promotion batch, so the blast radius was 100% of paying members.
func TestSeededLadderSortsBelowEveryExistingRank(t *testing.T) {
	db := freshLadderSchema(t)
	applyLadderMigration(t, db, func() { seedPaidTiers(t, db) })

	var earnedMax, otherMin int
	if err := db.Get(&earnedMax,
		`SELECT max(sort_order) FROM `+testSchema+`.groups WHERE kind = 'earned'`); err != nil {
		t.Fatalf("earned sort_order: %v", err)
	}
	if err := db.Get(&otherMin,
		`SELECT min(sort_order) FROM `+testSchema+`.groups WHERE kind <> 'earned'`); err != nil {
		t.Fatalf("paid sort_order: %v", err)
	}
	if earnedMax >= otherMin {
		t.Errorf("top earned rung sorts at %d, lowest existing rank at %d — an earned rung at or "+
			"above a paid one becomes the member's primary badge, and the Discord role sync "+
			"then strips the paid role it displaces", earnedMax, otherMin)
	}

	// And the display path agrees, which is the assertion that survives a
	// refactor of how the badge order is computed. A member holding both must
	// see the rank they bought at the head.
	st := NewPGStore(core.NewStorage(db).SchemaDB(testSchema))
	ctx := context.Background()
	var arashi, uploader int
	if err := db.Get(&arashi, `SELECT id FROM `+testSchema+`.groups WHERE slug='arashi'`); err != nil {
		t.Fatalf("arashi id: %v", err)
	}
	if err := db.Get(&uploader, `SELECT id FROM `+testSchema+`.groups WHERE slug='uploader'`); err != nil {
		t.Fatalf("uploader id: %v", err)
	}
	if err := st.AddMember(ctx, 2141, arashi, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember paid: %v", err)
	}
	if err := st.AddMember(ctx, 2141, uploader, 0); err != nil { // as the sweep grants
		t.Fatalf("AddMember earned: %v", err)
	}
	badges, err := (&groupDisplay{store: st}).BadgesFor(ctx, 2141)
	if err != nil {
		t.Fatalf("BadgesFor: %v", err)
	}
	if len(badges) != 2 || badges[0].Slug != "arashi" {
		t.Errorf("badges = %+v; the paid rank must stay at the head, because that is the "+
			"slug the Discord role sync maps", badges)
	}
}

// The seeded rungs are recognition, not a quota change — so the first sweep
// cannot silently re-rate a batch of members' download or API limits.
func TestSeededLadderConfersNoEntitlements(t *testing.T) {
	db := freshLadderSchema(t)
	applyLadderMigration(t, db, func() { seedPaidTiers(t, db) })

	var n int
	if err := db.Get(&n, `SELECT count(*) FROM `+testSchema+`.group_entitlements ge
	                      JOIN `+testSchema+`.groups g ON g.id = ge.group_id
	                     WHERE g.kind = 'earned'`); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 0 {
		t.Errorf("the seeded ladder confers %d entitlement(s); promoting a batch of members "+
			"must not move anybody's quota without an operator asking for it", n)
	}
}

// Every rung must be gated on releases AND age together, and Automatic() must
// agree — a rung with no criteria is one the sweep would hand to everybody.
func TestSeededLadderIsGatedOnReleasesAndAge(t *testing.T) {
	db := freshLadderSchema(t)
	applyLadderMigration(t, db, func() { seedPaidTiers(t, db) })

	groups, err := NewPGStore(core.NewStorage(db).SchemaDB(testSchema)).Groups(context.Background())
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	var earned []Group
	for _, g := range groups {
		if g.Kind == "earned" {
			earned = append(earned, g)
		}
	}
	if len(earned) != 4 {
		t.Fatalf("got %d earned ranks, want 4", len(earned))
	}
	for _, g := range earned {
		if !g.Automatic() {
			t.Errorf("%s is not automatic; the sweep will never promote to it", g.Slug)
		}
		if g.MinReleases <= 0 || g.MinAgeDays <= 0 {
			t.Errorf("%s: min_releases=%d min_age_days=%d — a rung gated on only one of the two "+
				"either rewards waiting or lets a day-old bulk import reach the top",
				g.Slug, g.MinReleases, g.MinAgeDays)
		}
		// No byte criteria: this host has no tracker, reports zero for every
		// member, and Qualifies fails closed — so a byte floor here would make
		// the rung permanently unreachable.
		if g.MinUploaded != 0 || g.MinRatio != 0 {
			t.Errorf("%s carries a byte criterion (uploaded=%d ratio=%v); on a host with no "+
				"tracker every member reads zero and the rung can never be earned",
				g.Slug, g.MinUploaded, g.MinRatio)
		}
	}
	// Strictly increasing on both axes and on prominence, walking the ladder up.
	sort.Slice(earned, func(i, j int) bool { return earned[i].SortOrder < earned[j].SortOrder })
	for i := 1; i < len(earned); i++ {
		if earned[i].MinReleases <= earned[i-1].MinReleases || earned[i].MinAgeDays <= earned[i-1].MinAgeDays {
			t.Errorf("rung %s is not strictly harder than %s; bestClass picks the highest "+
				"sort_order that qualifies, so a non-monotonic ladder promotes past its own rungs",
				earned[i].Slug, earned[i-1].Slug)
		}
	}
}

// A site that has already built its own earned tiers has answered this
// question. It must not be handed four more — which is exactly the demo host,
// whose seeder creates Newcomer/Regular/Contributor.
func TestSeedSkipsACatalogThatAlreadyHasEarnedRanks(t *testing.T) {
	db := freshLadderSchema(t)
	applyLadderMigration(t, db, func() {
		seedPaidTiers(t, db)
		if _, err := db.Exec(`INSERT INTO ` + testSchema + `.groups
			(slug, name, kind, visible, color, cost_points, duration_days, sort_order)
			VALUES ('newcomer','Newcomer','earned',TRUE,'secondary',0,30,10)`); err != nil {
			t.Fatalf("seed existing earned tier: %v", err)
		}
	})

	var slugs string
	if err := db.Get(&slugs, `SELECT string_agg(slug, ',' ORDER BY slug) FROM `+testSchema+`.groups
	                          WHERE kind = 'earned'`); err != nil {
		t.Fatalf("earned slugs: %v", err)
	}
	if slugs != "newcomer" {
		t.Errorf("earned ranks = %q, want only the site's own \"newcomer\" — a site that has "+
			"configured its ladder must not have a second one appear under it", slugs)
	}
}

// Re-running the migration is a boot on an already-migrated database. It must
// insert nothing the second time and must not trip the slug UNIQUE, which would
// abort the whole boot.
func TestSeedIsIdempotentOnRerun(t *testing.T) {
	db := freshLadderSchema(t)
	applyLadderMigration(t, db, func() { seedPaidTiers(t, db) })

	before := ladderFingerprint(t, db)
	// Re-apply just 005, which is what a runner without the applied-set would do.
	applyLadderMigration(t, db, nil)
	if after := ladderFingerprint(t, db); after != before {
		t.Errorf("re-running the seed changed the catalog\nbefore: %s\n after: %s", before, after)
	}
}

func ladderFingerprint(t *testing.T, db *sqlx.DB) string {
	t.Helper()
	var s string
	if err := db.Get(&s, `SELECT COALESCE(string_agg(slug||':'||sort_order||':'||min_releases||':'||min_age_days,
	                                                 ' ' ORDER BY sort_order), '')
	                        FROM `+testSchema+`.groups WHERE kind = 'earned'`); err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return s
}
