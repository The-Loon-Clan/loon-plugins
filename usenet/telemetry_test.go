package usenet

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestErrorRingKeepsNewest: the ring is a triage tail, so once it is full it
// must drop the OLDEST and still read newest-first. Getting the wrap wrong would
// show stale errors while hiding the one that just happened.
func TestErrorRingKeepsNewest(t *testing.T) {
	tel := newTelemetry()
	total := errorRingSize + 10
	for i := 0; i < total; i++ {
		tel.noteError("op", fmt.Errorf("err-%03d", i))
	}
	errs := tel.recentErrors()
	if len(errs) != errorRingSize {
		t.Fatalf("ring holds %d, want %d", len(errs), errorRingSize)
	}
	// Newest first.
	want := fmt.Sprintf("err-%03d", total-1)
	if errs[0].Msg != want {
		t.Errorf("first entry = %q, want the newest %q", errs[0].Msg, want)
	}
	// Oldest surviving entry.
	want = fmt.Sprintf("err-%03d", total-errorRingSize)
	if errs[len(errs)-1].Msg != want {
		t.Errorf("last entry = %q, want %q", errs[len(errs)-1].Msg, want)
	}
	// Strictly descending, no duplicates from a bad wrap.
	seen := map[string]bool{}
	for _, e := range errs {
		if seen[e.Msg] {
			t.Fatalf("duplicate entry %q — the ring wrap is wrong", e.Msg)
		}
		seen[e.Msg] = true
	}
}

func TestErrorRingBeforeFull(t *testing.T) {
	tel := newTelemetry()
	tel.noteError("a", errors.New("first"))
	tel.noteError("b", errors.New("second"))
	errs := tel.recentErrors()
	if len(errs) != 2 || errs[0].Msg != "second" || errs[1].Msg != "first" {
		t.Fatalf("partial ring = %+v, want newest-first [second first]", errs)
	}
	tel.noteError("c", nil) // nil must be ignored
	if e := tel.recentErrors(); len(e) != 2 {
		t.Errorf("a nil error was recorded")
	}
}

// TestPassAccounting: failed batches must count as attempts but contribute no
// articles, or a failing crawl would look productive.
func TestPassAccounting(t *testing.T) {
	tel := newTelemetry()
	tel.crawl.passStart(2)
	tel.crawl.noteGroups(5)
	tel.crawl.noteBatch(3000, 120, 1<<20, true)
	tel.crawl.noteBatch(3000, 80, 1<<20, true)
	tel.crawl.noteBatch(0, 0, 0, false)

	cur, _ := tel.crawl.snapshot()
	if !cur.InProgress {
		t.Error("pass should be in progress")
	}
	if cur.Batches != 3 || cur.Failed != 1 {
		t.Errorf("batches=%d failed=%d, want 3/1", cur.Batches, cur.Failed)
	}
	if cur.Articles != 6000 || cur.Staged != 200 {
		t.Errorf("articles=%d staged=%d, want 6000/200", cur.Articles, cur.Staged)
	}
	if cur.Groups != 5 {
		t.Errorf("groups=%d, want 5", cur.Groups)
	}

	tel.crawl.passEnd()
	cur, last := tel.crawl.snapshot()
	if cur.InProgress {
		t.Error("current pass should be cleared after passEnd")
	}
	if last.Articles != 6000 || last.InProgress {
		t.Errorf("last pass not retained correctly: %+v", last)
	}
}

// TestPassRates: rate and throughput are derived, so a zero duration must not
// produce a divide-by-zero or an absurd number.
func TestPassRates(t *testing.T) {
	p := passStats{}
	if p.Rate() != 0 || p.Throughput() != 0 || p.Duration() != 0 {
		t.Error("zero-value pass should report zeros, not NaN/Inf")
	}
	p = passStats{
		Started: time.Now().Add(-2 * time.Second), Finished: time.Now(),
		Articles: 6000, WireBytes: 2 * 1024 * 1024,
	}
	if r := p.Rate(); r < 2000 || r > 4000 {
		t.Errorf("rate = %.0f art/s over ~2s of 6000 articles, want ~3000", r)
	}
	if th := p.Throughput(); th < 0.5 || th > 2 {
		t.Errorf("throughput = %.2f MB/s over ~2s of 2 MiB, want ~1", th)
	}
}

// TestTelemetryConcurrent: batches are recorded from every pool worker at once.
func TestTelemetryConcurrent(t *testing.T) {
	tel := newTelemetry()
	tel.crawl.passStart(1)
	var wg sync.WaitGroup
	const workers, each = 8, 50
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				tel.crawl.noteBatch(10, 5, 100, true)
				tel.noteError("op", errors.New("boom"))
			}
		}()
	}
	wg.Wait()
	cur, _ := tel.crawl.snapshot()
	errs := tel.recentErrors()
	if cur.Batches != workers*each {
		t.Errorf("batches = %d, want %d — a concurrent update was lost", cur.Batches, workers*each)
	}
	if cur.Articles != workers*each*10 {
		t.Errorf("articles = %d, want %d", cur.Articles, workers*each*10)
	}
	if len(errs) != errorRingSize {
		t.Errorf("ring = %d entries, want it capped at %d", len(errs), errorRingSize)
	}
}

func TestFmtBytes(t *testing.T) {
	cases := map[int64]string{
		512: "512 B", 2048: "2.0 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB",
	}
	for in, want := range cases {
		if got := fmtBytes(in); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestNZBRetentionDefaultsToKeepForever guards against a regression that would
// silently destroy a catalogue: retention_days is CRAWL DEPTH (default 3), and
// it once also drove DELETE FROM nzbs. Any install left running lost everything
// older than three days. NZB deletion must stay opt-in.
func TestNZBRetentionDefaultsToKeepForever(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.NZBRetentionDays != 0 {
		t.Fatalf("nzb_retention_days defaults to %d; it MUST default to 0 (keep forever) — "+
			"any positive default deletes releases on the nightly prune",
			c.NZBRetentionDays)
	}
	if c.RetentionDays <= 0 {
		t.Errorf("crawl depth = %d, want a positive default", c.RetentionDays)
	}
	// A negative value must clamp to "keep", never to "delete everything".
	c2 := Config{NZBRetentionDays: -5}
	c2.applyDefaults()
	if c2.NZBRetentionDays != 0 {
		t.Errorf("negative retention became %d, want 0", c2.NZBRetentionDays)
	}
	// The two knobs must be independent.
	c3 := Config{RetentionDays: 30}
	c3.applyDefaults()
	if c3.NZBRetentionDays != 0 {
		t.Error("setting crawl depth changed the deletion horizon — they must stay separate")
	}
}
