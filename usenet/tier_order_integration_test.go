//go:build integration

package usenet

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The tier column is TEXT, so `ORDER BY g.tier` sorts ALPHABETICALLY: critical,
// low, normal. That puts every LOW group ahead of every NORMAL one — the exact
// inversion the tier exists to prevent — and it was live in all three query
// paths for as long as the feature has existed.
//
// This has to be an integration test. The bug is entirely in the SQL: Go's
// tierRank had the right order all along, so any test that exercised tierRank
// would have passed while production ordered its crawl wrongly.
func TestTierOrderingFollowsRankNotAlphabet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Insert in an order that neither matches the alphabet nor the wanted rank,
	// so a query that happens to preserve insertion order cannot pass by luck.
	seed := []struct {
		name string
		tier Tier
	}{
		{"a.b.low-one", TierLow},
		{"a.b.normal-one", TierNormal},
		{"a.b.critical-one", TierCritical},
		{"a.b.low-two", TierLow},
		{"a.b.normal-two", TierNormal},
	}
	if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for i, g := range seed {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroups (name, active, tier, sort_order)
				 VALUES ($1, TRUE, $2, $3)`, g.name, string(g.tier), i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.allGroups(ctx, "a.b.", 50)
	if err != nil {
		t.Fatal(err)
	}

	var tiers []Tier
	for _, g := range got {
		if len(g.Name) >= 4 && g.Name[:4] == "a.b." {
			tiers = append(tiers, normalizeTier(g.Tier))
		}
	}
	if len(tiers) != len(seed) {
		t.Fatalf("got %d seeded groups back, want %d", len(tiers), len(seed))
	}

	// Ranks must be non-decreasing. Under the alphabetical bug the sequence is
	// critical, low, low, normal, normal → ranks 0,2,2,1,1, which trips here.
	for i := 1; i < len(tiers); i++ {
		if tierRank(tiers[i]) < tierRank(tiers[i-1]) {
			t.Fatalf("tier order violates rank at %d: %v\n"+
				"  ORDER BY is sorting the TEXT column alphabetically "+
				"(critical, low, normal) instead of by priority", i, tiers)
		}
	}
	if tiers[0] != TierCritical {
		t.Errorf("first group is %q, want critical", tiers[0])
	}
	if last := tiers[len(tiers)-1]; last != TierLow {
		t.Errorf("last group is %q, want low", last)
	}
}
