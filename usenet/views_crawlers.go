package usenet

import (
	"context"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Crawlers status view: coverage, per-backbone progress, fleet, workers, health,
// recent activity, and the jobs widget.

// passVM is the last (or in-flight) crawl pass, pre-formatted. This answers the
// three questions an operator actually has when a crawl looks wrong: is it
// moving, how fast, and is anything failing.
type passVM struct {
	Running                  bool
	Any                      bool
	Groups, Batches, Failed  int
	GroupsDone, BatchesTotal int
	// PassBatches/PassBatchesTotal accumulate across the whole pass. The
	// round pair above feeds the live bar; these feed the completed-pass
	// readout — every catch-up pass ends on an empty round, so a card built
	// from the round pair read "0 batches" beside millions of articles.
	PassBatches, PassBatchesTotal int
	Round                         int // catch-up round, 1-based; 0 = none started
	Pct                           int // completed/planned batches, 0-100
	Reading                       string
	Articles, Staged              int
	Wire, Duration, Rate, Through string
	Providers                     int
}

// cellLevels quantises fill fractions into four steps so the bar renders with
// CSS classes instead of a per-cell inline style (which a strict CSP blocks).
// Any coverage at all shows as at least level 1: a slice holding one fetched
// batch out of thousands of articles is still meaningfully different from an
// untouched one, and rounding it to empty would hide exactly the sparse tail
// backfill is working through.
func cellLevels(cells []float64) []int {
	out := make([]int, len(cells))
	for i, f := range cells {
		switch {
		case f >= 0.999:
			out[i] = 3
		case f >= 0.5:
			out[i] = 2
		case f > 0:
			out[i] = 1
		}
	}
	return out
}

// coveredFraction averages the per-cell fills back into one number. Averaging
// the CELLS rather than re-summing the ranges keeps the headline and the strip
// telling the same story: if rounding puts a sliver of coverage into a cell,
// the percentage counts exactly what the operator can see.
func coveredFraction(cells []float64) float64 {
	if len(cells) == 0 {
		return 0
	}
	var sum float64
	for _, c := range cells {
		sum += c
	}
	return sum / float64(len(cells))
}

// fmtPct renders a coverage percentage without pretending to precision it does
// not have, while never rounding a real sliver away to a flat "0%" — the
// difference between "we have fetched a little of this group" and "we have
// fetched none of it" is the whole point of the strip.
func fmtPct(p float64) string {
	switch {
	case p <= 0:
		return "0%"
	case p < 1:
		return "<1%"
	case p >= 99.5:
		return "100%"
	default:
		return strconv.Itoa(int(p+0.5)) + "%"
	}
}

// statsVM formats one pass snapshot for the template. Works on the VALUE, not
// the tracker, so the web process can render the worker-published copy
// (telemetry_publish.go) through the same code path.
func statsVM(st passStats) passVM {
	if st.Started.IsZero() {
		return passVM{}
	}
	vm := passVM{
		Running: st.InProgress, Any: true,
		Groups: st.Groups, Batches: st.Batches, Failed: st.Failed,
		GroupsDone: st.GroupsDone, BatchesTotal: st.BatchesTotal,
		PassBatches: st.PassBatches, PassBatchesTotal: st.PassBatchesTotal,
		Reading:  st.Reading,
		Articles: st.Articles, Staged: st.Staged, Providers: st.Providers,
		Wire:     fmtBytes(st.WireBytes),
		Duration: st.Duration().Truncate(time.Second).String(),
		Rate:     fmt.Sprintf("%.0f art/s", st.Rate()),
		Through:  fmt.Sprintf("%.2f MB/s", st.Throughput()),
	}
	vm.Round = st.Round
	if st.BatchesTotal > 0 {
		// Clamped: a batch can complete after its round's denominator was
		// replaced, and a progress bar wider than its track is a rendering bug
		// on top of a counting one.
		vm.Pct = st.Batches * 100 / st.BatchesTotal
		if vm.Pct > 100 {
			vm.Pct = 100
		}
	}
	return vm
}

// errorVM is one recent failure, newest first.
type errorVM struct {
	When, Op, Msg string
}

func errorVMs(errs []crawlError) []errorVM {
	out := make([]errorVM, len(errs))
	for i, e := range errs {
		out[i] = errorVM{When: e.At.Format("15:04:05"), Op: e.Op, Msg: e.Msg}
	}
	return out
}

// providerVM is one provider row on the crawlers page: what it is, and whether
// its connections are actually working. Prod has no equivalent — it only ever
// had a fixed primary/secondary pair.
type providerVM struct {
	ID                         int
	Name, Host, Backbone, Role string
	Enabled, Down, Dialled     bool
	Open, Target, Busy         int
	// Configured is the per-worker pool size saved on the row (0 = default).
	// Shown next to the LIVE target when they differ: a pool only re-dials at
	// the next pass, and "I changed it but it still says 10" is exactly the
	// confusion that caused.
	Configured int
	Resets     int64
}

// fleetVMs renders the provider fleet. Dial stats come from the local fleet on
// the process that runs the jobs, and from the worker-PUBLISHED telemetry
// everywhere else — without the merge the web page showed every provider as
// "not dialled yet" forever on a split deployment.
func (p *Plugin) fleetVMs(ctx context.Context, published map[int]providerStat) []providerVM {
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/fleet-view", err)
		return nil
	}
	stats := map[int]providerStat{}
	if p.runsJobs && p.fleet != nil {
		stats = p.fleet.snapshotStats(time.Now())
	} else if published != nil {
		stats = published
	}
	out := make([]providerVM, len(servers))
	for i, sv := range servers {
		vm := providerVM{
			ID:   sv.ID,
			Name: sv.label(), Host: sv.addr(), Backbone: sv.backboneKey(),
			Role: sv.Role, Enabled: sv.Enabled, Configured: sv.Connections,
		}
		if st, ok := stats[sv.ID]; ok {
			vm.Dialled = true
			vm.Open, vm.Target, vm.Busy = st.Open, st.Target, st.Busy
			vm.Resets, vm.Down = st.Resets, st.Down
		}
		out[i] = vm
	}
	return out
}

