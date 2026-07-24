package usenet

import (
	"context"
	"fmt"
	"html/template"
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
	Running                       bool
	Any                           bool
	Groups, Batches, Failed       int
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

// statsVM formats one pass snapshot for the template. Works on the VALUE, not
// the tracker, so the web process can render the worker-published copy
// (telemetry_publish.go) through the same code path.
func statsVM(st passStats) passVM {
	if st.Started.IsZero() {
		return passVM{}
	}
	return passVM{
		Running: st.InProgress, Any: true,
		Groups: st.Groups, Batches: st.Batches, Failed: st.Failed,
		Articles: st.Articles, Staged: st.Staged, Providers: st.Providers,
		Wire:     fmtBytes(st.WireBytes),
		Duration: st.Duration().Truncate(time.Second).String(),
		Rate:     fmt.Sprintf("%.0f art/s", st.Rate()),
		Through:  fmt.Sprintf("%.2f MB/s", st.Throughput()),
	}
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
	Name, Host, Backbone, Role string
	Enabled, Down, Dialled     bool
	Open, Target               int
	Resets                     int64
}

func (p *Plugin) fleetVMs(ctx context.Context) []providerVM {
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/fleet-view", err)
		return nil
	}
	stats := map[int]providerStat{}
	if p.fleet != nil {
		stats = p.fleet.snapshotStats(time.Now())
	}
	out := make([]providerVM, len(servers))
	for i, sv := range servers {
		vm := providerVM{
			Name: sv.label(), Host: sv.addr(), Backbone: sv.backboneKey(),
			Role: sv.Role, Enabled: sv.Enabled,
		}
		if st, ok := stats[sv.ID]; ok {
			vm.Dialled = true
			vm.Open, vm.Target, vm.Resets, vm.Down = st.Open, st.Target, st.Resets, st.Down
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

func (p *Plugin) healthVM(ctx context.Context) healthVM {
	counts, err := p.st.healthBreakdown(ctx)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/health-view", err)
		return healthVM{}
	}
	vm := healthVM{
		Healthy: counts[healthHealthy], Broken: counts[healthBroken],
		Dead: counts[healthDead], Unknown: counts[healthUnknown],
	}
	vm.Total = vm.Healthy + vm.Broken + vm.Dead + vm.Unknown
	if vm.Total > 0 {
		vm.HealthyPct = vm.Healthy * 100 / vm.Total
		vm.BrokenPct = vm.Broken * 100 / vm.Total
		vm.DeadPct = vm.Dead * 100 / vm.Total
	}
	return vm
}

type crawlerGroupVM struct {
	Name         string
	NZBs, Staged int
	Cover        pluginapi.CoverageBar
	Cells        []int // per-slice fill level 0..3, drawn over the coverage bar
	Fragments    int   // contiguous fetched runs; >1 means backfill left holes
	FwdDate      string
	BackDate     string
	BackfillDone bool
	Remaining    int64
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
	Running  bool
}

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
			FwdDate: fmtDate(g.HighWatermarkDate), BackDate: fmtDate(g.BackWatermarkDate),
			LastCrawl: fmtTime(g.LastCrawl),
		}
		if !g.BackfillDone && g.BackWatermark > g.ServerLow {
			vm.Remaining = g.BackWatermark - g.ServerLow
		}
		if runs := ranges[coverKey{g.Backbone, g.Name}]; len(runs) > 0 {
			vm.Fragments = len(runs)
			vm.Cells = cellLevels(coverageCells(runs, g.ServerLow, g.ServerHigh, coverCellCount))
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
	jobs, running := p.jobVMs()
	builder, err := p.st.builderInfo(ctx, 15)
	if err != nil {
		return "", err
	}
	// The merged telemetry (local on the worker, worker-published on web) feeds
	// the pass card, the error tail and the backfill rate — the numbers the
	// store cannot answer.
	tv := p.telemetryView(ctx)
	eta := ""
	if d, ok := backfillETA(stats.TotalBackfillRemaining, tv.BackfillRate); ok {
		eta = fmtETA(d)
	}
	// Best-effort: recent activity is a liveness readout, and a failed query
	// there must not blank the status page it sits on.
	arts, aerr := p.st.recentArticles(ctx, 25)
	if aerr != nil {
		p.reportErr(ctx, "usenet/recent-articles", aerr)
	}
	nzbs, nerr := p.st.recentNZBs(ctx, 10)
	if nerr != nil {
		p.reportErr(ctx, "usenet/recent-nzbs", nerr)
	}
	recentArts := make([]recentArticleVM, len(arts))
	for i, a := range arts {
		recentArts[i] = recentArticleVM{
			Subject: a.Subject, Group: a.Group, Poster: a.Poster,
			Size: fmtBytes(a.Bytes), Posted: fmtTime(a.Posted),
		}
	}
	recentNzbs := make([]recentNZBVM, len(nzbs))
	for i, n := range nzbs {
		recentNzbs[i] = recentNZBVM{
			Title: n.Title, Group: n.Group,
			Size: fmtBytes(n.Size), Created: fmtTime(n.Created),
		}
	}
	return p.frag("crawlers.html", map[string]any{
		"Stats": stats, "Groups": groups, "Backbones": backbones,
		"Pass":        statsVM(pickPass(tv.CrawlCur, tv.CrawlLast)),
		"Backfill":    statsVM(pickPass(tv.BackfillCur, tv.BackfillLast)),
		"Errors":      errorVMs(tv.Errors),
		"BackfillETA": eta, "Jobs": jobs, "Builder": builder,
		"Fleet": p.fleetVMs(ctx), "Workers": p.workerVMs(ctx),
		"Health":         p.healthVM(ctx),
		"RecentArticles": recentArts, "RecentNzbs": recentNzbs,
		"AutoRefresh": running, "Msg": msg, "Err": errMsg,
	})
}

// recentArticleVM / recentNZBVM are the liveness readouts: what just got staged,
// and what just got built. Watermarks barely move over one pass, so these are
// what actually show the crawler is alive.
type recentArticleVM struct {
	Subject, Group, Poster, Size, Posted string
}

type recentNZBVM struct {
	Title, Group, Size, Created string
}

// jobVMs snapshots this plugin's own jobs so the page shows what each is doing.
func (p *Plugin) jobVMs() (jobs []crawlerJobVM, anyRunning bool) {
	mine := map[string]bool{
		"Usenet Crawler": true, "Usenet Backfill": true,
		"NZB Builder": true, "NZB Tag Fill": true, "NZB Prune": true,
	}
	for _, s := range schedule.GetAllSnapshots() {
		if !mine[s.Name] {
			continue
		}
		j := crawlerJobVM{Name: s.Name, Status: s.Status}
		if s.LastError != "" {
			j.Activity = s.LastError
		} else if len(s.Logs) > 0 {
			j.Activity = s.Logs[len(s.Logs)-1]
		}
		if s.Status == "running" || s.ElapsedSecs > 0 {
			j.Running, anyRunning = true, true
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
