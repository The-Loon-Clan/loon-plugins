package usenet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/nntp"
	"github.com/the-loon-clan/loon/schedule"
)

// stagedArticle is one parsed overview line awaiting assembly.
type stagedArticle struct {
	// ArticleNum is the server's message number for this article. Article
	// numbers are per BACKBONE and ascend with posting time, so the span
	// between a set's lowest and highest is how far apart its articles sit on
	// the server. A real release is posted in one run, so its articles are
	// near-contiguous; a set spanning millions of article numbers is not one
	// release but several unrelated posts that collided on the same base
	// subject — and such a set can never complete, because it is waiting for
	// files that belong to somebody else's upload.
	ArticleNum  int
	MessageID   string
	Subject     string
	BaseSubject string
	Poster      string
	Bytes       int64
	Posted      time.Time
	Group       string
	PartNum     int
	TotalParts  int
	SegTotal    int
	FileNum     int
	TotalFiles  int
	FileParts   bool
}

// batchJob is one OVER range to fetch. Jobs from EVERY group go into a single
// flat queue rather than per-group waves: in steady state most groups have only
// a batch or two of new articles, so per-group waves would leave most of the
// connection pool idle. One queue keeps every connection busy no matter how the
// work is distributed.
type batchJob struct {
	group  string
	lo, hi int
	// Resolved per group, because retention and throttling are per-group
	// settings and a batch is the unit that actually applies them.
	cutoff   time.Time
	throttle time.Duration
}

type batchResult struct {
	group            string
	lo, hi           int
	minDate, maxDate time.Time
	staged           int
	articles         int   // overview lines returned
	wire             int64 // bytes pulled off the wire, for throughput
	ok               bool  // fetched AND staged; only then may the watermark pass this range
}

// crawlPlan is one group's resolved forward window for this pass.
type crawlPlan struct {
	group     string
	low, high int // server's current article-number bounds
	start     int // first article we intend to fetch
	hasWork   bool
}

