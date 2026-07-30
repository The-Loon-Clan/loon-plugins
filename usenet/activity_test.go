package usenet

import (
	"testing"
	"time"
)

// The idle fallback picks whichever pass FINISHED most recently. Hardwired to
// the last crawl, a continuously-backfilling install (5-minute intervals, for
// months) made the public widget sawtooth: big backfill counters while
// running, a snap down to the last forward crawl's small numbers at every
// pass boundary, then back up — which reads as the crawler losing work.
func TestActivityIdleFallbackPrefersTheNewestPass(t *testing.T) {
	now := time.Now()
	tv := workerTelemetry{
		CrawlLast:    passStats{Started: now.Add(-2 * time.Hour), Finished: now.Add(-90 * time.Minute), Articles: 12},
		BackfillLast: passStats{Started: now.Add(-10 * time.Minute), Finished: now.Add(-5 * time.Minute), Articles: 900000},
	}
	got := activityFrom(tv)
	if !got.Backfill || got.Articles != 900000 {
		t.Errorf("idle readout = backfill:%v articles:%d, want the backfill pass that "+
			"finished 5 minutes ago, not the crawl from 90 minutes ago", got.Backfill, got.Articles)
	}

	// A running crawl still outranks everything.
	tv.CrawlCur = passStats{InProgress: true, Articles: 7}
	if got := activityFrom(tv); got.Backfill || got.Articles != 7 {
		t.Error("a running crawl lost the readout to a finished pass")
	}

	// And when the crawl is genuinely the newest, it keeps the slot.
	tv.CrawlCur = passStats{}
	tv.CrawlLast.Finished = now
	if got := activityFrom(tv); got.Backfill {
		t.Error("the crawl finished most recently but the backfill was shown")
	}
}

// Provider tallies attribute fetch volume per account. Pool state cannot: a
// throttled account's connections read just as busy as a healthy one's, so a
// backbone running at half speed had no readout naming which account dragged.
func TestProviderTalliesAttributeVolume(t *testing.T) {
	tel := newTelemetry()
	tel.noteProviderBatch(1, 3000, 900, 1<<20, true)
	tel.noteProviderBatch(1, 3000, 800, 1<<20, true)
	tel.noteProviderBatch(2, 0, 0, 0, false)
	tel.noteProviderBatch(0, 5000, 5000, 1<<30, true) // no provider id: dropped, not misfiled

	got := tel.providerTallies()
	if got[1].Articles != 6000 || got[1].Staged != 1700 || got[1].WireBytes != 2<<20 {
		t.Errorf("provider 1 = %+v, want 6000 articles / 1700 staged / 2MiB", got[1])
	}
	if got[2].Failed != 1 || got[2].Articles != 0 {
		t.Errorf("provider 2 = %+v, want one failed batch and nothing else", got[2])
	}
	if _, ok := got[0]; ok {
		t.Error("an id-less batch was filed under provider 0")
	}
}

func TestActivityFromPicksRunningCrawl(t *testing.T) {
	tv := workerTelemetry{
		UpdatedAt: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		CrawlCur:  passStats{InProgress: true, Articles: 100, Staged: 40, Groups: 5},
		CrawlLast: passStats{Articles: 999},
	}
	a := activityFrom(tv)
	if !a.InProgress || a.Backfill || a.Articles != 100 || a.Staged != 40 {
		t.Fatalf("want running crawl 100/40, got %+v", a)
	}
	if !a.UpdatedAt.Equal(tv.UpdatedAt) {
		t.Fatalf("UpdatedAt not carried: %+v", a)
	}
}

func TestActivityFromPrefersBackfillOverLastCrawl(t *testing.T) {
	tv := workerTelemetry{
		BackfillCur: passStats{InProgress: true, Articles: 7},
		CrawlLast:   passStats{Articles: 999},
	}
	a := activityFrom(tv)
	if !a.InProgress || !a.Backfill || a.Articles != 7 {
		t.Fatalf("want running backfill, got %+v", a)
	}
}

func TestActivityFromIdleFallsBackToLastCrawl(t *testing.T) {
	tv := workerTelemetry{
		CrawlLast: passStats{Articles: 999, Staged: 500, Finished: time.Now()},
	}
	a := activityFrom(tv)
	if a.InProgress || a.Backfill || a.Articles != 999 || a.Staged != 500 {
		t.Fatalf("want idle last-crawl 999/500, got %+v", a)
	}
}
