package usenet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

// runBackfill closes the GAPS below each group's back_watermark.
//
// Coverage is tracked in newsgroup_ranges, so "what is still missing" is the
// complement of the fetched runs rather than a single downward pointer. That
// matters for throughput: gaps from every group go into one flat batch queue and
// are fetched in PARALLEL across the shared connection pool, where a pointer
// walk can only ever fetch one batch at a time per group.
//
// A pass is bounded by BackfillBatchesPerRun across all groups, so a large
// history doesn't monopolise the pool.
func (p *Plugin) runBackfill(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.backfillMu.TryLock() {
		p.backfillJob.Log("backfill already running — skipping overlap")
		return
	}
	defer p.backfillMu.Unlock()
	p.backfillJob.SetRunning()
	cfg := p.effective(ctx)

	if cfg.SkipBackfill {
		p.backfillJob.Log("backfill disabled (skip_backfill) — new articles only")
		p.backfillJob.SetIdle(p.nextBackfill(ctx))
		return
	}

	if yield, pr := p.backfillYields(ctx, cfg); yield {
		p.backfillJob.Log("backfill paused: staging pressure %.0f%% (high %d%%, resumes below %d%%) — letting the NZB builder drain",
			pr*100, cfg.BackfillPressureHighPct, cfg.BackfillPressureLowPct)
		p.backfillJob.SetIdle(p.nextBackfill(ctx))
		return
	}

	// Same pools as the forward crawl: providers cap connections per account, so
	// a second set would just push each account over its limit.
	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.backfillJob.Log("no server configured")
			p.backfillJob.SetIdle(p.nextBackfill(ctx))
			return
		}
		p.backfillJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/backfill-fleet", err)
		return
	}

	p.tel.backfill.passStart(len(runs))
	defer p.tel.backfill.passEnd()

	// Catch-up loop: while history remains and staging has room, go again
	// immediately instead of sleeping out the interval.
	//
	// The forward crawl has had this since it was written, with the reasoning
	// spelled out there — "missing a lot of articles while sitting idle is
	// exactly wrong" — and the backfill, which is the job with 659 MILLION
	// articles outstanding, never got it. It did one 25-batch pass in about two
	// seconds and then slept 58, a 6% duty cycle measured against a queue that
	// will take days to clear. Nothing was throttling it: the pressure gate sits
	// at 85% and staging was at 77%, so the job simply ran out of budget and
	// went home. Bigger batches would only make each nap longer.
	//
	// Guarded the same three ways as the crawl loop: a round that COMPLETES no
	// batches ends the pass (planned-but-failing work must not count, or a dead
	// provider spins the loop forever), pressure is re-checked every round
	// rather than once at entry, and the operator can switch it off.
	lastLog := time.Now()
	res := runCatchUp(ctx,
		func() (int, int) {
			p.tel.backfill.roundStart()
			staged, worked := 0, 0
			for _, run := range runs {
				if ctx.Err() != nil {
					return staged, worked
				}
				st, b := p.backfillProvider(ctx, run, cfg)
				staged += st
				worked += b
			}
			// Drain while filling. The builder holds its own lock and no-ops if
			// it is already running, so kicking it each productive round keeps
			// staging falling instead of climbing toward the pressure gate this
			// loop is otherwise racing.
			if staged > 0 {
				go p.runBuild(ctx)
			}
			return staged, worked
		},
		func() bool {
			// Re-read per round: a catch-up pass can run for a long time, and an
			// operator switching it off should not have to wait for it to end.
			cfg = p.effective(ctx)
			return cfg.BackfillNoCatchup
		},
		func() (bool, float64) { return p.waitForDrain(ctx, cfg) },
		func(rounds, batches, staged int) {
			if time.Since(lastLog) >= 30*time.Second {
				p.backfillJob.Log("catch-up: %d round(s), %s batch(es), %s article(s) staged so far",
					rounds, fmtComma(int64(batches)), fmtComma(int64(staged)))
				lastLog = time.Now()
			}
		})
	total, totalBatches, rounds := res.Staged, res.Batches, res.Rounds
	if res.StoppedBy == stopPressure {
		p.backfillJob.Log("catch-up stopping: staging pressure %.0f%% (high %d%%) after %d round(s)",
			res.Pressure*100, cfg.BackfillPressureHighPct, rounds)
	}
	p.backfillJob.Log("backfill complete across %d provider(s): %s article(s) staged from %s batch(es) over %d round(s)",
		len(runs), fmtComma(int64(total)), fmtComma(int64(totalBatches)), rounds)
	p.backfillJob.SetIdle(p.nextBackfill(ctx))
	if total > 0 {
		go p.runBuild(ctx)
	}
}

