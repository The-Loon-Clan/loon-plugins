package usenet

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Crawl telemetry: what the last pass actually did, and what recently went
// wrong.
//
// Until now the only visibility was the job log, which scrolls and is per-job.
// When a crawl "looks wrong" the questions are always the same — is it moving,
// how fast, and what is failing — and none of them were answerable without
// reading logs on the worker host. That is especially poor once the crawler runs
// on a machine the operator is not logged into.
//
// Errors are ALSO reported to the host error log; the ring is a short, local
// tail for the admin page, not a replacement for it.

// errorRingSize is deliberately small: this is a "what just went wrong" tail,
// and an unbounded buffer on a per-batch error path is a slow memory leak.
const errorRingSize = 50

type crawlError struct {
	At  time.Time
	Op  string
	Msg string
}

// passStats is one completed crawl pass.
type passStats struct {
	Started    time.Time
	Finished   time.Time
	Groups     int
	Batches    int
	Failed     int
	Articles   int // overview lines fetched
	Staged     int // newly staged after junk filtering and dedup
	WireBytes  int64
	Providers  int
	InProgress bool
}

// Rate returns articles per second over the pass; 0 when it has no duration yet.
func (p passStats) Rate() float64 {
	d := p.Duration()
	if d <= 0 {
		return 0
	}
	return float64(p.Articles) / d.Seconds()
}

// Throughput returns MB/s of overview data pulled off the wire.
func (p passStats) Throughput() float64 {
	d := p.Duration()
	if d <= 0 {
		return 0
	}
	return float64(p.WireBytes) / (1024 * 1024) / d.Seconds()
}

func (p passStats) Duration() time.Duration {
	if p.Started.IsZero() {
		return 0
	}
	end := p.Finished
	if p.InProgress || end.IsZero() {
		end = time.Now()
	}
	return end.Sub(p.Started)
}

// telemetry holds the live counters and the error tail. Every field is guarded:
// batches are recorded from the pool workers concurrently.
type telemetry struct {
	mu      sync.Mutex
	cur     passStats
	last    passStats
	errs    []crawlError
	errNext int
}

func newTelemetry() *telemetry { return &telemetry{errs: make([]crawlError, 0, errorRingSize)} }

func (t *telemetry) passStart(providers int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = passStats{Started: time.Now(), Providers: providers, InProgress: true}
}

func (t *telemetry) passEnd() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur.Started.IsZero() {
		return
	}
	t.cur.Finished = time.Now()
	t.cur.InProgress = false
	t.last = t.cur
	t.cur = passStats{}
}

// noteBatch records one fetched range. Called from every pool worker.
func (t *telemetry) noteBatch(articles int, staged int, wire int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.Batches++
	if !ok {
		t.cur.Failed++
		return
	}
	t.cur.Articles += articles
	t.cur.Staged += staged
	t.cur.WireBytes += wire
}

func (t *telemetry) noteGroups(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.Groups += n
}

// noteError appends to the ring, overwriting the oldest once full.
func (t *telemetry) noteError(op string, err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := crawlError{At: time.Now(), Op: op, Msg: err.Error()}
	if len(t.errs) < errorRingSize {
		t.errs = append(t.errs, e)
		return
	}
	t.errs[t.errNext] = e
	t.errNext = (t.errNext + 1) % errorRingSize
}

// snapshot returns the current pass (if one is running), the last completed
// pass, and the recent errors NEWEST FIRST.
func (t *telemetry) snapshot() (cur, last passStats, errs []crawlError) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]crawlError, 0, len(t.errs))
	// Walk the ring backwards from the most recent write.
	for i := 0; i < len(t.errs); i++ {
		idx := (t.errNext - 1 - i + len(t.errs)*2) % len(t.errs)
		out = append(out, t.errs[idx])
	}
	return t.cur, t.last, out
}

// reportErr sends an error to the host error log AND the local ring, so the
// crawlers page can show a tail without the operator having to open the error
// log on another page (or ssh to the worker).
func (p *Plugin) reportErr(ctx context.Context, op string, err error) {
	if err == nil {
		return
	}
	if p.tel != nil {
		p.tel.noteError(op, err)
	}
	p.core.Errors.Report(ctx, op, err)
}

// fmtBytes renders a byte count for the admin page.
func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
