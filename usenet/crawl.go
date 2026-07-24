package usenet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/nntp"
	"github.com/the-loon-clan/loon/schedule"
)

// stagedArticle is one parsed overview line awaiting assembly.
type stagedArticle struct {
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
	// Pick up any admin edits to the junk rules before this pass filters anything.
	p.reloadJunkRules(ctx)

	runs, err := p.activeFleet(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.crawlJob.Log("no server configured — add one in the admin wizard")
			p.crawlJob.SetIdle(p.nextCrawl())
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
		staged, claimed := 0, 0
		for _, run := range runs {
			if ctx.Err() != nil {
				return
			}
			s, c := p.crawlProvider(ctx, run, cfg)
			staged += s
			claimed += c
		}
		totalStaged += staged
		if cfg.CrawlNoCatchup || ctx.Err() != nil {
			break
		}
		behind, err := p.st.forwardBacklog(ctx)
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
	p.crawlJob.SetIdle(p.nextCrawl())
	go p.runBuild(ctx)
	if totalStaged == 0 {
		go p.idleHealthCheck(ctx)
	}
}

// crawlProvider runs one provider's forward pass. Returns articles staged and
// how many groups this worker actually CLAIMED — the catch-up loop treats an
// all-blocked pass (claimed == 0 fleet-wide) as retry-shortly, not stalled.
func (p *Plugin) crawlProvider(ctx context.Context, run providerRun, cfg Config) (int, int) {
	pool, bb := run.pool, run.prov.backboneKey()
	pool.TopUp(ctx) // refill anything the last pass discarded

	groups, err := p.st.activeGroupsForBackbone(ctx, bb, cfg.MaxGroups)
	if err != nil {
		p.reportErr(ctx, "usenet/crawl-groups", err)
		return 0, 0
	}
	if len(groups) == 0 {
		p.crawlJob.Log("no active groups — pick some in the admin wizard")
		return 0, 0
	}
	// Split first, then lease. Assignment decides what to ATTEMPT so N crawlers
	// divide the work instead of racing; the lease then guarantees no two
	// workers touch one group even while a membership change settles.
	groups = p.myGroups(ctx, groups, cfg)
	if len(groups) == 0 {
		return 0, 0
	}
	groups, release := p.claimGroupLeases(ctx, bb, groups, p.leaseTTL(cfg))
	defer release()
	p.tel.crawl.noteGroups(len(groups))
	if len(groups) == 0 {
		p.crawlJob.Log("%s: every group already claimed by another worker", run.prov.label())
		return 0, 0
	}
	claimed := len(groups)

	// 1. Resolve each group's window and enqueue its batches.
	plans := make(map[string]*crawlPlan, len(groups))
	var jobs []batchJob
	for _, g := range groups {
		if ctx.Err() != nil {
			return 0, claimed
		}
		plan, err := p.planGroup(ctx, pool, g, cfg)
		if err != nil {
			p.reportErr(ctx, "usenet/crawl-plan",
				fmt.Errorf("%s/%s: %w", run.prov.label(), g.Name, err))
			p.crawlJob.Log("%s/%s: %v", run.prov.label(), g.Name, err)
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
	if len(jobs) == 0 {
		p.crawlJob.Log("%s: %d group(s), nothing new", run.prov.label(), len(plans))
		return 0, claimed
	}

	// 2. Fetch + stage in parallel over the pool. Each group's watermark and
	// coverage advance THE MOMENT its last batch lands (onGroup, called on
	// this goroutine) — a catch-up pass runs for hours, and advancing only at
	// pass end froze the coverage/backlog readouts for the duration and lost
	// the whole pass's progress when a deploy killed the worker.
	p.crawlJob.Log("%s: crawling %d group(s), %d batch(es) over %d connection(s)…",
		run.prov.label(), len(plans), len(jobs), run.size)
	staged, advanced := 0, 0
	leftover := p.runBatches(ctx, pool, jobs, cfg, func(name string, rs []batchResult) {
		plan := plans[name]
		if plan == nil {
			return
		}
		s, adv := p.advanceOneGroup(ctx, bb, plan, rs)
		staged += s
		if adv {
			advanced++
			p.crawlJob.Log("%s/%s: group complete — watermark advanced, %d article(s) staged",
				run.prov.label(), name, s)
		}
	})

	// 3. Final sweep: partial credit for groups a cancelled pass left
	// incomplete — their contiguous prefix still advances.
	s2, a2 := p.advanceWatermarks(ctx, bb, plans, leftover)
	staged += s2
	advanced += a2

	st := pool.Stats()
	p.crawlJob.Log("%s: %d group(s), %d batch(es), %d article(s) staged, %d advanced (conns %d/%d, resets %d)",
		run.prov.label(), len(plans), len(jobs), staged, advanced, st.Open, st.Target, st.Resets)
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

func (p *Plugin) nextCrawl() time.Time {
	return time.Now().Add(time.Duration(p.cfg.CrawlIntervalMin) * time.Minute)
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
func (p *Plugin) runBatches(ctx context.Context, pool *nntp.Pool, jobs []batchJob, cfg Config, onGroup func(group string, rs []batchResult)) []batchResult {
	workers := cfg.Connections
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}
	expected := make(map[string]int, 8)
	for _, j := range jobs {
		expected[j.group]++
	}

	jobCh := make(chan batchJob)
	resCh := make(chan batchResult, len(jobs)) // buffered: workers never block on send

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				resCh <- p.fetchBatch(ctx, pool, j)
			}
		}()
	}
	go func() {
		for _, j := range jobs {
			if ctx.Err() != nil {
				break
			}
			jobCh <- j
		}
		close(jobCh)
		wg.Wait()
		close(resCh)
	}()

	byGroup := make(map[string][]batchResult, len(expected))
	for r := range resCh {
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
func (p *Plugin) fetchBatch(ctx context.Context, pool *nntp.Pool, j batchJob) batchResult {
	res := batchResult{group: j.group, lo: j.lo, hi: j.hi}
	if ctx.Err() != nil {
		return res
	}
	p.tel.crawl.noteReading(j.group)

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
		p.tel.crawl.noteBatchFor(j.group, 0, 0, 0, false)
		return res // ok stays false — the watermark will not pass this range
	}
	res.articles, res.wire = len(ovs), wire
	res.maxDate = newestDate(ovs)
	res.minDate = oldestDate(ovs)

	arts := parseOverviews(ovs, j.group, j.cutoff, p.hits)
	if len(arts) > 0 {
		n, err := p.staging.stageArticles(ctx, arts)
		if err != nil {
			// Leave ok=false so the watermark does NOT move past articles we never
			// stored. (Prod drops the batch on a staging error but keeps the
			// already-advanced watermark, losing those articles permanently.)
			p.reportErr(ctx, "usenet/crawl-stage",
				fmt.Errorf("%s %d-%d: %w", j.group, j.lo, j.hi, err))
			p.tel.crawl.noteBatchFor(j.group, res.articles, 0, wire, false)
			return res
		}
		res.staged = n
	}
	res.ok = true
	p.tel.crawl.noteBatchFor(j.group, res.articles, res.staged, wire, true)
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
func parseOverviews(ovs []nntp.MessageOverview, group string, cutoff time.Time, hits *filterHits) []stagedArticle {
	out := make([]stagedArticle, 0, len(ovs))
	for _, ov := range ovs {
		if ov.MessageId == "" {
			continue
		}
		if !ov.Date.IsZero() && ov.Date.Before(cutoff) {
			continue
		}
		base, pn, tp, seg, fn, tf, fp := parseSubject(ov.Subject)
		if rule := whichJunkRule(base); rule != "" {
			hits.note("junk", rule, base)
			continue // obfuscated random-token post — never index it
		}
		out = append(out, stagedArticle{
			MessageID: ov.MessageId, Subject: ov.Subject, BaseSubject: base,
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
	LowPriority   bool
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