// workerVM is one crawler host. This is the view that makes a multi-host setup
// debuggable: who is alive, and how much of the work each one currently holds.
type workerVM struct {
	ID     string
	Me     bool
	Groups int
}

func (p *Plugin) workerVMs(ctx context.Context) []workerVM {
	cfg := p.effective(ctx)
	stale := time.Duration(cfg.WorkerStaleSec) * time.Second
	if stale <= 0 {
		stale = 90 * time.Second
	}
	term := time.Duration(cfg.AssignTermMin) * time.Minute
	// Everyone alive, not just this term's members — a worker waiting out the
	// term is exactly what an operator wants to see after adding a host.
	workers, err := p.st.eligibleWorkers(ctx, time.Now().Add(term), stale)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/worker-view", err)
		return nil
	}
	held, err := p.st.leaseHolders(ctx, leaseScopeGroup)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/lease-view", err)
	}
	counts := map[string]int{}
	for _, owner := range held {
		counts[owner]++
	}
	me := workerID()
	out := make([]workerVM, len(workers))
	for i, w := range workers {
		out[i] = workerVM{ID: w, Me: w == me, Groups: counts[w]}
	}
	return out
}

// healthVM is the healthy/broken/dead/unknown split, the one prod card worth
// copying wholesale — it answers "is the archive still downloadable" at a glance.
type healthVM struct {
	Healthy, Broken, Dead, Unknown, Total int
	HealthyPct, BrokenPct, DeadPct        int
}

// healthVM builds the health census. cs is the host's cached catalog stats
// when sink=host (the verdicts the health job writes live in the HOST's
// domain, so the plugin's own table would answer zeros there); nil means
// internal mode — count our own table — or a host that skipped the optional
// capability, which degrades to an absent card.
func (p *Plugin) healthVM(ctx context.Context, cs *pluginapi.CatalogStats) healthVM {
	var vm healthVM
	if p.cfg.Sink == SinkHost {
		if cs != nil {
			vm = healthVM{
				Healthy: int(cs.Health.Healthy), Broken: int(cs.Health.Broken),
				Dead: int(cs.Health.Dead), Unknown: int(cs.Health.Untested),
			}
		}
	} else {
		counts, err := p.st.healthBreakdown(ctx)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/health-view", err)
			return healthVM{}
		}
		vm = healthVM{
			Healthy: counts[healthHealthy], Broken: counts[healthBroken],
			Dead: counts[healthDead], Unknown: counts[healthUnknown],
		}
	}
	vm.Total = vm.Healthy + vm.Broken + vm.Dead + vm.Unknown
	if vm.Total > 0 {
		vm.HealthyPct = vm.Healthy * 100 / vm.Total
		vm.BrokenPct = vm.Broken * 100 / vm.Total
		vm.DeadPct = vm.Dead * 100 / vm.Total
	}
	return vm
}