// backfillYields reports whether staging is too full to keep fetching.
//
// Hysteresis: pause at the high-water mark, resume only below the low one, so
// a backend hovering at the threshold does not flap. Extracted so the entry
// check and the per-round check are literally the same code — the crawl loop
// grew three drifted copies of its pressure comparison exactly this way.
func (p *Plugin) backfillYields(ctx context.Context, cfg Config) (bool, float64) {
	pr, err := p.staging.pressure(ctx)
	if err != nil {
		return false, 0 // unknown pressure is not a reason to stop protecting history
	}

	// Yielding is only useful if the builder can act on it.
	//
	// This gate deadlocked production. Once pressure touched the high-water mark
	// the hysteresis latched, and it only releases below the LOW mark — but what
	// fills memory here is mostly INCOMPLETE sets, releases waiting for segments
	// that nothing except the backfill can fetch. The builder cannot assemble
	// those, so memory never fell, the latch never cleared, and the fetcher stayed
	// off. Observed at 74% pressure against an 85% threshold, ready queue at 119,
	// every job idle 92% of the time and the operator asking why nothing was
	// working. It was not throttling; it was stuck.
	//
	// So the rule is: pause while there is a DRAINABLE backlog, and when there is
	// nothing to build, keep fetching — because fetching is the only thing that
	// turns those half-finished sets into releases and frees the memory. The
	// ceiling still applies, because past it Redis evicts rather than refuses and
	// that is how 97 million keys were destroyed.
	if depth, derr := p.staging.readyDepth(ctx); derr == nil && depth == 0 {
		if pr < float64(cfg.BackfillPressureCeilingPct)/100.0 {
			p.backfillPaused = false
			return false, pr
		}
		return true, pr
	}

	switch {
	case p.backfillPaused && pr >= float64(cfg.BackfillPressureLowPct)/100.0:
		return true, pr
	case pr >= float64(cfg.BackfillPressureHighPct)/100.0:
		p.backfillPaused = true
		return true, pr
	}
	p.backfillPaused = false
	return false, pr
}

