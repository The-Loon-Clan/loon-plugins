package usenet

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStatusReportJSONContract pins the wire field names. This endpoint is meant
// to be polled by monitors and scripts, so a rename is a breaking change and
// should have to be made deliberately rather than by refactoring a struct.
func TestStatusReportJSONContract(t *testing.T) {
	b, err := json.Marshal(StatusReport{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"generated_at", "crawl", "backfill", "providers", "workers",
		"active_groups", "staged_articles", "pending_releases", "ready_releases",
		"total_nzbs", "backfill_remaining", "backfill_eta_seconds", "recent_errors",
	} {
		if _, ok := got[field]; !ok {
			t.Errorf("status JSON missing field %q", field)
		}
	}
}

// TestPassReportFromStats: the report must carry the derived rate, not just raw
// counters — a monitor watching "is it moving" reads that field.
func TestPassReportFromStats(t *testing.T) {
	start := time.Now().Add(-100 * time.Second)
	st := passStats{
		Started: start, Finished: start.Add(100 * time.Second),
		Groups: 3, Batches: 20, Failed: 2, Articles: 5000, Staged: 4000,
		WireBytes: 1 << 20,
	}
	r := passReport(st)
	if r.Running {
		t.Error("a finished pass reported as running")
	}
	if r.Articles != 5000 || r.Failed != 2 {
		t.Errorf("counters lost: %+v", r)
	}
	if r.ArticlesSec < 49 || r.ArticlesSec > 51 {
		t.Errorf("rate = %.2f, want ~50/s", r.ArticlesSec)
	}
	if r.DurationSec < 99 || r.DurationSec > 101 {
		t.Errorf("duration = %.2f, want ~100s", r.DurationSec)
	}
}

// TestCurrentOrLastPrefersRunning: a poll during a pass must describe THAT pass;
// a poll between passes must still describe the last one rather than zeros.
func TestCurrentOrLastPrefersRunning(t *testing.T) {
	var tr passTracker
	if got := currentOrLast(&tr); got.Articles != 0 {
		t.Errorf("fresh tracker reported %+v", got)
	}

	tr.passStart(1)
	tr.noteBatch(100, 90, 1000, true)
	tr.passEnd()
	if got := currentOrLast(&tr); got.Articles != 100 || got.InProgress {
		t.Errorf("between passes = %+v, want the last completed pass", got)
	}

	tr.passStart(1)
	tr.noteBatch(7, 7, 70, true)
	got := currentOrLast(&tr)
	if !got.InProgress || got.Articles != 7 {
		t.Errorf("during a pass = %+v, want the running one", got)
	}
}

// TestBackfillETASecondsZeroMeansUnknown documents the one field that could be
// misread: zero is "no measured rate", never "finished". A monitor that alerted
// on eta==0 would fire on every fresh install.
func TestBackfillETASecondsZeroMeansUnknown(t *testing.T) {
	var tr passTracker // no passes ever run
	if _, ok := backfillETA(5_000_000, tr.rate()); ok {
		t.Error("produced an ETA with no measured rate; the field must stay 0/unknown")
	}
}
