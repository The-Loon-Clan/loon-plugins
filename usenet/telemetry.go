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
	Started  time.Time
	Finished time.Time

	// Round is which catch-up round is running, 1-based. A pass keeps going
	// while the servers hold a backlog, so one "pass" is routinely dozens of
	// rounds over many hours.
	//
	// The four progress counters below (Groups, GroupsDone, Batches,
	// BatchesTotal) are scoped to the CURRENT ROUND, not the pass. They used to
	// accumulate across rounds, which made the progress bar meaningless: prod
	// showed 542,460 / 520,000 batches, where the denominator was just
	// 26 rounds x the per-round budget and grew for as long as the pass ran.
	// A denominator that only ever increases is not progress toward anything.
	Round  int
	Groups int
	// GroupsDone counts groups whose LAST planned batch has completed — the
	// legacy page's "Group N / M" readout. Batches from every group interleave
	// on the flat queue, so this advances slower than the batch counter.
	GroupsDone int
	Batches    int
	// BatchesTotal is the planned batch count, known up front from the plan
	// enumeration — Batches/BatchesTotal is the pass progress bar.
	BatchesTotal int
	// PassBatches/PassBatchesTotal are the same counters accumulated across
	// the WHOLE pass, never reset by roundStart. They exist because every
	// catch-up pass ENDS on a round that found no work — that is the loop's
	// exit condition — so passEnd always snapshotted the zeroed terminal
	// round and "Last pass" read "0 batches" beside millions of articles: the
	// operator-reported mystery. The round pair keeps feeding the live
	// progress bar, where a round-scoped denominator is the meaningful one;
	// these feed the completed-pass readout, where only the total is.
	PassBatches      int
	PassBatchesTotal int
	// These four are PASS-cumulative on purpose: "this pass has fetched 1.6B
	// articles" is the number an operator wants, and the stats widget derives
	// its rate from deltas between polls, which needs a monotonic counter.
	Failed    int
	Articles  int // overview lines fetched
	Staged    int // newly staged after junk filtering and dedup
	WireBytes int64
	Providers int
	// Reading is the group a pool worker most recently started fetching — the
	// legacy dashboard's "what is it reading right now" label.
	Reading    string
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
	// groupsLeft is the current pass's outstanding batch count per group,
	// seeded by notePlanned; a group is "done" when it hits zero.
	groupsLeft map[string]int
	// groupsSeen / groupsDone are SETS, and that is the point. Both counters
	// used to be incremented, so a group re-claimed or re-planned in a later
	// round of a long-running pass counted again: a 28-newsgroup site showed
	// "433 / 980 groups" on the public stats widget after a 12-hour pass, a
	// fraction that could not mean anything to a viewer. Sets make the numbers
	// what the label already claims — distinct newsgroups being scanned.
	groupsSeen map[string]struct{}
	groupsDone map[string]struct{}
}

func (t *passTracker) passStart(providers int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Round 0 until the first roundStart, so a pass that dies before its first
	// round reads as "no round completed" rather than "round 1 did nothing".
	t.cur = passStats{Started: time.Now(), Providers: providers, InProgress: true}
	t.groupsLeft = nil
	t.groupsSeen = nil
	t.groupsDone = nil
}

// roundStart opens a catch-up round: the progress counters reset so the bar
// measures THIS round, while the pass totals (articles, staged, wire, failed)
// and the pass start time carry through.
func (t *passTracker) roundStart() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.Round++
	t.cur.Groups, t.cur.GroupsDone = 0, 0
	t.cur.Batches, t.cur.BatchesTotal = 0, 0
	t.cur.Reading = ""
	// The per-group bookkeeping is round-scoped too. Carrying groupsDone over
	// meant a group finished in round 1 stayed "done" for the rest of the pass
	// even while later rounds re-planned and re-fetched it.
	t.groupsLeft = nil
	t.groupsSeen = nil
	t.groupsDone = nil
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
	t.noteBatchFor("", articles, staged, wire, ok)
}