// indexStatsVM is the Index Stats card: the catalogue and staging at a glance.
type indexStatsVM struct {
	GroupsActive, GroupsTotal int
	Releases                  int64
	TotalSize                 string
	HaveCatalog               bool // capability present (host) or own table (internal)
	CatalogCached             bool // host mode: numbers are the host's hourly cache
	Staging                   stagingInfo
	MemUsed, MemMax           string
	Evicted                   int64 // hopeless sets shed since worker start (redis mode)
}

// indexStatsWithTel augments the store/staging numbers with the ones only the
// worker's telemetry knows.
func (p *Plugin) indexStatsWithTel(ctx context.Context, activeGroups int, cs *pluginapi.CatalogStats, tv workerTelemetry) indexStatsVM {
	vm := p.indexStatsVM(ctx, activeGroups, cs)
	vm.Evicted = tv.Evicted
	return vm
}

func (p *Plugin) indexStatsVM(ctx context.Context, activeGroups int, cs *pluginapi.CatalogStats) indexStatsVM {
	vm := indexStatsVM{GroupsActive: activeGroups}
	if total, err := p.st.groupCount(ctx); err == nil {
		vm.GroupsTotal = total
	}
	if p.cfg.Sink == SinkHost {
		if cs != nil {
			vm.HaveCatalog, vm.CatalogCached = true, true
			vm.Releases, vm.TotalSize = cs.Releases, fmtBytes(cs.TotalSizeBytes)
		}
	} else if count, size, err := p.st.catalogTotals(ctx); err == nil {
		vm.HaveCatalog = true
		vm.Releases, vm.TotalSize = count, fmtBytes(size)
	} else {
		p.core.Errors.Report(ctx, "usenet/catalog-totals", err)
	}
	if si, err := p.staging.stagingInfo(ctx); err == nil {
		vm.Staging = si
		vm.MemUsed, vm.MemMax = fmtBytes(si.MemUsedBytes), fmtBytes(si.MemMaxBytes)
	} else {
		p.reportErr(ctx, "usenet/staging-info", err)
	}
	return vm
}

type crawlerGroupVM struct {
	Name         string
	NZBs, Staged int
	Cover        pluginapi.CoverageBar
	Cells        []int // per-slice fill level 0..3, drawn over the coverage bar
	Fragments    int   // contiguous fetched runs; >1 means backfill left holes
	// CoveredFmt is the share of the server's span we have actually fetched,
	// as a whole-number percent. This is the honest headline: the watermark
	// bar reads solid green whenever the two bookmarks meet, which they do
	// even when nothing was ingested between them.
	CoveredFmt string
	// NoCoverage marks a group with no fetched ranges recorded at all, so the
	// strip can say so in words instead of rendering an empty track that
	// looks identical to "fetched nothing yet" and to "never asked".
	NoCoverage bool
	// The legacy-format coverage line: "↩ back-at · ↑ fwd-at (+N new)".
	FwdAt        string // forward watermark, date+time
	BackAt       string // backfill watermark, date+time
	NewFmt       string // articles on the server past the forward watermark, comma-formatted; "" = none
	RemainingFmt string // backfill articles left, comma-formatted; "" = none/done
	BackfillDone bool
	LastCrawl    string
}

// backboneVM groups one backbone's rows. Coverage is per backbone, so the page
// cannot show a single merged table without inventing numbers.
type backboneVM struct {
	Name   string // "" before the first crawl, rendered as "not yet crawled"
	Groups []crawlerGroupVM
}