// runCrawl fetches new overviews for every active group into staging, then
// chains the builder. Batches are fetched in parallel across the shared NNTP
// connection pool; a group's watermark only advances past batches that were both
// fetched and staged successfully.
func (p *Plugin) runCrawl(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.crawlMu.TryLock() {
		p.crawlJob.Log("crawl already running — skipping overlap")
		return
	}
	defer p.crawlMu.Unlock()
	p.crawlJob.SetRunning()
	cfg := p.effective(ctx)

	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.crawlJob.Log("no server configured — add one in the admin wizard")
			p.crawlJob.SetIdle(p.nextCrawl(ctx))
			return
		}
		p.crawlJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/crawl-fleet", err)
		return
	}

	// Each provider crawls independently with its OWN watermarks and coverage —
	// article numbers are per-server, so nothing numeric can be shared. What they
	// do share is the staging area, where message-id dedup turns the overlap into
	// better completeness: a release short a segment on one backbone can be
	// finished by another.
	p.tel.crawl.passStart(len(runs))
	defer p.tel.crawl.passEnd()
	defer p.flushPosterHits(ctx)
	totalStaged := 0
	// Catch-up loop: when the servers still hold a meaningful forward backlog
	// after a pass, go again immediately instead of sleeping out the interval —
	// "missing a lot of articles while sitting idle" is exactly wrong. Guarded
	// three ways: progress must be made each round (or a stalled provider would
	// spin), the off-peak gate is honored between rounds, and the operator can
	// disable it (crawl_no_catchup).
	prevBehind := int64(-1)
	blockedRetries := 0
	for {
		// Each iteration of the catch-up loop is a ROUND: it re-plans from the
		// current watermarks and runs a fresh batch budget, so the progress
		// counters restart with it.
		p.tel.crawl.roundStart()
		// Operator config is re-read every ROUND, not once per pass. A catch-up
		// pass runs for HOURS, so a pass-scoped read means an admin editing a
		// junk rule or adding a watched poster sees no effect until the pass
		// ends — indistinguishable from the feature being broken, and it cost a
		// diagnostic cycle when a poster added 90 seconds into a pass recorded
		// nothing. Same reasoning as re-resolving the fleet each round below.
		p.reloadJunkRules(ctx)
		p.loadPosterWatch(ctx)
		// Do not stage into a staging backend that is already full.
		//
		// The backfill has yielded to pressure since it was written; the forward
		// crawl never did, on the reasoning that new articles matter more than
		// history. That reasoning holds only while storing them WORKS. Under
		// redis with an allkeys-* policy at its ceiling, every write evicts
		// something else to make room — and the coldest keys are precisely the
		// forming sets waiting between crawl visits. Production evicted 97.4
		// MILLION keys that way, roughly 640 staged releases a minute, while the
		// crawler kept feeding it.
		//
		// Skipping the round leaves the watermark where it is, so nothing is
		// lost: those articles are re-read next pass. Storing them into a full
		// backend is what loses them.
		if pause, pr := p.pauseForStagingPressure(ctx, cfg); pause {
			p.crawlJob.Log("crawl paused: staging %.0f%% full (>= %d%%) — storing now would evict "+
				"sets that are still assembling; watermarks held so nothing is skipped",
				pr*100, cfg.crawlStopPct())
			p.crawlJob.SetIdle(p.nextCrawl(ctx))
			return
		}
		staged, claimed := 0, 0
		for _, bbRuns := range groupByBackbone(runs) {
			if ctx.Err() != nil {
				return
			}
			s, c := p.crawlBackbone(ctx, bbRuns, cfg)
			staged += s
			claimed += c
		}
		totalStaged += staged
		// Publish this round's attribution now. Held to pass end, a multi-hour
		// catch-up shows an operator an empty table while it is actively
		// accumulating the rows they are waiting for. drain() resets, so the
		// deferred pass-end flush cannot double-count what this already wrote.
		p.flushPosterHits(ctx)
		if cfg.CrawlNoCatchup || ctx.Err() != nil {
			break
		}
		behind, err := p.st.forwardBacklog(ctx, cfg.HoldLowUntilBackfilled)
		if err != nil || behind <= int64(cfg.Batch) {
			break // caught up (within one batch) — the interval takes over
		}
		if claimed == 0 {
			// Every group is lease-held by someone else. Right after a deploy
			// that someone is the KILLED predecessor, whose heartbeat hasn't
			// aged past the takeover window yet (the boot delay is shorter
			// than leaseOwnerDeadAfter, so the first pass loses that race by
			// design). Blocked is temporary — retry shortly instead of
			// sleeping out the interval, which idled every boot for 15
			// minutes. Bounded: with a genuinely live sibling holding the
			// groups, we stop retrying and let the interval take over.
			blockedRetries++
			if blockedRetries > 6 {
				p.crawlJob.Log("groups still lease-held by another worker after %d retries — waiting for the next interval", blockedRetries-1)
				break
			}
			p.crawlJob.Log("all groups lease-held by another worker — retrying in 45s (%s article(s) behind)", fmtComma(behind))
			if !schedule.SleepCtx(ctx, 45*time.Second) {
				return
			}
			continue
		}
		blockedRetries = 0
		if prevBehind >= 0 && behind >= prevBehind {
			p.crawlJob.Log("catch-up stalled at %s article(s) behind — waiting for the next interval", fmtComma(behind))
			break
		}
		prevBehind = behind
		if !schedule.OffPeakGate() {
			p.crawlJob.Log("catch-up paused: site is busy (off-peak gate); %s article(s) behind", fmtComma(behind))
			break
		}
		p.crawlJob.Log("catch-up: %s article(s) still behind — continuing without waiting for the interval", fmtComma(behind))
		// Re-resolve the fleet each round: catch-up passes run for HOURS, and
		// an operator adding or disabling a provider mid-pass should take
		// effect on the next round, not after the whole pass — "I added the
		// EU server and can't start it" was exactly this. A resolve failure
		// keeps the current fleet rather than aborting a working pass.
		if newRuns, err := p.activeFleet(ctx, cfg); err == nil && len(newRuns) > 0 {
			runs = newRuns
		}
	}
	p.crawlJob.Log("crawl complete across %d provider(s): %d article(s) staged", len(runs), totalStaged)
	p.crawlJob.SetIdle(p.nextCrawl(ctx))
	go p.runBuild(ctx)
	if totalStaged == 0 {
		go p.idleHealthCheck(ctx)
	}
}