// noteBatchFor is noteBatch plus per-group completion accounting: when a
// group's last planned batch lands (success OR failure — either way it is no
// longer outstanding), GroupsDone advances.
func (t *passTracker) noteBatchFor(group string, articles, staged int, wire int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.Batches++
	t.cur.PassBatches++
	if n, tracked := t.groupsLeft[group]; tracked {
		if n <= 1 {
			delete(t.groupsLeft, group)
			if t.groupsDone == nil {
				t.groupsDone = map[string]struct{}{}
			}
			t.groupsDone[group] = struct{}{}
			t.cur.GroupsDone = len(t.groupsDone)
		} else {
			t.groupsLeft[group] = n - 1
		}
	}
	if !ok {
		t.cur.Failed++
		return
	}
	t.cur.Articles += articles
	t.cur.Staged += staged
	t.cur.WireBytes += wire
}

// notePlanned seeds the pass's planned work: the progress denominator and the
// per-group outstanding counts.
func (t *passTracker) notePlanned(group string, batches int) {
	if batches <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur.BatchesTotal += batches
	t.cur.PassBatchesTotal += batches
	if t.groupsLeft == nil {
		t.groupsLeft = map[string]int{}
	}
	t.groupsLeft[group] += batches
	// A group with new planned work is no longer done. Without this a group
	// re-planned after finishing would stay counted as done AND be re-counted
	// when it finished again, which is half of how GroupsDone drifted past the
	// group total.
	if _, done := t.groupsDone[group]; done {
		delete(t.groupsDone, group)
		t.cur.GroupsDone = len(t.groupsDone)
	}
}

// noteReading marks the group a pool worker just started fetching.
func (t *passTracker) noteReading(group string) {
	t.mu.Lock()
	t.cur.Reading = group
	t.mu.Unlock()
}

// noteGroups records which groups this pass is working. Takes names rather
// than a count so re-claiming a group in a later planning round cannot inflate
// the total — see groupsSeen.
func (t *passTracker) noteGroups(names []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.groupsSeen == nil {
		t.groupsSeen = make(map[string]struct{}, len(names))
	}
	for _, n := range names {
		t.groupsSeen[n] = struct{}{}
	}
	t.cur.Groups = len(t.groupsSeen)
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
	// pending is the latest incomplete-sets sample (see pendingSet);
	// pendingSeen marks that a sample has been taken at all, so an empty
	// pipeline is distinguishable from an unmeasured one.
	pending     []pendingSet
	pendingSeen bool
	// evicted counts hopeless sets shed by redis staging since the worker
	// started — proof the eviction machinery is working, not failing.
	evicted int64
	// demoted counts ready-queue entries the builder withdrew because its
	// verification refused what staging queued as complete. Each one is a
	// completeness-check disagreement; a steady rate is merged re-posts doing
	// what merged re-posts do, a climbing rate means the checks drifted.
	demoted int64
	// walkPast counts sets evicted by the walk-past sweep — incomplete with
	// their whole article span already fetched, so they could never complete.
	// Distinct from `evicted` (hopeless: stalled and far short) because the
	// two shed different populations for different reasons.
	walkPast int64
	// salvaged counts walk-past-dead sets assembled anyway (broken-but-
	// repairable, or par2-only gaps) instead of destroyed.
	salvaged int64
	// stalledPasses counts consecutive crawl passes that ended in the
	// "catch-up stalled" break while a large backlog remained. The 2026-07-24
	// incident (20/20 groups planning zero batches, pass after pass, 575M
	// articles behind) looked exactly like this and reached nothing but the
	// scrolling job log — a human reading it was the detector. The crawl
	// maintains this; the third consecutive stall also raises a report.
	stalledPasses int
	// prov attributes fetch volume per provider id (see provTally).
	prov map[int]*provTally
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

	// Why this set is short, not just that it is. "have 1,000 / need 11,314"
	// says a set is stalled and nothing about which of the two numbers is
	// wrong — and for a multi-file release Need is DERIVED (the sum of each
	// known file's declared segment count plus one article for every file not
	// seen yet), so a single file declaring a bogus total inflates it and the
	// set can never complete. Diagnosing that from have/need alone took several
	// deploy cycles of guessing; these three fields are already in the meta hash
	// the caller fetched, so publishing them is free and turns the question into
	// a readout.
	// ArtLo/ArtHi are the lowest and highest server article numbers seen in
	// this set. Article numbers ascend with posting time, so their span is how
	// far apart the set's articles sit on the server. One release is uploaded
	// in a single run, so its articles are near-contiguous — a set spanning
	// millions of article numbers is not one release but several unrelated
	// posts that collided on the same base subject, and it can never complete
	// because it is waiting on files belonging to somebody else's upload.
	ArtLo int `json:"art_lo,omitempty"`
	ArtHi int `json:"art_hi,omitempty"`

	Files   int    `json:"files"`    // total_files as the posts declare it
	Seen    int    `json:"seen"`     // files with at least one article staged
	PerFile string `json:"per_file"` // "1:1000 2:14 3:14" — declared total per file
}