type crawlerJobVM struct {
	Name     string
	Status   string
	Activity string
	Next     string // next scheduled run (HH:MM:SS) — answers "when will it run"
	Running  bool
	// Duty is the job's trailing-hour busy percentage (duty.go) — the number
	// that separates "runs on schedule and works" from "runs on schedule and
	// achieves a 6% duty cycle", which three incidents shipped as. Filled on
	// the worker; rides the telemetry publish to the web process.
	Duty float64 `json:"duty"`
	// Logs is the recent job-log tail — the Jobs tab's live logging. Capped
	// (jobLogTail) because it rides the worker-telemetry publish.
	Logs []string
}

// DutyLabel renders the duty percentage, hiding the meaningless zero of a
// worker that has not completed a window yet.
func (j crawlerJobVM) DutyLabel() string {
	if j.Duty <= 0 {
		return ""
	}
	if j.Duty < 1 {
		return "<1% busy (1h)"
	}
	return fmt.Sprintf("%.0f%% busy (1h)", j.Duty)
}

// jobLogTail bounds how many log lines each job publishes cross-process.
const jobLogTail = 25

func (p *Plugin) renderCrawlers(ctx context.Context, msg, errMsg string) (template.HTML, error) {
	stats, err := p.st.stats(ctx)
	if err != nil {
		return "", err
	}
	// Best-effort: coverage detail is decoration over the watermark bar, and a
	// ranges query failing should not blank the whole status page.
	ranges, rerr := p.st.allCoveredRanges(ctx)
	if rerr != nil {
		p.reportErr(ctx, "usenet/coverage-ranges", rerr)
	}

	var backbones []backboneVM
	byBackbone := map[string]int{}
	for _, g := range stats.Groups {
		vm := crawlerGroupVM{
			Name: g.Name, NZBs: g.NZBs, Staged: g.Staged,
			Cover: g.Coverage(), BackfillDone: g.BackfillDone,
			FwdAt: fmtDateTime(g.HighWatermarkDate), BackAt: fmtDateTime(g.BackWatermarkDate),
			LastCrawl: fmtTime(g.LastCrawl),
		}
		// "+N new" — what the server holds past our forward watermark; the
		// number the operator watches to see the next pass's workload.
		if g.HighWatermark > 0 && g.ServerHigh > g.HighWatermark {
			vm.NewFmt = fmtComma(g.ServerHigh - g.HighWatermark)
		}
		if !g.BackfillDone && g.BackWatermark > g.ServerLow {
			vm.RemainingFmt = fmtComma(g.BackWatermark - g.ServerLow)
		}
		if runs := ranges[coverKey{g.Backbone, g.Name}]; len(runs) > 0 {
			cells := coverageCells(runs, g.ServerLow, g.ServerHigh, coverCellCount)
			vm.Fragments = len(runs)
			vm.Cells = cellLevels(cells)
			vm.CoveredFmt = fmtPct(coveredFraction(cells) * 100)
		} else {
			vm.NoCoverage = true
		}
		idx, ok := byBackbone[g.Backbone]
		if !ok {
			idx = len(backbones)
			byBackbone[g.Backbone] = idx
			backbones = append(backbones, backboneVM{Name: g.Backbone})
		}
		backbones[idx].Groups = append(backbones[idx].Groups, vm)
	}
	// Flat list too: the summary cards and any single-backbone install read this.
	var groups []crawlerGroupVM
	for _, b := range backbones {
		groups = append(groups, b.Groups...)
	}
	// The merged telemetry (local on the worker, worker-published on web) feeds
	// the pass card, the error tail, the recently-built list, the job table,
	// the incomplete-sets sample and the backfill rate — the numbers no shared
	// table can answer in every mode.
	tv := p.telemetryView(ctx)
	jobs, running := p.jobVMs()
	if !p.runsJobs {
		// The web registry can only hold HOST jobs that happen to share a
		// name (a web-side "NZB Tag Fill" once masked the worker's whole job
		// set here, showing "idle" mid-crawl). The worker's published
		// snapshots are the only truth on this process — use them
		// unconditionally.
		jobs, running = tv.Jobs, false
		for _, j := range jobs {
			if j.Running {
				running = true
			}
		}
	}
	eta := ""
	if d, ok := backfillETA(stats.TotalBackfillRemaining, tv.BackfillRate); ok {
		eta = fmtETA(d)
	}
	recentNzbs := make([]recentNZBVM, len(tv.Built))
	for i, b := range tv.Built {
		recentNzbs[i] = recentNZBVM{
			Title: b.Title, Group: b.Group,
			Size: fmtBytes(b.Size), Created: fmtTime(b.At),
		}
	}
	// Catalog-stats fetch feeds Index Stats in host mode (internal mode reads
	// the plugin's own tables inside the VM). Per-job status, logs, and the
	// Builder/health panels moved to the Jobs tab (views_jobs.go).
	var cs *pluginapi.CatalogStats
	if p.cfg.Sink == SinkHost {
		if prov, ok := pluginapi.LookupCatalogStats(p.core); ok {
			if v, err := prov.CatalogStats(ctx); err == nil {
				cs = &v
			} else {
				p.core.Errors.Report(ctx, "usenet/catalog-stats", err)
			}
		}
	}
	return p.frag("crawlers.html", map[string]any{
		"Stats": stats, "Groups": groups, "Backbones": backbones,
		"Pass":        statsVM(pickPass(tv.CrawlCur, tv.CrawlLast)),
		"Backfill":    statsVM(pickPass(tv.BackfillCur, tv.BackfillLast)),
		"Errors":      errorVMs(tv.Errors),
		"BackfillETA": eta,
		"Fleet":       p.fleetVMs(ctx, tv.Fleet), "Workers": p.workerVMs(ctx),
		"IndexStats":  p.indexStatsWithTel(ctx, len(stats.Groups), cs, tv),
		"HostSink":    p.cfg.Sink == SinkHost,
		"RecentNzbs":  recentNzbs,
		"WorkerStale": tv.Stale, "WorkerLastSeen": fmtTime(tv.UpdatedAt),
		"AutoRefresh": running, "Msg": msg, "Err": errMsg,
	})
}