// crawlBackbone runs ONE forward pass for one backbone, across every provider
// account on it. Returns articles staged and how many groups this worker
// actually CLAIMED — the catch-up loop treats an all-blocked pass
// (claimed == 0 fleet-wide) as retry-shortly, not stalled.
//
// Per BACKBONE rather than per provider, because article numbers and therefore
// watermarks are backbone-scoped: two accounts on the same backbone see the
// same articles. Running them as separate passes meant the first advanced the
// watermarks and the second found "nothing new" every time — prod had two
// 50-connection accounts on netnews and only ever crawled with one of them.
// pluginapi has said "the second is extra connections, not extra coverage"
// since the seam was written; this is the code finally agreeing.
func (p *Plugin) crawlBackbone(ctx context.Context, runs []providerRun, cfg Config) (int, int) {
	// planGroup only issues GROUP, so any account on the backbone can answer
	// it; the fetch work is what spreads across all of them.
	lead := runs[0]
	pool, bb := lead.pool, lead.prov.backboneKey()
	for _, r := range runs {
		r.pool.TopUp(ctx) // refill anything the last pass discarded
	}

	// Should the low tier sit this pass out? Asked per backbone, because
	// article numbers — and therefore backfill progress — are per backbone.
	holdLow := false
	if cfg.HoldLowUntilBackfilled {
		pend, err := p.st.criticalBackfillPending(ctx, bb)
		if err != nil {
			// Fail OPEN. Not knowing whether critical history is outstanding is
			// no reason to stop crawling a whole tier; the alternative silently
			// converts a transient query error into an outage for 14 groups.
			p.reportErr(ctx, "usenet/crawl-hold-check", err)
		} else if pend.Any() {
			holdLow = true
			p.crawlJob.Log("holding LOW-tier groups: %d critical group(s) still backfilling, %s article(s) of history left (stalest: %s)",
				pend.Groups, fmtComma(pend.Articles), pend.Stalest)
		}
	}

	groups, err := p.st.activeGroupsForBackbone(ctx, bb, cfg.MaxGroups, holdLow)
	if err != nil {
		p.reportErr(ctx, "usenet/crawl-groups", err)
		return 0, 0
	}
	if len(groups) == 0 {
		if holdLow {
			// Distinguish "held on purpose" from "misconfigured". Both leave the
			// pass with nothing to do, and only one of them is a problem.
			p.crawlJob.Log("nothing to crawl: every active group is LOW-tier and held until critical backfill completes")
		} else {
			p.crawlJob.Log("no active groups — pick some in the admin wizard")
		}
		return 0, 0
	}
	// Split first, then lease. Assignment decides what to ATTEMPT so N crawlers
	// divide the work instead of racing; the lease then guarantees no two
	// workers touch one group even while a membership change settles.
	groups = p.myGroups(ctx, groups, cfg)
	if len(groups) == 0 {
		return 0, 0
	}
	// From here the pass runs on the lease context: losing the lease mid-pass
	// cancels it, because a sibling may own these groups from that moment.
	// jobCtx keeps the job's own context so lease loss and shutdown are
	// distinguishable at the final sweep.
	jobCtx := ctx
	groups, ctx, release := p.claimGroupLeases(ctx, bb, groups, p.leaseTTL(cfg))
	defer release()
	claimedNames := make([]string, 0, len(groups))
	for _, g := range groups {
		claimedNames = append(claimedNames, g.Name)
	}
	p.tel.crawl.noteGroups(claimedNames)
	if len(groups) == 0 {
		p.crawlJob.Log("%s: every group already claimed by another worker", bb)
		return 0, 0
	}
	claimed := len(groups)

	// 1. Resolve each group's window and enqueue its batches, bounded by the
	// pass budget: a deep backlog runs as bounded rounds (the catch-up loop
	// rolls the remainder into the next round) instead of one hours-long
	// pass. Backfill has had this since day one (backfill_batches_per_run);
	// the forward crawl was unbounded — 192k batches were observed planned in
	// a single pass. Truncating mid-group is safe: the watermark advances
	// through the contiguous prefix of fetched batches, so the unplanned tail
	// is simply next round's window.
	plans := make(map[string]*crawlPlan, len(groups))
	var jobs []batchJob
	budgetHit := false
	for _, g := range groups {
		if ctx.Err() != nil {
			return 0, claimed
		}
		if len(jobs) >= cfg.CrawlMaxBatches {
			budgetHit = true
			break
		}
		plan, err := p.planGroup(ctx, pool, g, cfg)
		if err != nil {
			p.reportErr(ctx, "usenet/crawl-plan",
				fmt.Errorf("%s/%s: %w", lead.prov.label(), g.Name, err))
			p.crawlJob.Log("%s/%s: %v", lead.prov.label(), g.Name, err)
			continue
		}
		plans[g.Name] = plan
		if !plan.hasWork {
			// Nothing new, but still record the server range so the coverage view
			// stays honest.
			if err := p.st.updateGroupStateForBackbone(ctx, bb, plan.group, int64(plan.low), int64(plan.high),
				0, int64(plan.start), time.Time{}); err != nil {
				p.reportErr(ctx, "usenet/crawl-range", err)
			}
			continue
		}
		before := len(jobs)
		for i := plan.start; i <= plan.high; i += cfg.Batch {
			if len(jobs) >= cfg.CrawlMaxBatches {
				budgetHit = true
				break
			}
			end := i + cfg.Batch - 1
			if end > plan.high {
				end = plan.high
			}
			jobs = append(jobs, batchJob{
				group: plan.group, lo: i, hi: end,
				cutoff:   g.cutoff(cfg),
				throttle: time.Duration(g.ThrottleMs) * time.Millisecond,
			})
		}
		p.tel.crawl.notePlanned(plan.group, len(jobs)-before)
	}
	if budgetHit {
		p.crawlJob.Log("%s: pass budget reached (%d batches) — remaining work rolls into the next round",
			bb, len(jobs))
	}
	if len(jobs) == 0 {
		p.crawlJob.Log("%s: %d group(s), nothing new", bb, len(plans))
		return 0, claimed
	}

	// 2. Fetch + stage in parallel over the pool. Each group's watermark and
	// coverage advance THE MOMENT its last batch lands (onGroup, called on
	// this goroutine) — a catch-up pass runs for hours, and advancing only at
	// pass end froze the coverage/backlog readouts for the duration and lost
	// the whole pass's progress when a deploy killed the worker.
	p.crawlJob.Log("%s: crawling %d group(s), %d batch(es) over %d connection(s) on %s…",
		bb, len(plans), len(jobs), totalConns(runs), providerLabels(runs))
	staged, advanced := 0, 0
	// Mid-round pressure probe: the entry gate at the top of the catch-up loop
	// re-checks once per ROUND, and a round here is up to crawl_max_batches —
	// hours of staging with nothing watching the ceiling in between. When the
	// probe trips, runBatches stops feeding new batches; the fetched contiguous
	// prefix still advances below and the rest re-plans next round, where the
	// entry gate sees the same pressure and pauses the pass properly.
	pressureStopped := false
	overBudget := func() bool {
		pause, pr := p.pauseForStagingPressure(ctx, cfg)
		if pause && !pressureStopped {
			pressureStopped = true
			p.crawlJob.Log("%s: staging pressure %.0f%% crossed %d%% mid-round — no new batches fed; "+
				"fetched work still lands and the rest re-plans next round", bb, pr*100, cfg.crawlStopPct())
		}
		return pause
	}
	leftover := p.runBatches(ctx, runs, jobs, cfg, &p.tel.crawl, func(name string, rs []batchResult) {
		plan := plans[name]
		if plan == nil {
			return
		}
		s, adv := p.advanceOneGroup(ctx, bb, plan, rs)
		// Attribution lands with the group, not with the round. A round is up
		// to crawl_max_batches (20,000) against whatever backlog exists — on a
		// 347M-article group that is hours — so round-scoped flushing still
		// showed an operator an empty table while the crawler was fetching
		// millions of articles past the very posters they were watching. This
		// is the same reasoning that already advances the watermark and
		// coverage here rather than at pass end.
		p.flushPosterHits(ctx)
		staged += s
		if adv {
			advanced++
			p.crawlJob.Log("%s/%s: group complete — watermark advanced, %d article(s) staged",
				bb, name, s)
		}
	}, overBudget)

	// 3. Final sweep: partial credit for groups a cancelled pass left
	// incomplete — their contiguous prefix still advances. NOT on lease loss:
	// another worker may own these groups now, and a late watermark write
	// would clobber theirs. Completed groups already advanced via onGroup.
	if ctx.Err() != nil && jobCtx.Err() == nil {
		p.crawlJob.Log("%s: lease lost mid-pass — final sweep skipped; %d group(s) already advanced", bb, advanced)
	} else {
		s2, a2 := p.advanceWatermarks(ctx, bb, plans, leftover)
		staged += s2
		advanced += a2
	}

	open, target, resets := fleetStats(runs)
	p.crawlJob.Log("%s: %d group(s), %d batch(es), %d article(s) staged, %d advanced (conns %d/%d, resets %d)",
		bb, len(plans), len(jobs), staged, advanced, open, target, resets)
	return staged, claimed
}

