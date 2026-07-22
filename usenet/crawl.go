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
}

type batchResult struct {
	group            string
	lo, hi           int
	minDate, maxDate time.Time
	staged           int
	ok               bool // fetched AND staged; only then may the watermark pass this range
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

	pool, err := p.ensurePool(ctx, cfg)
	if err != nil {
		if errors.Is(err, errNoServer) {
			p.crawlJob.Log("no server configured — add one in the admin wizard")
			p.crawlJob.SetIdle(p.nextCrawl())
			return
		}
		p.crawlJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/crawl-pool", err)
		return
	}
	pool.TopUp(ctx) // refill anything the last pass discarded

	groups, err := p.st.activeGroups(ctx, cfg.MaxGroups)
	if err != nil {
		p.crawlJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/crawl-groups", err)
		return
	}
	if len(groups) == 0 {
		p.crawlJob.Log("no active groups — pick some in the admin wizard")
		p.crawlJob.SetIdle(p.nextCrawl())
		return
	}

	// 1. Resolve each group's window and enqueue its batches.
	plans := make(map[string]*crawlPlan, len(groups))
	var jobs []batchJob
	for _, g := range groups {
		if ctx.Err() != nil {
			return
		}
		plan, err := p.planGroup(ctx, pool, g, cfg)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/crawl-plan", fmt.Errorf("%s: %w", g.Name, err))
			p.crawlJob.Log("%s: %v", g.Name, err)
			continue
		}
		plans[g.Name] = plan
		if !plan.hasWork {
			// Nothing new, but still record the server range so the coverage view
			// stays honest.
			if err := p.st.updateGroupState(ctx, plan.group, int64(plan.low), int64(plan.high),
				0, int64(plan.start), time.Time{}); err != nil {
				p.core.Errors.Report(ctx, "usenet/crawl-range", err)
			}
			continue
		}
		for i := plan.start; i <= plan.high; i += cfg.Batch {
			end := i + cfg.Batch - 1
			if end > plan.high {
				end = plan.high
			}
			jobs = append(jobs, batchJob{group: plan.group, lo: i, hi: end})
		}
	}
	if len(jobs) == 0 {
		p.crawlJob.Log("crawl complete: %d group(s), nothing new", len(plans))
		p.crawlJob.SetIdle(p.nextCrawl())
		go p.idleHealthCheck(ctx) // nothing to fetch — spend the idle pool on health
		return
	}

	// 2. Fetch + stage in parallel over the pool.
	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	p.crawlJob.Log("crawling %d group(s), %d batch(es) over %d connection(s)…",
		len(plans), len(jobs), cfg.Connections)
	results := p.runBatches(ctx, pool, jobs, cutoff, cfg)

	// 3. Advance watermarks to the last contiguous success per group.
	staged, advanced := p.advanceWatermarks(ctx, plans, results)

	st := pool.Stats()
	p.crawlJob.Log("crawl complete: %d group(s), %d batch(es), %d article(s) staged, %d group(s) advanced (conns %d/%d, resets %d)",
		len(plans), len(jobs), staged, advanced, st.Open, st.Target, st.Resets)
	p.crawlJob.SetIdle(p.nextCrawl())
	go p.runBuild(ctx) // assemble what just landed
	if staged == 0 {
		go p.idleHealthCheck(ctx)
	}
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
	pending, err := p.st.groupsNeedingBackfill(ctx, 1)
	if err != nil || len(pending) > 0 {
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
	err := pool.Do(ctx, func(c *nntp.Conn) error {
		_, l, h, err := c.Group(g.Name)
		if err != nil {
			return err
		}
		low, high = l, h
		return nil
	})
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
func (p *Plugin) runBatches(ctx context.Context, pool *nntp.Pool, jobs []batchJob, cutoff time.Time, cfg Config) []batchResult {
	workers := cfg.Connections
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	jobCh := make(chan batchJob)
	resCh := make(chan batchResult, len(jobs)) // buffered: workers never block on send

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				resCh <- p.fetchBatch(ctx, pool, j, cutoff)
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	close(resCh)

	out := make([]batchResult, 0, len(jobs))
	for r := range resCh {
		out = append(out, r)
	}
	return out
}

// fetchBatch pulls one overview range and stages it. The connection is returned
// to the pool before any parsing or database work.
func (p *Plugin) fetchBatch(ctx context.Context, pool *nntp.Pool, j batchJob, cutoff time.Time) batchResult {
	res := batchResult{group: j.group, lo: j.lo, hi: j.hi}
	if ctx.Err() != nil {
		return res
	}

	var ovs []nntp.MessageOverview
	err := pool.Do(ctx, func(c *nntp.Conn) error {
		// The pool hands out whichever connection is free and another caller may
		// have selected a different group on it, so always re-select.
		if _, _, _, err := c.Group(j.group); err != nil {
			return err
		}
		got, _, err := c.Overview(j.lo, j.hi)
		if err != nil {
			return err
		}
		ovs = got
		return nil
	})
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/crawl-fetch",
			fmt.Errorf("%s %d-%d: %w", j.group, j.lo, j.hi, err))
		return res // ok stays false — the watermark will not pass this range
	}
	res.maxDate = newestDate(ovs)
	res.minDate = oldestDate(ovs)

	arts := parseOverviews(ovs, j.group, cutoff)
	if len(arts) > 0 {
		n, err := p.staging.stageArticles(ctx, arts)
		if err != nil {
			// Leave ok=false so the watermark does NOT move past articles we never
			// stored. (Prod drops the batch on a staging error but keeps the
			// already-advanced watermark, losing those articles permanently.)
			p.core.Errors.Report(ctx, "usenet/crawl-stage",
				fmt.Errorf("%s %d-%d: %w", j.group, j.lo, j.hi, err))
			return res
		}
		res.staged = n
	}
	res.ok = true
	return res
}

