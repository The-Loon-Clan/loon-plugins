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

// passTracker accumulates one job's passes. Every field is guarded: batches are
// recorded from the pool workers concurrently.
type passTracker struct {
	mu   sync.Mutex
	cur  passStats
	last passStats
}

func (t *passTracker) passStart(providers int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = passStats{Started: time.Now(), Providers: providers, InProgress: true}
}

func (t *passTracker) passEnd() {
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
func (t *passTracker) noteBatch(articles int, staged int, wire int64, ok bool) {
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

func (t *passTracker) noteGroups(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.Groups += n
}

// snapshot returns the in-progress pass (if any) and the last completed one.
func (t *passTracker) snapshot() (cur, last passStats) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur, t.last
}

// rate returns the best available articles/sec: the running pass once it has
// enough of a sample to mean anything, otherwise the last completed one. The
// floor exists because the first seconds of a pass are all connection setup, and
// dividing a few hundred articles by two seconds produces an ETA off by orders
// of magnitude.
func (t *passTracker) rate() float64 {
	cur, last := t.snapshot()
	if cur.InProgress && cur.Duration() > 20*time.Second && cur.Articles > 0 {
		return cur.Rate()
	}
	return last.Rate()
}

// telemetry holds each job's counters plus a shared error tail.
//
// Crawl and backfill are tracked SEPARATELY because their rates mean different
// things: the forward crawl is bursty and usually idle (it only fetches what
// arrived since last pass), while backfill runs flat out. Averaging them
// together would make the backfill ETA swing wildly with the crawl's duty cycle.
type telemetry struct {
	crawl    passTracker
	backfill passTracker

	mu      sync.Mutex
	errs    []crawlError
	errNext int
	// built is the last few releases THIS crawler assembled, newest kept.
	// It exists because no table answers "what did the crawler just build"
	// in every mode: with sink=host the release rows land in the host's
	// domain (mixed with agent uploads), and the plugin's own table stays
	// empty. In-memory and reset on restart — it is a liveness readout, not
	// history.
	built     []builtRelease
	builtNext int
	// pending is the latest incomplete-sets sample (see pendingSet).
	pending []pendingSet
	// evicted counts hopeless sets shed by redis staging since the worker
	// started — proof the eviction machinery is working, not failing.
	evicted int64
}

// builtRelease is one crawler-assembled release, exported for the telemetry
// publish round trip (telemetry_publish.go).
type builtRelease struct {
	Title string    `json:"title"`
	Group string    `json:"group"`
	Size  int64     `json:"size"`
	At    time.Time `json:"at"`
}

const builtRingSize = 10

func newTelemetry() *telemetry { return &telemetry{errs: make([]crawlError, 0, errorRingSize)} }

// noteBuilt appends to the built ring, overwriting the oldest once full.
func (t *telemetry) noteBuilt(title, group string, size int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := builtRelease{Title: title, Group: group, Size: size, At: time.Now()}
	if len(t.built) < builtRingSize {
		t.built = append(t.built, b)
		return
	}
	t.built[t.builtNext] = b
	t.builtNext = (t.builtNext + 1) % builtRingSize
}

// pendingSet is one staged-but-incomplete release — the "missing articles"
// readout. Sampled by the build pass (not per page render: listing redis sets
// means SCAN + a pipelined read per set) and published with the telemetry.
type pendingSet struct {
	Base     string `json:"base"`
	Group    string `json:"group"`
	Have     int    `json:"have"`
	Need     int    `json:"need"`
	Segments int    `json:"segments"`
	Multi    bool   `json:"multi"`
}

// Missing is the article shortfall — what the dashboard's "Forming releases"
// card prints per row.
func (p pendingSet) Missing() int {
	if p.Need <= p.Have {
		return 0
	}
	return p.Need - p.Have
}

// noteEvicted counts hopeless-set evictions (wired into redis staging as a
// callback — the staging layer has no telemetry reference of its own).
func (t *telemetry) noteEvicted(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evicted += int64(n)
}

func (t *telemetry) evictedCount() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.evicted
}

// setPending replaces the incomplete-sets sample wholesale (one build pass =
// one fresh sample).
func (t *telemetry) setPending(sets []pendingSet) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = sets
}

func (t *telemetry) pendingSets() []pendingSet {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending
}

// recentBuilt returns the built tail NEWEST FIRST.
func (t *telemetry) recentBuilt() []builtRelease {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]builtRelease, 0, len(t.built))
	for i := 0; i < len(t.built); i++ {
		idx := (t.builtNext - 1 - i + len(t.built)*2) % len(t.built)
		out = append(out, t.built[idx])
	}
	return out
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

// recentErrors returns the error tail NEWEST FIRST.
func (t *telemetry) recentErrors() []crawlError {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]crawlError, 0, len(t.errs))
	// Walk the ring backwards from the most recent write.
	for i := 0; i < len(t.errs); i++ {
		idx := (t.errNext - 1 - i + len(t.errs)*2) % len(t.errs)
		out = append(out, t.errs[idx])
	}
	return out
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

// backfillETA estimates how long the remaining backfill will take at the
// observed backfill rate. Returns ok=false when there is nothing left or no
// measured rate yet — showing "0s remaining" or a made-up number for a crawler
// that has not run is worse than showing nothing.
//
// This is deliberately a FLEET-WIDE estimate. Per-group ETAs would require
// predicting the order the scheduler visits groups in, which changes with
// priority, leases and how many workers are up; one honest aggregate beats a
// column of confident-looking per-group guesses.
func backfillETA(remaining int64, rate float64) (time.Duration, bool) {
	if remaining <= 0 || rate <= 0 {
		return 0, false
	}
	return time.Duration(float64(remaining) / rate * float64(time.Second)), true
}

// fmtETA renders a duration at a resolution that matches its size — nobody needs
// seconds on a two-week estimate, and the extra digits imply a precision this
// estimate does not have.
func fmtETA(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%.0f days", d.Hours()/24)
	case d >= 2*time.Hour:
		return fmt.Sprintf("%.0f hours", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0f min", d.Minutes())
	default:
		return "< 1 min"
	}
}