// idleHealthCheck runs a health pass only when the indexer has nothing better to
// do: no new articles this crawl AND no backfill left. Health checking is
// bookkeeping — it earns connections only once the work that produces content
// has none to spend. (Prod is looser: it drains health whenever backfill is
// exhausted, even on a pass that just fetched tens of thousands of articles.)
func (p *Plugin) idleHealthCheck(ctx context.Context) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	pending, err := p.st.anyBackfillPending(ctx)
	if err != nil || pending {
		return // backfill still has history to pull; it goes first
	}
	p.runHealthCheck(ctx)
}

// nextCrawl is the displayed next-run for the crawl/build jobs. It MUST
// read the live effective interval, not the boot p.cfg: the scheduler
// sleeps for schedule.IntervalOverride (→ p.effective().CrawlIntervalMin),
// so computing the display off the frozen boot value made the countdown
// disagree with reality after any admin interval change — the crawl
// fired on the live interval while the UI still counted down the old one.
func (p *Plugin) nextCrawl(ctx context.Context) time.Time {
	return time.Now().Add(time.Duration(p.effective(ctx).CrawlIntervalMin) * time.Minute)
}

// planGroup selects the group once to learn the server's bounds and works out
// which article numbers this pass should fetch.
func (p *Plugin) planGroup(ctx context.Context, pool *nntp.Pool, g groupRow, cfg Config) (*crawlPlan, error) {
	var low, high int
	sel := func(c *nntp.Conn) error {
		_, l, h, err := c.Group(g.Name)
		if err != nil {
			return err
		}
		low, high = l, h
		return nil
	}
	err := pool.Do(ctx, sel)
	if err != nil {
		// A pass that starts after an idle gap begins with every pooled
		// connection dead — providers drop idle NNTP sessions, and the corpse
		// answers "400 Idle timeout" on first use. The failed Do has already
		// discarded the stale socket; refill and retry ONCE on a fresh dial,
		// or one stale socket per group silently costs the whole pass
		// (observed on prod 2026-07-24: 20/20 groups planned zero batches,
		// pass after pass, with 575M articles behind).
		pool.TopUp(ctx)
		err = pool.Do(ctx, sel)
	}
	if err != nil {
		return nil, err
	}
	start := int(g.HighWatermark) + 1
	if g.HighWatermark == 0 {
		start = high - cfg.MaxArticlesPerGroup + 1 // first pass: cap the volume
	}
	if start < low {
		start = low
	}
	return &crawlPlan{
		group: g.Name, low: low, high: high,
		start: start, hasWork: start <= high,
	}, nil
}