// recentNZBVM is the liveness readout: what the CRAWLER just built (from the
// telemetry ring, so it is crawler-only in every sink mode). Watermarks barely
// move over one pass, so this is what actually shows the crawler is alive.
type recentNZBVM struct {
	Title, Group, Size, Created string
}

// jobVMs snapshots this plugin's own jobs so the page shows what each is doing.
func (p *Plugin) jobVMs() (jobs []crawlerJobVM, anyRunning bool) {
	// All plugin jobs carry the "Usenet " name prefix ON PURPOSE: the host
	// registers its own jobs in the same registry, and matching generic names
	// ("NZB Tag Fill") once picked up the HOST's job — the dashboard showed a
	// paused legacy job and a running host job as if they were ours.
	mine := map[string]bool{
		jobNameCrawl: true, jobNameBackfill: true,
		jobNameBuild: true, jobNameTagFill: true,
		jobNamePrune: true, jobNameHealth: true,
	}
	for _, s := range schedule.GetAllSnapshots() {
		if !mine[s.Name] {
			continue
		}
		j := crawlerJobVM{Name: s.Name, Status: s.Status}
		if !s.NextRun.IsZero() {
			j.Next = s.NextRun.Format("15:04:05")
		}
		if s.LastError != "" {
			j.Activity = s.LastError
		} else if len(s.Logs) > 0 {
			j.Activity = s.Logs[len(s.Logs)-1]
		}
		if n := len(s.Logs); n > 0 {
			start := n - jobLogTail
			if start < 0 {
				start = 0
			}
			j.Logs = s.Logs[start:]
		}
		if s.Status == "running" || s.ElapsedSecs > 0 {
			j.Running, anyRunning = true, true
		}
		if p.duty != nil {
			j.Duty = p.duty.dutyPct(s.Name, dutySpan, time.Now())
		}
		jobs = append(jobs, j)
	}
	return jobs, anyRunning
}

func (p *Plugin) renderJobsWidget(ctx context.Context) (template.HTML, error) {
	stats, err := p.st.stats(ctx)
	if err != nil {
		return "", err
	}
	jobs, _ := p.jobVMs()
	var crawlJobs []crawlerJobVM
	for _, j := range jobs {
		if strings.HasPrefix(j.Name, "Usenet") {
			crawlJobs = append(crawlJobs, j)
		}
	}
	return p.frag("jobswidget.html", map[string]any{
		"Jobs": crawlJobs, "Stats": stats,
	})
}
