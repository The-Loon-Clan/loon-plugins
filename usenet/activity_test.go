package usenet

import (
	"testing"
	"time"
)

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