// runBatches fetches every job across the pool. Worker count matches the pool
// size — more would just queue on the pool's blocking fallback, which is the
// backpressure that keeps us from outrunning the server.
//
// onGroup fires the moment a GROUP's last batch lands (called on this
// goroutine, in completion order) so its watermark and coverage advance
// mid-pass — a catch-up pass runs for hours, and batching every state write
// to the end froze the dashboard's coverage/backlog for the duration AND
// threw away the whole pass's progress if the worker was killed. Only the
// results of groups that did NOT complete (context cancelled mid-pass) are
// returned, for the caller's final partial-advance sweep.
// groupByBackbone buckets the pass's providers by backbone, preserving the
// order activeFleet chose so the primary account still leads its own bucket.
// Providers on one backbone share watermarks, so they must crawl as one pass.
func groupByBackbone(runs []providerRun) [][]providerRun {
	var order []string
	byKey := map[string][]providerRun{}
	for _, r := range runs {
		k := r.prov.backboneKey()
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], r)
	}
	out := make([][]providerRun, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// fleetStats sums the live connection state across every pool the pass used.
// Reporting only the lead account's numbers would show "50/50" on a backbone
// running 100 connections, and would hide a second account that was resetting
// constantly behind a healthy-looking first one.
func fleetStats(runs []providerRun) (open, target int, resets int64) {
	for _, r := range runs {
		st := r.pool.Stats()
		open += st.Open
		target += st.Target
		resets += st.Resets
	}
	return open, target, resets
}

// totalConns is the pass's whole connection budget across its providers.
func totalConns(runs []providerRun) int {
	n := 0
	for _, r := range runs {
		n += r.size
	}
	return n
}

// providerLabels names the accounts sharing this pass, for the log line — an
// operator seeing "100 connections" needs to know which accounts they came
// from, especially when one is benched and the number silently halves.
func providerLabels(runs []providerRun) string {
	names := make([]string, 0, len(runs))
	for _, r := range runs {
		names = append(names, r.prov.label())
	}
	return strings.Join(names, " + ")
}

// assignPools maps each fetch worker to the pool it will draw from, dealing
// one worker at a time around the providers so the load spreads evenly instead
// of filling the first account before touching the second.
//
// Bounded by what each pool actually opened: a provider that opened 10 takes
// 10 workers and no more, and the remainder go to whoever still has room. That
// is what keeps a small standby account from being handed a third of the work
// it cannot serve.
func assignPools(runs []providerRun, workers int) []*nntp.Pool {
	out := make([]*nntp.Pool, 0, workers)
	room := make([]int, len(runs))
	for i, r := range runs {
		room[i] = r.size
	}
	for len(out) < workers {
		dealt := false
		for i, r := range runs {
			if room[i] <= 0 {
				continue
			}
			out = append(out, r.pool)
			room[i]--
			dealt = true
			if len(out) == workers {
				break
			}
		}
		// Every pool is at capacity. batchWorkers already caps at the same
		// total, so this is unreachable in practice — but an unreachable
		// infinite loop is still an infinite loop.
		if !dealt {
			break
		}
	}
	return out
}

// batchWorkers is how many fetch goroutines a pass runs: one per available
// connection, never more than there is work for, never zero.
//
// Extracted so the budget decision is testable without an NNTP server, because
// getting it wrong is silent. It is capped by the pass's RESOLVED budget
// (providerRun.size) rather than the site-wide connections setting; those
// diverge as soon as a second crawler host joins and the account cap is split,
// and the surplus goroutines then queue in the pool for connections that were
// never going to exist.
func batchWorkers(conns, jobs int) int {
	w := conns
	if w > jobs {
		w = jobs
	}
	if w < 1 {
		w = 1
	}
	return w
}

// pressureCheckEvery is how many COMPLETED batches pass between overBudget
// probes inside runBatches. Granularity, not policy: at the default batch size
// one interval is ~300k article numbers (~90 MB of staging worst-case), small
// against the ~640 MB between the eviction ceiling and maxmemory, and one
// cheap probe per interval. The thresholds themselves stay in Config.
const pressureCheckEvery = 100

func (p *Plugin) runBatches(ctx context.Context, runs []providerRun, jobs []batchJob, cfg Config, tel *passTracker, onGroup func(group string, rs []batchResult), overBudget func() bool) []batchResult {
	// The budget is the sum of what THIS pass's pools actually opened
	// (providerRun.size), not the site-wide setting. They differ the moment a
	// second crawler host joins: the account cap is split across the fleet, so
	// a pool opens 25 while cfg.Connections still reads 50, and the surplus
	// goroutines would sit in the pool's blocking fallback waiting on
	// connections that were never going to exist.
	workers := batchWorkers(totalConns(runs), len(jobs))
	assigned := assignPools(runs, workers)
	expected := make(map[string]int, 8)
	for _, j := range jobs {
		expected[j.group]++
	}

	jobCh := make(chan batchJob)
	resCh := make(chan batchResult, len(jobs)) // buffered: workers never block on send
	// Closed when overBudget trips: the feeder stops handing out NEW work while
	// everything in flight completes normally. Unfed jobs simply never produce
	// results, and both callers survive that by design — the crawl's watermark
	// only advances through a group's contiguous fetched prefix, so unfetched
	// batches roll into the next round losslessly. This check exists because a
	// crawl round is bounded by crawl_max_batches (default 20,000) PER BACKBONE
	// between the loop's per-round pressure gates: one catch-up round could
	// stage gigabytes past the eviction ceiling, and at maxmemory Redis evicts
	// the forming sets — the 97M-key incident — while every write "succeeds".
	stopFeed := make(chan struct{})

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		pool := assigned[w]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				resCh <- p.fetchBatch(ctx, pool, j, tel)
			}
		}()
	}
	go func() {
	feed:
		for _, j := range jobs {
			select {
			case jobCh <- j:
			case <-stopFeed:
				break feed
			case <-ctx.Done():
				break feed
			}
		}
		close(jobCh)
		wg.Wait()
		close(resCh)
	}()

	completed, stopped := 0, false
	byGroup := make(map[string][]batchResult, len(expected))
	for r := range resCh {
		completed++
		if overBudget != nil && !stopped && completed%pressureCheckEvery == 0 && overBudget() {
			stopped = true
			close(stopFeed)
		}
		byGroup[r.group] = append(byGroup[r.group], r)
		if onGroup != nil && len(byGroup[r.group]) == expected[r.group] {
			onGroup(r.group, byGroup[r.group])
			delete(byGroup, r.group)
		}
	}
	var leftover []batchResult
	for _, rs := range byGroup {
		leftover = append(leftover, rs...)
	}
	return leftover
}

