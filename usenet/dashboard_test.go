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
		"jobs", "ready_groups", "evicted", "pending_count",
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

// TestPickPassPrefersRunning: a poll during a pass must describe THAT pass;
// a poll between passes must still describe the last one rather than zeros.
func TestPickPassPrefersRunning(t *testing.T) {
	pick := func(tr *passTracker) passStats { return pickPass(tr.snapshot()) }

	var tr passTracker
	if got := pick(&tr); got.Articles != 0 {
		t.Errorf("fresh tracker reported %+v", got)
	}

	tr.passStart(1)
	tr.noteBatch(100, 90, 1000, true)
	tr.passEnd()
	if got := pick(&tr); got.Articles != 100 || got.InProgress {
		t.Errorf("between passes = %+v, want the last completed pass", got)
	}

	tr.passStart(1)
	tr.noteBatch(7, 7, 70, true)
	got := pick(&tr)
	if !got.InProgress || got.Articles != 7 {
		t.Errorf("during a pass = %+v, want the running one", got)
	}
}

// TestWorkerTelemetryRoundTrip pins the publish format: what the worker
// marshals into the settings table must come back identical on the web side —
// the whole cross-process telemetry contract is this round trip.
func TestWorkerTelemetryRoundTrip(t *testing.T) {
	var tr passTracker
	tr.passStart(2)
	tr.noteBatch(500, 450, 9000, true)
	tr.passEnd()
	cur, last := tr.snapshot()

	in := workerTelemetry{
		UpdatedAt: time.Now().Truncate(time.Second),
		CrawlCur:  cur, CrawlLast: last,
		BackfillRate: 123.5,
		Errors:       []crawlError{{At: time.Now().Truncate(time.Second), Op: "usenet/crawl", Msg: "boom"}},
		Fleet:        map[int]providerStat{7: {Open: 18, Target: 20, Busy: 3, Resets: 2, Down: true}},
		Jobs: []crawlerJobVM{{Name: "Usenet Crawler", Status: "running",
			Activity: "batch 12/40", Next: "14:30:00", Running: true}},
		Pending: []pendingSet{{Base: "Some.Release", Group: "a.b.anime", Have: 40, Need: 100}},
		Evicted: 17,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out workerTelemetry
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.CrawlLast.Articles != 500 || out.CrawlLast.Staged != 450 || out.CrawlLast.WireBytes != 9000 {
		t.Errorf("pass counters lost in transit: %+v", out.CrawlLast)
	}
	if out.BackfillRate != 123.5 {
		t.Errorf("rate lost: %v", out.BackfillRate)
	}
	if len(out.Errors) != 1 || out.Errors[0].Msg != "boom" {
		t.Errorf("errors lost: %+v", out.Errors)
	}
	if pickPass(out.CrawlCur, out.CrawlLast).Articles != 500 {
		t.Error("published pass not selectable via pickPass")
	}
	fs, ok := out.Fleet[7]
	if !ok || fs.Open != 18 || fs.Target != 20 || fs.Busy != 3 || fs.Resets != 2 || !fs.Down {
		t.Errorf("fleet dial stats lost in transit: %+v", out.Fleet)
	}
	if len(out.Jobs) != 1 || out.Jobs[0].Name != "Usenet Crawler" ||
		out.Jobs[0].Next != "14:30:00" || !out.Jobs[0].Running {
		t.Errorf("job snapshots lost in transit: %+v", out.Jobs)
	}
	if len(out.Pending) != 1 || out.Pending[0].Missing() != 60 {
		t.Errorf("pending sample lost in transit: %+v", out.Pending)
	}
	if out.Evicted != 17 {
		t.Errorf("evicted counter lost in transit: %d", out.Evicted)
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
