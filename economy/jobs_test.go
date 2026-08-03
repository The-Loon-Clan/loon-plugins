package economy

import "testing"

// The grab bonus credits a DELTA against a high-water mark read back from the
// ledger. Every way this goes wrong is a subtraction in the wrong direction,
// and the failure is paying a member for grabs they were already paid for.
func TestGrabAwardOnlyPaysWhatIsNew(t *testing.T) {
	for _, c := range []struct {
		name                     string
		total, credited, perGrab int
		wantNew, wantPts         int
	}{
		{"first ever award", 100, 0, 2, 100, 200},
		{"only the delta", 150, 100, 2, 50, 100},
		{"nothing new since last run", 100, 100, 2, 0, 0},
		// The mark ahead of the total: a purge removed grabs, or someone
		// edited the ledger. Awarding a negative would DEBIT a member for
		// having fewer grabs, which is worse than the bug that caused it.
		{"mark ahead of total", 90, 100, 2, 0, 0},
		{"rate of zero disables the rule", 500, 0, 0, 0, 0},
		{"negative rate is not a debit", 500, 0, -5, 0, 0},
		{"no grabs at all", 0, 0, 2, 0, 0},
	} {
		gotNew, gotPts := grabAward(c.total, c.credited, c.perGrab)
		if gotNew != c.wantNew || gotPts != c.wantPts {
			t.Errorf("%s: grabAward(%d,%d,%d) = %d new/%d pts, want %d/%d",
				c.name, c.total, c.credited, c.perGrab, gotNew, gotPts, c.wantNew, c.wantPts)
		}
		if gotPts < 0 || gotNew < 0 {
			t.Errorf("%s: awarded a negative, which would debit the member", c.name)
		}
	}
}

// The ledger reason codes are load-bearing, not cosmetic: GrabsAlreadyCredited
// filters the ledger on earn_grabs to find the high-water mark. Rename it and
// every past award becomes invisible to the rule that wrote it — which would
// then pay every uploader for their entire history, again.
func TestLedgerReasonCodesAreStable(t *testing.T) {
	if reasonGrabs != "earn_grabs" {
		t.Errorf("reasonGrabs = %q; changing it re-pays every uploader's whole history", reasonGrabs)
	}
}