// fetchBatch pulls one overview range and stages it. The connection is returned
// to the pool before any parsing or database work.
//
// tel is the tracker that OWNS this batch. It is a parameter rather than
// p.tel.crawl because backfill shares this path: hardcoding the forward
// tracker meant every backfill batch incremented the forward crawl's counters,
// which is how the progress bar reached "21,000 / 20,000 batches (100.0%)" —
// the numerator was counting two jobs, the denominator one. It also put the
// backfill's newsgroup in the forward pass's "reading" field, so the live view
// named a group the forward crawl was not on.
func (p *Plugin) fetchBatch(ctx context.Context, pool *nntp.Pool, j batchJob, tel *passTracker) batchResult {
	res := batchResult{group: j.group, lo: j.lo, hi: j.hi}
	if ctx.Err() != nil {
		return res
	}
	tel.noteReading(j.group)

	var ovs []nntp.MessageOverview
	var wire int64
	err := pool.Do(ctx, func(c *nntp.Conn) error {
		// The pool hands out whichever connection is free and another caller may
		// have selected a different group on it, so always re-select.
		if _, _, _, err := c.Group(j.group); err != nil {
			return err
		}
		got, wb, err := c.Overview(j.lo, j.hi)
		if err != nil {
			return err
		}
		ovs, wire = got, wb
		return nil
	})
	if err != nil {
		p.reportErr(ctx, "usenet/crawl-fetch",
			fmt.Errorf("%s %d-%d: %w", j.group, j.lo, j.hi, err))
		tel.noteBatchFor(j.group, 0, 0, 0, false)
		return res // ok stays false — the watermark will not pass this range
	}
	res.articles, res.wire = len(ovs), wire
	res.maxDate = newestDate(ovs)
	res.minDate = oldestDate(ovs)

	arts := parseOverviews(ovs, j.group, j.cutoff, p.hits, p.posterWatch, p.posterHits)
	if len(arts) > 0 {
		n, err := p.staging.stageArticles(ctx, arts)
		if err != nil {
			// Leave ok=false so the watermark does NOT move past articles we never
			// stored. (Prod drops the batch on a staging error but keeps the
			// already-advanced watermark, losing those articles permanently.)
			p.reportErr(ctx, "usenet/crawl-stage",
				fmt.Errorf("%s %d-%d: %w", j.group, j.lo, j.hi, err))
			tel.noteBatchFor(j.group, res.articles, 0, wire, false)
			return res
		}
		res.staged = n
	}
	res.ok = true
	tel.noteBatchFor(j.group, res.articles, res.staged, wire, true)
	// Per-group pacing: some providers rate limit per group, and some groups are
	// not worth saturating the pool for. Applied after the connection is back in
	// the pool, so throttling this group frees capacity for others rather than
	// idling a connection.
	if j.throttle > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(j.throttle):
		}
	}
	return res
}

