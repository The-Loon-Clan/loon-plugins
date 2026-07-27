//go:build integration

package usenet

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
)

// Adoption is the flip-the-switch moment: a production host moves to sink=host
// and the plugin must RESUME the legacy crawler's position, not restart it.
// The expensive property is backfill_done — losing it re-runs a years-long
// backfill against a paying provider.
//
// The legacy tables live in public.*; this test creates prod-shaped ones if
// the shared test database does not already have them (the indexer-site suite
// creates the real thing), uses uniquely-named groups, and removes its rows —
// so it composes with whatever else runs against the database.

func seedHostTables(t *testing.T, db *sqlx.DB) func() {
	t.Helper()
	stmts := []string{
		// PROD-TRUE shape: no backfill_done column (completion is derived from
		// back_watermark <= server_low), no last_crawl, and the per-group
		// tuning columns the legacy settings page writes. The first version of
		// this fixture invented a backfill_done column and the test failed
		// against the shared database's REAL prod schema — exactly the
		// divergence it exists to catch.
		`CREATE TABLE IF NOT EXISTS public.newsgroups (
			id SERIAL PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			active BOOLEAN NOT NULL DEFAULT FALSE,
			high_watermark BIGINT NOT NULL DEFAULT 0,
			high_watermark_date TIMESTAMPTZ,
			back_watermark BIGINT,
			back_watermark_date TIMESTAMPTZ,
			server_low BIGINT NOT NULL DEFAULT 0,
			server_high BIGINT NOT NULL DEFAULT 0,
			sort_order INT NOT NULL DEFAULT 0,
			retention_days INT NOT NULL DEFAULT 6431,
			low_priority BOOLEAN DEFAULT FALSE,
			throttle_ms INT DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS public.blacklist_regexes (
			id BIGSERIAL PRIMARY KEY,
			pattern TEXT NOT NULL,
			field TEXT NOT NULL DEFAULT 'title',
			enabled BOOLEAN NOT NULL DEFAULT TRUE)`,
		// anime: back watermark AT server_low -> derived backfill_done=TRUE.
		// tv: history remains (100 > 1) -> derived FALSE. Tuning on anime.
		`INSERT INTO public.newsgroups
		       (name, active, high_watermark, back_watermark, server_low, server_high,
		        retention_days, throttle_ms, low_priority, sort_order)
		 VALUES ('adopt.test.anime', TRUE, 41000005000, 41000000000, 41000000000, 41000005999, 90, 250, FALSE, 5),
		        ('adopt.test.tv',    TRUE, 900, 100, 1, 999, 6431, 0, TRUE, 0),
		        ('adopt.test.off',   FALSE, 77, 10, 1, 99, 6431, 0, FALSE, 0)
		 ON CONFLICT (name) DO NOTHING`,
		`INSERT INTO public.blacklist_regexes (pattern, field, enabled)
		 VALUES ('(?i)adopt-test-spammer', 'poster', TRUE)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil { // sqllint:allow test fixture, literal DDL
			t.Fatalf("seed host tables: %v", err)
		}
	}
	return func() {
		_, _ = db.Exec(`DELETE FROM public.newsgroups WHERE name LIKE 'adopt.test.%'`)
		_, _ = db.Exec(`DELETE FROM public.blacklist_regexes WHERE pattern = '(?i)adopt-test-spammer'`)
	}
}

func TestAdoptFromHostCarriesEverything(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cleanup := seedHostTables(t, s.db.DB())
	defer cleanup()

	groups, state, blacklist, covRanges, hostFound, err := s.adoptFromHost(ctx, "omicron")
	if err != nil {
		t.Fatal(err)
	}
	if !hostFound {
		t.Fatal("host tables exist but hostFound=false")
	}
	if groups < 3 || state < 2 || blacklist < 1 {
		t.Fatalf("adopted groups=%d state=%d blacklist=%d, want >=3/>=2/>=1", groups, state, blacklist)
	}
	// The coverage map must be seeded to match the watermarks just imported.
	// Left empty, the admin coverage strip reads ~0% for a group whose history
	// the legacy crawler really did index, and the operator's only remaining
	// signal says the opposite of the truth. Both active groups qualify: each
	// has high_watermark above its floored back watermark.
	if covRanges < 2 {
		t.Errorf("adopted coverage ranges=%d, want >=2 (one per active group)", covRanges)
	}

	// The million-dollar bit: backfill_done DERIVED correctly (the legacy
	// schema encodes completion as back_watermark <= server_low), so the
	// plugin will NOT re-run a finished multi-year backfill. Watermarks are
	// the billion-scale numbers a real backbone hands out.
	st, err := s.stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var anime, tv, off bool
	for _, g := range st.Groups {
		switch g.Name {
		case "adopt.test.anime":
			anime = true
			if g.Backbone != "omicron" || !g.BackfillDone || g.HighWatermark != 41000005000 {
				t.Errorf("anime carried wrong: %+v", g)
			}
		case "adopt.test.tv":
			tv = true
			if g.BackfillDone || g.BackWatermark != 100 {
				t.Errorf("tv carried wrong: %+v", g)
			}
		case "adopt.test.off":
			off = true // inactive: group row may exist, but stats only shows active
		}
	}
	if !anime || !tv {
		t.Errorf("adopted groups missing from stats: anime=%v tv=%v", anime, tv)
	}
	if off {
		t.Error("inactive group appeared in active stats")
	}

	// The blacklist rule is live for the matcher's loader.
	rules, err := s.blacklistRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rules {
		if r.Pattern == "(?i)adopt-test-spammer" && r.Field == "poster" && r.Enabled {
			found = true
		}
	}
	if !found {
		t.Errorf("blacklist rule not adopted: %+v", rules)
	}

	// Tuning carried: the operator's legacy crawler-settings survive.
	tuned, err := s.activeGroupsForBackbone(ctx, "omicron", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range tuned {
		if g.Name == "adopt.test.anime" && (g.RetentionDays != 90 || g.ThrottleMs != 250) {
			t.Errorf("anime tuning lost: %+v", g)
		}
		if g.Name == "adopt.test.tv" {
			if g.Tier != TierLow {
				t.Errorf("tv low tier lost: %+v", g)
			}
			// Default-depth rows become INHERIT, not a pinned override —
			// otherwise raising the global depth would silently do nothing.
			if g.RetentionDays != 0 {
				t.Errorf("tv default depth pinned as override: %+v", g)
			}
		}
	}

	// Re-run: everything conflicts away — a partial-failure retry cannot
	// double anything.
	g2, s2, b2, c2, _, err := s.adoptFromHost(ctx, "omicron")
	if err != nil {
		t.Fatal(err)
	}
	if g2 != 0 || s2 != 0 || b2 != 0 || c2 != 0 {
		t.Errorf("re-run duplicated rows: groups=%d state=%d blacklist=%d coverage=%d",
			g2, s2, b2, c2)
	}
}

// TestAdoptFromHostResumePoint: after adoption, the crawl plan for the adopted
// backbone starts exactly one past the legacy watermark — the "resume, don't
// restart" contract, end to end through the group listing the crawler uses.
func TestAdoptFromHostResumePoint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cleanup := seedHostTables(t, s.db.DB())
	defer cleanup()

	if _, _, _, _, _, err := s.adoptFromHost(ctx, "omicron"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.activeGroupsForBackbone(ctx, "omicron", 50)
	if err != nil {
		t.Fatal(err)
	}
	var hw int64
	for _, g := range rows {
		if g.Name == "adopt.test.anime" {
			hw = g.HighWatermark
		}
	}
	if hw != 41000005000 {
		t.Fatalf("crawler would start from watermark %d, want the legacy 41000005000 — anything else re-fetches or skips", hw)
	}
}
