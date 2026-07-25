package usenet

import (
	"database/sql"
	"testing"
	"time"
)

// TestOrderCrawlGroups locks in the selection order the operator reported bugs
// against: low-priority must never be crawled before a normal group, and under
// a cap the slots must rotate stalest-first so no group starves forever.
func TestOrderCrawlGroups(t *testing.T) {
	base := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	nt := func(d time.Duration) sql.NullTime { return sql.NullTime{Time: base.Add(d), Valid: true} }
	never := sql.NullTime{}
	mk := func(name string, low bool, last sql.NullTime) crawlGroupSel {
		return crawlGroupSel{Name: name, LowPri: low, LastCrawl: last}
	}
	names := func(rows []crawlGroupSel) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.Name
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	// Input in SQL (low_priority, sort_order, name) order: normal tier first,
	// then low, with mixed last_crawl within each.
	rows := []crawlGroupSel{
		mk("n_fresh", false, nt(-1*time.Minute)),
		mk("n_stale", false, nt(-30*time.Minute)),
		mk("n_never", false, never),
		mk("l_fresh", true, nt(-2*time.Minute)),
		mk("l_stale", true, nt(-40*time.Minute)),
	}

	t.Run("no cap: normal before low, stalest (never) first within each tier", func(t *testing.T) {
		got := names(orderCrawlGroups(rows, 0))
		want := []string{"n_never", "n_stale", "n_fresh", "l_stale", "l_fresh"}
		if !eq(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("negative limit is also no cap", func(t *testing.T) {
		if len(orderCrawlGroups(rows, -1)) != len(rows) {
			t.Error("negative limit must not cap")
		}
	})

	t.Run("cap falls on the low-pri tail first (low never preempts normal)", func(t *testing.T) {
		got := names(orderCrawlGroups(rows, 3))
		want := []string{"n_never", "n_stale", "n_fresh"} // all normal; low starved under a tight cap
		if !eq(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("cap adds the stalest low group after every normal group", func(t *testing.T) {
		got := names(orderCrawlGroups(rows, 4))
		want := []string{"n_never", "n_stale", "n_fresh", "l_stale"}
		if !eq(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("equal last_crawl keeps the SQL (sort_order, name) input order", func(t *testing.T) {
		same := nt(-5 * time.Minute)
		in := []crawlGroupSel{mk("a", false, same), mk("b", false, same), mk("c", false, same)}
		got := names(orderCrawlGroups(in, 0))
		if !eq(got, []string{"a", "b", "c"}) {
			t.Errorf("stable tiebreak broken: got %v", got)
		}
	})
}