// advanceWatermarks runs advanceOneGroup for every group in results — the
// end-of-pass sweep for groups a cancelled pass left incomplete (completed
// groups already advanced via runBatches' onGroup callback).
func (p *Plugin) advanceWatermarks(ctx context.Context, backbone string, plans map[string]*crawlPlan, results []batchResult) (staged, advanced int) {
	byGroup := make(map[string][]batchResult, len(plans))
	for _, r := range results {
		byGroup[r.group] = append(byGroup[r.group], r)
	}
	for name, rs := range byGroup {
		plan := plans[name]
		if plan == nil {
			continue
		}
		s, adv := p.advanceOneGroup(ctx, backbone, plan, rs)
		staged += s
		if adv {
			advanced++
		}
	}
	return staged, advanced
}

// advanceOneGroup moves one group's high watermark to the end of its last
// CONTIGUOUS run of successful batches, recording the fetched ranges. A
// failure in the middle stops the advance there, so the failed range is
// refetched next pass instead of being silently skipped — with parallel
// batches, "highest success" would strand gaps.
func (p *Plugin) advanceOneGroup(ctx context.Context, backbone string, plan *crawlPlan, rs []batchResult) (staged int, advanced bool) {
	for _, r := range rs {
		staged += r.staged
		if !r.ok {
			continue
		}
		if err := p.st.recordFetchedRangeFor(ctx, backbone, plan.group, int64(r.lo), int64(r.hi)); err != nil {
			p.reportErr(ctx, "usenet/crawl-range-record", err)
		}
	}
	highest, latest := contiguousEnd(plan.start, rs)

	var watermark int64
	if highest >= plan.start {
		watermark = int64(highest)
		advanced = true
	} else {
		p.crawlJob.Log("%s: no contiguous progress this pass — retrying from %d", plan.group, plan.start)
	}
	if err := p.st.updateGroupStateForBackbone(ctx, backbone, plan.group, int64(plan.low), int64(plan.high),
		watermark, int64(plan.start), latest); err != nil {
		p.reportErr(ctx, "usenet/crawl-watermark", fmt.Errorf("%s: %w", plan.group, err))
	}
	return staged, advanced
}

// contiguousEnd returns the end of the unbroken run of successful batches
// beginning at start, together with the newest article date seen across that run
// (dates from batches beyond a break are ignored — they are not yet covered).
// Returns start-1 when nothing contiguous succeeded. Sorts rs in place.
func contiguousEnd(start int, rs []batchResult) (int, time.Time) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].lo < rs[j].lo })
	highest := start - 1
	var latest time.Time
	for _, r := range rs {
		if !r.ok || r.lo > highest+1 {
			break
		}
		highest = r.hi
		if r.maxDate.After(latest) {
			latest = r.maxDate
		}
	}
	return highest, latest
}

