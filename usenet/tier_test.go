package usenet

import (
	"database/sql"
	"testing"
	"time"
)

func TestNormalizeTier(t *testing.T) {
	cases := map[string]Tier{
		"critical": TierCritical,
		"normal":   TierNormal,
		"low":      TierLow,
		// Anything unrecognised must land on normal, not vanish: a group the
		// operator can no longer see is worse than one at the wrong priority.
		"":         TierNormal,
		"CRITICAL": TierNormal, // case-sensitive on purpose; the CHECK stores lowercase
		"urgent":   TierNormal,
	}
	for in, want := range cases {
		if got := normalizeTier(in); got != want {
			t.Errorf("normalizeTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// sel builds a selection row; lastCrawl "" means never crawled.
func sel(name, tier, lastCrawl string) crawlGroupSel {
	r := crawlGroupSel{Name: name, TierRaw: tier}
	if lastCrawl != "" {
		ts, err := time.Parse(time.RFC3339, lastCrawl)
		if err != nil {
			panic(err)
		}
		r.LastCrawl = sql.NullTime{Time: ts, Valid: true}
	}
	return r
}

func names(rows []crawlGroupSel) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func eqNames(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The rule that motivated the whole change: a critical group takes the next
// slot even when it was crawled most recently of anyone. Stalest-first must
// only ever reorder WITHIN a tier.
func TestOrderCrawlGroups_CriticalBeatsStaleness(t *testing.T) {
	rows := []crawlGroupSel{
		sel("never-crawled-normal", "normal", ""),
		sel("ancient-low", "low", "2020-01-01T00:00:00Z"),
		sel("ancient-normal", "normal", "2020-01-02T00:00:00Z"),
		sel("just-crawled-critical", "critical", "2026-07-27T06:00:00Z"),
	}
	got := names(orderCrawlGroups(rows, 0))
	eqNames(t, got, []string{
		"just-crawled-critical", // freshest of all, still first
		"never-crawled-normal",  // never-crawled ahead of dated, within normal
		"ancient-normal",
		"ancient-low", // oldest overall, still last
	})
}

func TestOrderCrawlGroups_StalestFirstWithinTier(t *testing.T) {
	rows := []crawlGroupSel{
		sel("c-recent", "critical", "2026-07-27T06:00:00Z"),
		sel("c-old", "critical", "2026-07-01T00:00:00Z"),
		sel("c-never", "critical", ""),
	}
	eqNames(t, names(orderCrawlGroups(rows, 0)), []string{"c-never", "c-old", "c-recent"})
}

// The cap must fall on the low tail, never on a critical group.
func TestOrderCrawlGroups_CapSparesCritical(t *testing.T) {
	rows := []crawlGroupSel{
		sel("low-1", "low", "2020-01-01T00:00:00Z"),
		sel("low-2", "low", "2020-01-02T00:00:00Z"),
		sel("normal-1", "normal", "2021-01-01T00:00:00Z"),
		sel("crit", "critical", "2026-07-27T06:00:00Z"),
	}
	eqNames(t, names(orderCrawlGroups(rows, 2)), []string{"crit", "normal-1"})
}

// An unrecognised tier must be crawled as normal rather than dropped from the
// pass entirely — the failure mode this guards is a group silently never
// crawled again after a bad write.
func TestOrderCrawlGroups_UnknownTierIsCrawledAsNormal(t *testing.T) {
	rows := []crawlGroupSel{
		sel("bogus", "urgent", "2020-01-01T00:00:00Z"),
		sel("low-1", "low", "2019-01-01T00:00:00Z"),
		sel("crit", "critical", "2026-01-01T00:00:00Z"),
	}
	eqNames(t, names(orderCrawlGroups(rows, 0)), []string{"crit", "bogus", "low-1"})
}

func TestOrderCrawlGroups_EmptyAndNoCap(t *testing.T) {
	if got := orderCrawlGroups(nil, 5); len(got) != 0 {
		t.Errorf("nil rows: got %d, want 0", len(got))
	}
	rows := []crawlGroupSel{sel("a", "normal", ""), sel("b", "normal", "")}
	if got := orderCrawlGroups(rows, 0); len(got) != 2 {
		t.Errorf("limit 0 means no cap: got %d, want 2", len(got))
	}
	if got := orderCrawlGroups(rows, 99); len(got) != 2 {
		t.Errorf("limit above len: got %d, want 2", len(got))
	}
}