// backfillProvider closes one provider's gaps. Gaps are per-server: another
// provider's coverage says nothing about this one's, because the article numbers
// are not the same articles.
func (p *Plugin) backfillProvider(ctx context.Context, run providerRun, cfg Config) (staged, batches int) {
	pool, bb := run.pool, run.prov.backboneKey()
	pool.TopUp(ctx)

	groups, err := p.st.groupsNeedingBackfillForBackbone(ctx, bb, cfg.MaxGroups)
	if err != nil {
		p.reportErr(ctx, "usenet/backfill-groups", err)
		return 0, 0
	}
	if len(groups) == 0 {
		p.backfillJob.Log("%s: nothing to backfill — caught up to the retention horizon", run.prov.label())
		return 0, 0
	}
	// Backfill leases the same (backbone, group) keys as the forward crawl: both
	// advance the same row, so they must not run on it from two workers at once.
	rows := make([]groupRow, len(groups))
	for i, g := range groups {
		rows[i] = groupRow{Name: g.Name}
	}
	// From here the pass runs on the lease context: losing the lease mid-pass
	// cancels it (a sibling may own these groups). jobCtx distinguishes lease
	// loss from shutdown when deciding whether to record coverage below.
	jobCtx := ctx
	heldRows, ctx, release := p.claimGroupLeases(ctx, bb, rows, p.leaseTTL(cfg))
	defer release()
	if len(heldRows) == 0 {
		return 0, 0
	}
	mine := make(map[string]bool, len(heldRows))
	for _, r := range heldRows {
		mine[r.Name] = true
	}
	kept := groups[:0]
	for _, g := range groups {
		if mine[g.Name] {
			kept = append(kept, g)
		}
	}
	groups = kept

	// Collect every eligible group's candidate batches first, then SHARE the
	// pass budget between them.
	//
	// This loop used to hand gapJobs the whole remaining budget on the first
	// iteration, so the first group with gaps consumed the entire pass and
	// `budget <= 0` broke out before any other group was looked at. The old
	// comment claimed the budget stopped exactly that; it caused it. Every pass
	// then reported "backfilling 1 group(s)" and the progress widget sat at
	// "0 / 1 groups", because one group was targeted and it was nowhere near
	// finishing: the leading group here has 2.48 BILLION articles of history
	// left, which at the observed rate is over two months during which the
	// other three groups would not advance one batch.
	budget := cfg.BackfillBatchesPerRun
	targets := make(map[string]backfillRow, len(groups))

	var cands []candidate
	for _, g := range groups {
		if ctx.Err() != nil {
			return 0, 0
		}
		gaps, err := p.st.backfillGapsFor(ctx, bb, g.Name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.reportErr(ctx, "usenet/backfill-gaps", fmt.Errorf("%s/%s: %w", run.prov.label(), g.Name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDoneForBackbone(ctx, bb, g.Name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s/%s: backfill complete — no gaps remain", run.prov.label(), g.Name)
			continue
		}
		gj := gapJobs(g.Name, gaps, cfg.Batch, budget)
		if len(gj) == 0 {
			continue
		}
		gc := groupRow{Name: g.Name, RetentionDays: g.RetentionDays, ThrottleMs: g.ThrottleMs}.cutoff(cfg)
		for i := range gj {
			gj[i].cutoff = gc
			gj[i].throttle = time.Duration(g.ThrottleMs) * time.Millisecond
		}
		cands = append(cands, candidate{g: g, batches: gj})
	}

	// Share WITHIN a tier, but never ACROSS one.
	//
	// Sharing the budget evenly over every candidate fixed one starvation and
	// caused another, worse one. The groups are already tier-ordered by the
	// query, and the tiers mean something: with hold_low_until_backfilled set,
	// LOW-tier groups are not crawled at all until every CRITICAL group has its
	// history. Splitting the budget three ways between one critical group and
	// two low ones therefore spent two thirds of every pass on history that is
	// gated behind the very group it was starving — and made the gate last
	// three times as long. Production sat with exactly one unfinished critical
	// group holding fourteen LOW groups, while most of the budget went to
	// alt.binaries.nzb, which is itself LOW.
	jobs, used := planPass(cands, budget)
	for i := range used {
		targets[used[i].g.Name] = used[i].g
		// The denominator for backfill's own progress bar. Without it
		// BatchesTotal stayed 0 and the bar never rendered, so a pass that runs
		// for hours showed a batch count with nothing to measure it against.
		p.tel.backfill.notePlanned(used[i].g.Name, used[i].taken)
	}
	if len(jobs) == 0 {
		p.backfillJob.Log("%s: nothing to do this pass", run.prov.label())
		return 0, 0
	}

	p.backfillJob.Log("%s: backfilling %d group(s), %d batch(es) over %d connection(s)…",
		run.prov.label(), len(targets), len(jobs), run.size)
	targetNames := make([]string, 0, len(targets))
	for name := range targets {
		targetNames = append(targetNames, name)
	}
	p.tel.backfill.noteGroups(targetNames)
	// nil onGroup: backfill passes are bounded (backfill_batches_per_run) and
	// recordBackfill needs the complete result set — with no callback,
	// runBatches returns every result.
	// Counting happens inside fetchBatch against the tracker passed here; a
	// second pass over the results would double every backfill batch.
	// nil overBudget: a backfill round is backfill_batches_per_run (~25)
	// batches, far below one pressureCheckEvery interval — its per-round
	// pressure gate in runCatchUp is the right granularity already.
	results := p.runBatches(ctx, []providerRun{run}, jobs, cfg, &p.tel.backfill, nil, nil)

	// On lease loss the coverage writes are skipped, not just doomed to fail:
	// a sibling may own these groups now, and coverage-IS-state means the
	// unrecorded batches are simply refetched next pass.
	if ctx.Err() != nil && jobCtx.Err() == nil {
		p.backfillJob.Log("%s: lease lost mid-pass — coverage not recorded; batches will refetch next pass", run.prov.label())
		return 0, 0
	}
	staged = p.recordBackfill(ctx, bb, targets, results, cfg)

	// The progress signal is batches COMPLETED, not planned. A failed batch
	// records no coverage, so the next round re-derives the identical gaps and
	// plans the identical jobs — planned work is a fixed point for a dead or
	// auth-broken provider, or a group the server 411s, and returning it once
	// spun the catch-up loop forever: the job showed Running indefinitely,
	// SetIdle was unreachable, and every round flooded the error ring with one
	// fetch error per batch. Completed work genuinely shrinks on an all-failing
	// round (to zero), which is exactly the stop condition runCatchUp needs.
	completed := completedBatches(results)
	if completed == 0 && len(jobs) > 0 && ctx.Err() == nil {
		p.backfillJob.Log("%s: every one of %d batch(es) failed — ending catch-up rather than replanning the same work",
			run.prov.label(), len(jobs))
	}
	st := pool.Stats()
	p.backfillJob.Log("%s: %d historical article(s) staged from %d/%d batch(es) (conns %d/%d, resets %d)",
		run.prov.label(), staged, completed, len(jobs), st.Open, st.Target, st.Resets)
	return staged, completed
}

// completedBatches counts the results whose fetch AND stage both succeeded —
// the only definition of progress that terminates: ok batches record coverage,
// so the next round's gap derivation genuinely shrinks.
func completedBatches(results []batchResult) int {
	n := 0
	for _, r := range results {
		if r.ok {
			n++
		}
	}
	return n
}

// nextBackfill is the displayed next-run for the backfill job. Reads the
// live effective interval, not the boot p.cfg — same reason as nextCrawl:
// the scheduler sleeps for the override, so a stale display disagreed with
// reality after an admin interval change.
func (p *Plugin) nextBackfill(ctx context.Context) time.Time {
	return time.Now().Add(time.Duration(p.effective(ctx).BackfillIntervalMin) * time.Minute)
}

// recordBackfill persists coverage for successful batches, then re-derives each
// group's remaining work. Unlike the forward crawl there is no contiguity rule:
// a failed batch simply leaves its gap unrecorded, so the next pass recomputes
// it and tries again — coverage IS the state, so nothing can be silently
// skipped.
func (p *Plugin) recordBackfill(ctx context.Context, backbone string, targets map[string]backfillRow, results []batchResult, cfg Config) (staged int) {
	byGroup := make(map[string][]batchResult, len(targets))
	for _, r := range results {
		staged += r.staged
		byGroup[r.group] = append(byGroup[r.group], r)
	}

	for name, rs := range byGroup {
		g, ok := targets[name]
		if !ok {
			continue
		}
		var oldest time.Time
		for _, r := range rs {
			if !r.ok {
				continue
			}
			if err := p.st.recordFetchedRangeFor(ctx, backbone, name, int64(r.lo), int64(r.hi)); err != nil {
				p.reportErr(ctx, "usenet/backfill-range-record", fmt.Errorf("%s: %w", name, err))
				continue
			}
			if !r.minDate.IsZero() && (oldest.IsZero() || r.minDate.Before(oldest)) {
				oldest = r.minDate
			}
		}

		// Reached this group's retention horizon: everything below is older still.
		cutoff := groupRow{Name: name, RetentionDays: g.RetentionDays}.cutoff(cfg)
		if horizonReached(rs, cutoff) {
			if err := p.st.markBackfillDoneForBackbone(ctx, backbone, name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: reached the retention horizon (a whole batch older than %s)",
				name, cutoff.Format("2006-01-02"))
			continue
		}

		// Re-derive what is left; the newest remaining gap is where the next pass
		// picks up, which is what back_watermark means to the coverage view.
		gaps, err := p.st.backfillGapsFor(ctx, backbone, name, g.ServerLow, g.BackWatermark)
		if err != nil {
			p.reportErr(ctx, "usenet/backfill-gaps", fmt.Errorf("%s: %w", name, err))
			continue
		}
		if len(gaps) == 0 {
			if err := p.st.markBackfillDoneForBackbone(ctx, backbone, name); err != nil {
				p.reportErr(ctx, "usenet/backfill-done", err)
			}
			p.backfillJob.Log("%s: backfill complete", name)
			continue
		}
		if err := p.st.updateBackWatermarkForBackbone(ctx, backbone, name, gaps[0].End, oldest); err != nil {
			p.reportErr(ctx, "usenet/backfill-watermark", fmt.Errorf("%s: %w", name, err))
		}
	}
	return staged
}

// horizonReached decides whether one group's pass proved its history is
// exhausted down to the retention cutoff. Marking a group backfill_done is the
// most consequential silent write in the pipeline — nothing resets it
// automatically, and the only recovery re-walks the group's entire history
// against a paying provider — so both rules here are load-bearing:
//
// A batch counts as past the horizon only when its NEWEST article is older
// than the cutoff. The previous rule used the pass's oldest article, which is
// poster-controlled boundary data: one forged or broken ancient Date header
// anywhere in one 3,000-article batch marked the whole group done and
// permanently stranded everything below the back watermark. A whole batch
// being old requires the poster to have forged every date in it. (The forward
// crawl's contiguousEnd stamps watermarks from maxDate for the same reason.)
//
// And the shortcut only fires when EVERY batch in the group's pass succeeded.
// A failed batch leaves its span unrecorded — recordBackfill's contract is
// that the next pass re-derives the gap and retries — but that contract only
// holds while the group stays open. Marking done past a failed batch orphans
// its gap forever, invisibly, behind a log line that reads as success.
//
// A pass of empty batches (deep history where every article has expired)
// reaches the horizon through the other path: its ranges are recorded, the
// gaps shrink, and the gaps==0 branch marks the group done.
func horizonReached(rs []batchResult, cutoff time.Time) bool {
	past := false
	for _, r := range rs {
		if !r.ok {
			return false
		}
		if !r.maxDate.IsZero() && r.maxDate.Before(cutoff) {
			past = true
		}
	}
	return past
}

// candidate is one group's planned batches for a pass.
type candidate struct {
	g       backfillRow
	batches []batchJob
	taken   int
}

// highestTierOnly keeps the most important tier that still has work.
//
// Tiers exist to say which groups matter more, and hold_low_until_backfilled
// makes that concrete: LOW groups are not crawled at all while a CRITICAL group
// still owes history. Spending a share of every pass on LOW history in that
// state is worse than useless — it lengthens exactly the gate that is holding
// those same groups back. Within one tier the budget is shared evenly, which is
// the starvation this originally set out to fix.
func highestTierOnly(cands []candidate) []candidate {
	if len(cands) == 0 {
		return cands
	}
	best := tierRank(normalizeTier(cands[0].g.Tier))
	for _, c := range cands[1:] {
		if r := tierRank(normalizeTier(c.g.Tier)); r < best {
			best = r
		}
	}
	out := cands[:0]
	for _, c := range cands {
		if tierRank(normalizeTier(c.g.Tier)) == best {
			out = append(out, c)
		}
	}
	return out
}

// planPass decides everything about one backfill pass's shape: which groups it
// touches and how much of the budget each gets.
//
// Both rules live here, behind ONE call, because they are a single decision and
// splitting them is how the wrong one ships. Tier filtering used to be a
// separate line at the call site, which meant a unit test of the filter passed
// happily while the call itself could be deleted without any test noticing —
// the same shape as a job that is registered but never scheduled.
func planPass(cands []candidate, budget int) ([]batchJob, []candidate) {
	cands = highestTierOnly(cands)
	perGroup := make([][]batchJob, len(cands))
	for i := range cands {
		perGroup[i] = cands[i].batches
	}
	jobs, taken := shareBudget(perGroup, budget)
	var used []candidate
	for i := range cands {
		if taken[i] == 0 {
			continue
		}
		cands[i].taken = taken[i]
		used = append(used, cands[i])
	}
	return jobs, used
}

// Why the catch-up decision lives in its own function.
//
// It has been wrong in production twice. First there was no loop at all, so the
// backfill did one 25-batch pass in two seconds and slept the other 58 with 659
// million articles outstanding — a 5.9% duty cycle. Then the loop shipped
// measuring progress by ARTICLES STAGED, which is wrong here because most of
// this history is empty on the server: a round routinely covers 150,000 article
// numbers, stages nothing, and that IS progress — the range is marked and never
// revisited. Breaking on staged == 0 put the job straight back to sleep and the
// duty cycle went to 15.7% instead of to full.
//
// Both bugs were invisible to the test suite because driving runBackfill needs a
// fake fleet, pools and store. This shape needs none of that.

// stop reasons for a catch-up pass.
const (
	stopNoWork    = "no-work"
	stopPressure  = "pressure"
	stopDisabled  = "disabled"
	stopCancelled = "cancelled"
)

type catchUpResult struct {
	Rounds, Batches, Staged int
	StoppedBy               string
	Pressure                float64
}

// runCatchUp repeats rounds while there is work to do and staging has room.
//
// round reports (articles staged, BATCHES COMPLETED). Completed batches are
// the progress signal, and both halves of that are load-bearing: staging
// nothing while covering ground is normal and must not end the pass (most
// deep history is empty on the server), while PLANNED batches must not count
// — a failed batch records nothing, so an all-failing provider re-plans the
// identical work every round and planned-count never reaches zero.
func runCatchUp(ctx context.Context, round func() (int, int), disabled func() bool,
	yields func() (bool, float64), afterRound func(rounds, batches, staged int)) catchUpResult {
	var res catchUpResult
	for {
		res.Rounds++
		staged, batches := round()
		res.Staged += staged
		res.Batches += batches
		if afterRound != nil {
			afterRound(res.Rounds, res.Batches, res.Staged)
		}
		switch {
		case ctx.Err() != nil:
			res.StoppedBy = stopCancelled
		case disabled != nil && disabled():
			res.StoppedBy = stopDisabled
		case batches == 0:
			// No batches COMPLETED anywhere: caught up to the retention horizon,
			// every group lease-held by a sibling worker — or every planned batch
			// failed, in which case the per-provider log has already said so and
			// re-planning the identical work would spin forever.
			res.StoppedBy = stopNoWork
		default:
			if y, pr := yields(); y {
				res.StoppedBy, res.Pressure = stopPressure, pr
			}
		}
		if res.StoppedBy != "" {
			return res
		}
	}
}

// waitForDrain is the pressure check, plus the thing that was missing: asking
// for the drain instead of just giving up.
//
// Staging filling is not a reason to stop working. It means the builder is
// behind, and the builder is the only thing that can relieve it — so the
// backfill would park itself for a full interval while the queue that blocks it
// sat there draining at whatever rate the builder's own schedule happened to
// manage. On this install that was 500 sets a minute against 1.46 million, so
// the pause was effectively permanent and every job showed idle.
//
// Now the backfill kicks the builder and waits, in short steps, re-checking as
// it goes. The two run together: one making room, the other using it. Bounded,
// so a builder that cannot make progress ends the pass rather than holding it
// open forever, and cancellable so shutdown is never delayed.
func (p *Plugin) waitForDrain(ctx context.Context, cfg Config) (bool, float64) {
	yield, pr := p.backfillYields(ctx, cfg)
	if !yield {
		return false, pr
	}
	waited := 0
	for waited < cfg.BackfillDrainWaitSec {
		// TryLock inside runBuild makes a redundant kick a no-op, so this is
		// "ensure a build is running", not "start another one".
		go p.runBuild(ctx)
		if !schedule.SleepCtx(ctx, drainPollInterval) {
			return true, pr // shutdown: stop, do not report a false all-clear
		}
		waited += int(drainPollInterval / time.Second)
		if again, pr2 := p.backfillYields(ctx, cfg); !again {
			p.backfillJob.Log("staging drained to %.0f%% after %ds — resuming backfill", pr2*100, waited)
			return false, pr2
		}
	}
	return true, pr
}

// drainPollInterval is how often the backfill re-checks while waiting for the
// builder. Short enough that it resumes promptly once room appears, long enough
// that the check itself is not the load.
const drainPollInterval = 10 * time.Second
