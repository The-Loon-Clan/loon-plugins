package usenet

import (
	"database/sql"
	"testing"
	"time"
)

func gateRows() []crawlGroupSel {
	old := sql.NullTime{Time: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), Valid: true}
	return []crawlGroupSel{
		{Name: "a.b.critical", TierRaw: string(TierCritical), LastCrawl: old},
		{Name: "a.b.normal", TierRaw: string(TierNormal), LastCrawl: old},
		{Name: "a.b.low-huge", TierRaw: string(TierLow), LastCrawl: old},
		{Name: "a.b.low-two", TierRaw: string(TierLow), LastCrawl: old},
	}
}

func gateNames(rows []crawlGroupSel) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// Tier ordering alone cannot prevent this: once critical and normal are caught
// up they plan instantly and the entire remaining budget falls to whichever low
// group has a backlog. The hold has to REMOVE the tier from the pass, not rank
// it last.
func TestHoldRemovesLowTierEntirely(t *testing.T) {
	got := orderCrawlGroups(gateRows(), 0, true)
	for _, r := range got {
		if normalizeTier(r.TierRaw) == TierLow {
			t.Errorf("low-tier group %q survived the hold: %v", r.Name, gateNames(got))
		}
	}
	if len(got) != 2 {
		t.Fatalf("kept %d groups %v, want the critical and normal ones", len(got), gateNames(got))
	}

	// Ranking low last is NOT sufficient — pin that the un-held path still
	// includes them, so this test would fail if someone "fixed" the hold by
	// reordering instead of excluding.
	if all := orderCrawlGroups(gateRows(), 0, false); len(all) != 4 {
		t.Errorf("without the hold every group must crawl, got %v", gateNames(all))
	}
}

// The hold must not disturb the ordering it does not concern.
func TestHoldPreservesTierOrderForTheRest(t *testing.T) {
	got := orderCrawlGroups(gateRows(), 0, true)
	if len(got) < 2 {
		t.Fatalf("got %v", gateNames(got))
	}
	if tierRank(normalizeTier(got[0].TierRaw)) > tierRank(normalizeTier(got[1].TierRaw)) {
		t.Errorf("hold reordered the surviving tiers: %v", gateNames(got))
	}
}

// The cap applies AFTER the hold, or holding a tier would silently shrink how
// much critical work a pass does.
func TestHoldDoesNotWasteTheCapOnHeldGroups(t *testing.T) {
	got := orderCrawlGroups(gateRows(), 2, true)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	for _, r := range got {
		if normalizeTier(r.TierRaw) == TierLow {
			t.Errorf("a held group consumed a capped slot: %v", gateNames(got))
		}
	}
}

// An all-low install with the hold on crawls nothing. That is correct and it is
// also the shape most likely to be mistaken for a broken crawler, which is why
// the caller logs the distinction — pinned here so the empty result stays
// intentional rather than becoming a surprise.
func TestHoldCanEmptyThePassEntirely(t *testing.T) {
	rows := []crawlGroupSel{
		{Name: "a.b.low-one", TierRaw: string(TierLow)},
		{Name: "a.b.low-two", TierRaw: string(TierLow)},
	}
	if got := orderCrawlGroups(rows, 0, true); len(got) != 0 {
		t.Errorf("expected an empty pass, got %v", gateNames(got))
	}
	if got := orderCrawlGroups(rows, 0, false); len(got) != 2 {
		t.Errorf("without the hold both must crawl, got %v", gateNames(got))
	}
}

// backfillPending.Any is what decides the hold, so "no critical work left" must
// never read as outstanding — otherwise the low tier stays starved forever on a
// fully caught-up site.
func TestPendingAnyIsExact(t *testing.T) {
	if (backfillPending{}).Any() {
		t.Error("an empty result must not hold the low tier")
	}
	if (backfillPending{Groups: 0, Articles: 500}).Any() {
		t.Error("articles with no groups must not hold the tier — the group count decides")
	}
	if !(backfillPending{Groups: 1, Stalest: "a.b.critical"}).Any() {
		t.Error("one outstanding critical group must hold the tier")
	}
}