// advanceWatermarks moves each group's high watermark to the end of its last
// CONTIGUOUS run of successful batches. A failure in the middle stops the
// advance there, so the failed range is refetched next pass instead of being
// silently skipped — with parallel batches, "highest success" would strand gaps.
func (p *Plugin) advanceWatermarks(ctx context.Context, plans map[string]*crawlPlan, results []batchResult) (staged, advanced int) {
	byGroup := make(map[string][]batchResult, len(plans))
	for _, r := range results {
		staged += r.staged
		byGroup[r.group] = append(byGroup[r.group], r)
	}

	for name, rs := range byGroup {
		plan := plans[name]
		if plan == nil {
			continue
		}
		for _, r := range rs {
			if !r.ok {
				continue
			}
			if err := p.st.recordFetchedRange(ctx, name, int64(r.lo), int64(r.hi)); err != nil {
				p.core.Errors.Report(ctx, "usenet/crawl-range-record", err)
			}
		}
		highest, latest := contiguousEnd(plan.start, rs)

		var watermark int64
		if highest >= plan.start {
			watermark = int64(highest)
			advanced++
		} else {
			p.crawlJob.Log("%s: no contiguous progress this pass — retrying from %d", name, plan.start)
		}
		if err := p.st.updateGroupState(ctx, name, int64(plan.low), int64(plan.high),
			watermark, int64(plan.start), latest); err != nil {
			p.core.Errors.Report(ctx, "usenet/crawl-watermark", fmt.Errorf("%s: %w", name, err))
		}
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
func parseOverviews(ovs []nntp.MessageOverview, group string, cutoff time.Time) []stagedArticle {
	out := make([]stagedArticle, 0, len(ovs))
	for _, ov := range ovs {
		if ov.MessageId == "" {
			continue
		}
		if !ov.Date.IsZero() && ov.Date.Before(cutoff) {
			continue
		}
		base, pn, tp, seg, fn, tf, fp := parseSubject(ov.Subject)
		if isJunkTitle(base) {
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

// updateGroupState records the server's bounds and, when watermark > 0, advances
// high_watermark. The watermark is passed separately from serverHigh because a
// pass may only complete part of its window (see advanceWatermarks); GREATEST
// keeps it monotonic, and backSeed initialises back_watermark on the first crawl
// so the backfill knows where history begins.
func (s *PGStore) updateGroupState(ctx context.Context, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var hw sql.NullTime
		if !hwDate.IsZero() {
			hw = sql.NullTime{Time: hwDate, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			   SET high_watermark = GREATEST(high_watermark, $2),
			       server_low = $3, server_high = $4, last_crawl = now(),
			       back_watermark = COALESCE(back_watermark, $5),
			       high_watermark_date = COALESCE($6, high_watermark_date)
			 WHERE name = $1`, name, watermark, serverLow, serverHigh, backSeed, hw)
		return err
	})
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