// Missing is the article shortfall — what the dashboard's "Forming releases"
// card prints per row.
func (p pendingSet) Missing() int {
	if p.Need <= p.Have {
		return 0
	}
	return p.Need - p.Have
}

// provTally is one provider's fetch volume since worker start. Pool state
// (open/resets) never answered "which account is slow": during a pass every
// pool's connections read busy regardless, so an account serving overviews at
// a third the speed halved a backbone's effective rate for days with nothing
// attributing the drop. Cumulative-since-start like the evicted counter —
// readers take deltas.
type provTally struct {
	Articles  int   `json:"articles"`
	Staged    int   `json:"staged"`
	WireBytes int64 `json:"wire_bytes"`
	Failed    int   `json:"failed"`
}

// noteProviderBatch attributes one fetched batch to its provider.
func (t *telemetry) noteProviderBatch(id int, articles, staged int, wire int64, ok bool) {
	if id <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.prov == nil {
		t.prov = map[int]*provTally{}
	}
	pt := t.prov[id]
	if pt == nil {
		pt = &provTally{}
		t.prov[id] = pt
	}
	if !ok {
		pt.Failed++
		return
	}
	pt.Articles += articles
	pt.Staged += staged
	pt.WireBytes += wire
}

func (t *telemetry) providerTallies() map[int]provTally {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[int]provTally, len(t.prov))
	for id, pt := range t.prov {
		out[id] = *pt
	}
	return out
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

// noteDemoted counts ready-queue withdrawals (builder-refused candidates).
// Cumulative-since-start like the evicted counter, and for the same reason:
// the interesting reading is the delta between snapshots.
func (t *telemetry) noteDemoted(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.demoted += int64(n)
}

func (t *telemetry) demotedCount() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.demoted
}

// noteWalkPast counts walk-past evictions. Cumulative-since-start like its
// siblings; readers take deltas.
func (t *telemetry) noteWalkPast(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.walkPast += int64(n)
}

func (t *telemetry) walkPastCount() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.walkPast
}

// noteSalvaged counts salvage builds. Cumulative-since-start like its siblings.
func (t *telemetry) noteSalvaged(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.salvaged += int64(n)
}

func (t *telemetry) salvagedCount() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.salvaged
}

// setStalledPasses records the crawl's consecutive-stall streak for telemetry.
func (t *telemetry) setStalledPasses(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stalledPasses = n
}

func (t *telemetry) stalled() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stalledPasses
}

// setPending replaces the incomplete-sets sample wholesale (one build pass =
// one fresh sample).
func (t *telemetry) setPending(sets []pendingSet) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending, t.pendingSeen = sets, true
}

// pendingCount is the last sample's size, or -1 when no sample has been taken
// yet — the census must distinguish "none forming" from "never measured".
func (t *telemetry) pendingCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.pendingSeen {
		return -1
	}
	return len(t.pending)
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