// parseOverviews turns overview lines into staged articles, dropping ones with
// no message-id and ones posted before the retention cutoff.
//
// hits may be nil (tests): junk counting is observability, not behaviour.
func parseOverviews(ovs []nntp.MessageOverview, group string, cutoff time.Time, hits *filterHits, watch *posterWatch, ph *posterHits) []stagedArticle {
	out := make([]stagedArticle, 0, len(ovs))
	for _, ov := range ovs {
		if ov.MessageId == "" {
			continue
		}
		// Before anything reads the subject: it is a raw header, and a poster
		// writing outside ASCII sends it RFC 2047 encoded. Undecoded it is
		// unparseable AND reads as punctuation soup to the junk engine.
		subject := decodeSubject(ov.Subject)
		if !ov.Date.IsZero() && ov.Date.Before(cutoff) {
			// A watched poster's article dropped for age is worth saying out
			// loud: "the crawl depth excludes them" looks identical to "the
			// crawler never saw them" in every other readout.
			if p, ok := watch.watched(ov.From); ok {
				ph.note(p, "ingest", "before-retention-cutoff", subject)
			}
			continue
		}
		base, pn, tp, seg, fn, tf, fp := parseSubject(subject)
		if rule := whichJunkRule(base); rule != "" {
			hits.note("junk", rule, base)
			if p, ok := watch.watched(ov.From); ok {
				ph.note(p, "ingest", rule, subject)
			}
			continue // obfuscated random-token post — never index it
		}
		// Record the keeps too. Without them "no rows for this poster" is
		// ambiguous between "never fetched" and "fetched and all dropped", and
		// those have opposite fixes.
		if p, ok := watch.watched(ov.From); ok {
			ph.note(p, "ingest", "staged", subject)
		}
		out = append(out, stagedArticle{
			ArticleNum: ov.MessageNumber,
			MessageID:  ov.MessageId, Subject: subject, BaseSubject: base,
			Poster: ov.From, Bytes: int64(ov.Bytes), Posted: ov.Date, Group: group,
			PartNum: pn, TotalParts: tp, SegTotal: seg, FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	return out
}

// ── store methods for crawling ──────────────────────────────────────

type groupRow struct {
	Name          string
	HighWatermark int64
	// Per-group tuning (migration 013). RetentionDays 0 means "use the
	// plugin-wide crawl depth".
	RetentionDays int
	ThrottleMs    int
	Tier          Tier
}

// cutoff resolves this group's crawl horizon, falling back to the global depth.
func (g groupRow) cutoff(cfg Config) time.Time { return g.cutoffAt(cfg, time.Now()) }

// cutoffAt takes the reference instant so the rule can be asserted exactly.
// Comparing two results of a time.Now()-based cutoff is a coin flip on a
// fine-grained clock, which made the fallback test pass on Windows and fail on
// Linux for reasons that had nothing to do with retention.
func (g groupRow) cutoffAt(cfg Config, now time.Time) time.Time {
	days := g.RetentionDays
	if days <= 0 {
		days = cfg.RetentionDays
	}
	return now.AddDate(0, 0, -days)
}

func (s *PGStore) activeGroups(ctx context.Context, limit int) ([]groupRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name string `db:"name"`
		HW   int64  `db:"high_watermark"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT name, high_watermark FROM newsgroups WHERE active = TRUE ORDER BY name LIMIT $1`, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]groupRow, len(rows))
	for i, r := range rows {
		out[i] = groupRow{Name: r.Name, HighWatermark: r.HW}
	}
	return out, nil
}

func (s *PGStore) stageArticles(ctx context.Context, arts []stagedArticle) (int, error) {
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, a := range arts {
			var posted sql.NullTime
			if !a.Posted.IsZero() {
				posted = sql.NullTime{Time: a.Posted, Valid: true}
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO articles
				   (message_id, subject, base_subject, poster, bytes, posted, group_name,
				    part_num, total_parts, seg_total, file_num, total_files, file_parts)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
				 ON CONFLICT (message_id) DO NOTHING`,
				a.MessageID, a.Subject, a.BaseSubject, a.Poster, a.Bytes, posted, a.Group,
				a.PartNum, a.TotalParts, a.SegTotal, a.FileNum, a.TotalFiles, a.FileParts)
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n++
			}
		}
		return nil
	})
	return n, err
}

// newestDate / oldestDate scan an overview batch for its date bounds (used to
// stamp watermarks and to detect the retention horizon during backfill).
func newestDate(ovs []nntp.MessageOverview) time.Time {
	var t time.Time
	for _, ov := range ovs {
		if ov.Date.After(t) {
			t = ov.Date
		}
	}
	return t
}

func oldestDate(ovs []nntp.MessageOverview) time.Time {
	var t time.Time
	for _, ov := range ovs {
		if ov.Date.IsZero() {
			continue
		}
		if t.IsZero() || ov.Date.Before(t) {
			t = ov.Date
		}
	}
	return t
}

// pauseForStagingPressure reports whether this round must not stage, and the
// pressure reading behind that answer.
//
// A method rather than an inline check so the WIRING is testable, not just the
// arithmetic: a test that only exercises shouldPauseForPressure passes happily
// while the crawl loop ignores it entirely, which is exactly what mutation
// testing caught here.
//
// Fails OPEN. If the backend cannot report its own fullness, crawling is the
// safer default — refusing to stage on an unreadable gauge would idle the
// crawler over a monitoring failure.
func (p *Plugin) pauseForStagingPressure(ctx context.Context, cfg Config) (bool, float64) {
	if p.staging == nil {
		return false, 0
	}
	pr, err := p.staging.pressure(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/crawl-pressure", err)
		return false, 0
	}
	return shouldPauseForPressure(pr, cfg.crawlStopPct()), pr
}
