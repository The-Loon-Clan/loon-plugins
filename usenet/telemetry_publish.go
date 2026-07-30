package usenet

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Cross-process telemetry.
//
// The pass trackers and the error ring live in WORKER memory, but the admin
// page and status.json are served by the WEB process. On a split deployment
// the web side would render an eternally-idle crawler whatever the worker was
// doing. So the worker publishes this snapshot into the shared settings table
// on a slow tick, and every process that does not run the jobs reads it back
// wherever it would have read local telemetry.

const telemetrySettingKey = "worker_telemetry"

// triggerRequestKey relays the dashboard's Crawl/Backfill-now buttons across
// the process split: the buttons post to the WEB process, whose trigger
// func-vars are nil (jobs live in the worker), so the click was a silent
// no-op. The web action writes "kind:unix" here; the worker's telemetry tick
// consumes it below.
const triggerRequestKey = "trigger_request"

// fireTrigger starts the job a relay kind names. One switch shared by the
// direct path (actionTrigger in the process that runs the jobs) and the
// cross-process relay below, so every job the Jobs tab can trigger stays in
// one place. Returns false for an unknown kind.
func (p *Plugin) fireTrigger(kind string) bool {
	switch kind {
	case "crawl":
		go p.runCrawl(p.ctx)
	case "backfill":
		go p.runBackfill(p.ctx)
	case "build":
		go p.runBuild(p.ctx)
	case "tagfill":
		go p.runTagFill(p.ctx)
	case "prune":
		go p.runPrune(p.ctx)
	case "health":
		go p.runHealthCheck(p.ctx)
	default:
		return false
	}
	return true
}

// fireTriggerRequest runs one relayed trigger. The freshness window guards a
// worker that restarts with a stale request still in the table — an operator's
// click should either happen promptly or not at all, never surprise-fire an
// hour later.
func (p *Plugin) fireTriggerRequest(req string) {
	kind, tsStr, ok := strings.Cut(req, ":")
	if !ok {
		return
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > 2*time.Minute {
		return
	}
	if p.fireTrigger(kind) {
		if j := p.jobFor(kind); j != nil {
			j.Log("%s trigger relayed from the web process", kind)
		}
	}
}

// jobFor maps a trigger kind to its job, so relay logs land in the pane the
// operator is actually watching.
func (p *Plugin) jobFor(kind string) core.Job {
	switch kind {
	case "crawl":
		return p.crawlJob
	case "backfill":
		return p.backfillJob
	case "build":
		return p.buildJob
	case "tagfill":
		return p.tagJob
	case "prune":
		return p.pruneJob
	case "health":
		return p.healthJob
	}
	return nil
}

// workerTelemetry is the process-local half of the crawler's status —
// everything the store cannot answer. Store-backed numbers (group stats,
// builder depth, providers, workers) are NOT here; they are already shared.
type workerTelemetry struct {
	UpdatedAt    time.Time      `json:"updated_at"`
	CrawlCur     passStats      `json:"crawl_current"`
	CrawlLast    passStats      `json:"crawl_last"`
	BackfillCur  passStats      `json:"backfill_current"`
	BackfillLast passStats      `json:"backfill_last"`
	BackfillRate float64        `json:"backfill_rate"`
	Errors       []crawlError   `json:"errors"`
	Built        []builtRelease `json:"built"`
	// Fleet is the per-provider connection-pool state (keyed by provider id).
	// Dial health lives in the worker's in-memory providerFleet; publishing it
	// is what lets the web page show open/target/resets instead of an eternal
	// "not dialled yet".
	Fleet map[int]providerStat `json:"fleet,omitempty"`
	// Jobs is the worker's own job snapshots (status + last activity). The
	// jobs register only in the worker process, so without this the web
	// page's Activity table cannot say whether anything is running.
	Jobs []crawlerJobVM `json:"jobs,omitempty"`
	// Pending is the incomplete-sets sample the build pass takes — the
	// "which releases are still missing articles" readout.
	Pending []pendingSet `json:"pending,omitempty"`
	// Evicted counts hopeless sets shed by redis staging since worker start —
	// the answer to "is eviction failing or filtering".
	Evicted int64 `json:"evicted,omitempty"`
	// Census is the last few staging-health samples, newest first. Published
	// because the readings that matter are DELTAS between build passes, and a
	// dashboard showing one instant cannot express "evictions are climbing" —
	// which is the difference between a crawler that is filtering and one that
	// is destroying completed work.
	Census []censusRow `json:"census,omitempty"`
	// Schema is the newest plugin migration this binary carries. app_versions
	// reports the SITE's git SHA, so a plugin-only deploy is invisible there;
	// this is the marker that says which plugin code is actually running.
	Schema string `json:"schema,omitempty"`
	// CrawlStalledPasses is the crawl's consecutive zero-progress streak
	// against a large backlog; the third also raises an error report. (Duty
	// percentages ride inside Jobs — one carrier, so the pair cannot drift.)
	CrawlStalledPasses int `json:"crawl_stalled_passes,omitempty"`

	// Stale is set by the READ side (telemetryView) when the published
	// snapshot's heartbeat has lapsed: the worker is gone or wedged, and
	// whatever this snapshot says about in-progress passes is history, not
	// state. Never serialized — it is an assessment of the payload, not part
	// of it.
	Stale bool `json:"-"`
}

// pickPass prefers the running pass, falling back to the last completed one,
// so a status read between passes still reports what happened.
func pickPass(cur, last passStats) passStats {
	if cur.InProgress {
		return cur
	}
	return last
}

// localTelemetry snapshots this process's own trackers.
func (p *Plugin) localTelemetry() workerTelemetry {
	tv := workerTelemetry{UpdatedAt: time.Now()}
	if p.tel == nil {
		return tv
	}
	tv.CrawlCur, tv.CrawlLast = p.tel.crawl.snapshot()
	tv.BackfillCur, tv.BackfillLast = p.tel.backfill.snapshot()
	tv.BackfillRate = p.tel.backfill.rate()
	tv.Errors = p.tel.recentErrors()
	tv.Built = p.tel.recentBuilt()
	if p.fleet != nil {
		if stats := p.fleet.snapshotStats(time.Now()); len(stats) > 0 {
			tv.Fleet = stats
		}
	}
	tv.Jobs, _ = p.jobVMs()
	tv.Pending = p.tel.pendingSets()
	tv.Evicted = p.tel.evictedCount()
	tv.Schema = newestMigration()
	tv.CrawlStalledPasses = p.tel.stalled()
	if p.st != nil {
		// Cheap: an indexed LIMIT read of a table with one row per build pass.
		if rows, err := p.st.stagingCensusRows(context.Background(), 12); err == nil {
			tv.Census = rows
		}
	}
	return tv
}

// telemetryStaleAfter is how old a published snapshot may be before the read
// side declares the worker gone. Three missed heartbeats: the publisher
// force-writes at least every telemetryHeartbeat even when nothing changed,
// so a fresh UpdatedAt is a liveness signal, not just a change marker.
const (
	telemetryHeartbeat  = time.Minute
	telemetryStaleAfter = 3 * telemetryHeartbeat
)

// telemetryView returns the telemetry the CURRENT process should render: its
// own when it runs the jobs (worker / all), the worker-published copy
// otherwise (web). A missing or unparsable published copy degrades to zero
// values — the page shows an idle crawler, exactly what it showed before
// publishing existed.
//
// A published copy whose heartbeat has LAPSED is a special case that used to
// lie: a worker that died mid-pass leaves CrawlCur.InProgress=true behind,
// passStats.Duration() substitutes time.Now() for running passes, and the
// dashboard rendered a "crawl in progress" whose duration climbed forever —
// a dead worker indistinguishable from a busy one. The flags are cleared and
// Stale is set so every consumer renders what is actually known: the worker
// has not reported since UpdatedAt.
func (p *Plugin) telemetryView(ctx context.Context) workerTelemetry {
	if p.runsJobs {
		return p.localTelemetry()
	}
	var tv workerTelemetry
	s, err := p.st.getSettings(ctx)
	if err != nil {
		return tv
	}
	if raw := s[telemetrySettingKey]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &tv)
	}
	tv.markStaleIfLapsed(time.Now())
	return tv
}

// markStaleIfLapsed downgrades a snapshot whose heartbeat has lapsed: the
// worker is gone, so its in-progress claims are history. Separated from
// telemetryView so the rule is testable without a settings store.
func (tv *workerTelemetry) markStaleIfLapsed(now time.Time) {
	if tv.UpdatedAt.IsZero() || now.Sub(tv.UpdatedAt) <= telemetryStaleAfter {
		return
	}
	tv.Stale = true
	tv.CrawlCur.InProgress = false
	tv.BackfillCur.InProgress = false
	for i := range tv.Jobs {
		tv.Jobs[i].Running = false
	}
}

// publishTelemetry ships the local snapshot every few seconds. Identical
// consecutive payloads are not rewritten, so an idle crawler costs zero
// writes; during a pass the moving counters make each tick one small settings
// UPDATE — which is what lets the web page's status poll show a crawl
// mid-flight.
func (p *Plugin) publishTelemetry(ctx context.Context) {
	var last, lastTrig string
	var lastWrite time.Time
	var readFailed bool
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Consume any relayed trigger request first, so a click on the web
			// dashboard starts the pass within one tick. A read failure is
			// reported ONCE per streak — the web flash promises "starts within
			// ~5s", so silently never consuming the request breaks a promise.
			if s, err := p.st.getSettings(ctx); err == nil {
				readFailed = false
				if req := s[triggerRequestKey]; req != "" && req != lastTrig {
					lastTrig = req
					p.fireTriggerRequest(req)
				}
			} else if !readFailed {
				readFailed = true
				p.reportErr(ctx, "usenet/trigger-relay-read", err)
			}
			tv := p.localTelemetry()
			tv.UpdatedAt = time.Time{} // stamp excluded from the change check
			b, err := json.Marshal(tv)
			if err != nil {
				continue
			}
			// The heartbeat half of the change check: an unchanged payload is
			// normally not rewritten (an idle crawler costs zero writes), but
			// UpdatedAt then froze for as long as the worker idled — making a
			// stale-snapshot guard on the read side impossible, because a
			// healthy-idle worker and a dead one looked identical. Forcing one
			// small write per telemetryHeartbeat makes UpdatedAt a liveness
			// signal at the cost of one settings UPDATE a minute.
			if s := string(b); s != last || time.Since(lastWrite) >= telemetryHeartbeat {
				last = s
				tv.UpdatedAt = time.Now()
				lastWrite = tv.UpdatedAt
				stamped, _ := json.Marshal(tv)
				if err := p.st.setSetting(ctx, telemetrySettingKey, string(stamped)); err != nil {
					p.reportErr(ctx, "usenet/telemetry-publish", err)
				}
			}
		}
	}
}

// Span is how many article numbers this set covers on the server, or 0 when
// the bounds are unknown (sets staged before span tracking existed).
func (p pendingSet) Span() int {
	if p.ArtLo <= 0 || p.ArtHi < p.ArtLo {
		return 0
	}
	return p.ArtHi - p.ArtLo + 1
}

// spanCollisionMin is the article span past which a set cannot be one upload.
//
// ABSOLUTE, not a ratio of articles held. The first version scaled with Have and
// was wrong in the worst possible way: a set early in its arrival holds very few
// articles while already spanning a legitimate range, so every release got
// flagged during exactly the window an operator is watching it. A test with a
// genuine four-article release caught it.
//
// A busy group takes one to two million articles a day, so a million article
// numbers is on the order of half a day of posting — far beyond any single
// upload run, while an ordinary release with other posters interleaved sits
// three or four orders of magnitude below it. That gap is what makes an absolute
// boundary safe here.
//
// A constant rather than a knob because it classifies a diagnostic rather than
// tuning behaviour: nothing schedules, drops or retries on it. If it proves
// noisy on a slower group it should become one.
const spanCollisionMin = 1_000_000

// Collided reports a set whose articles are spread far too wide to be a single
// upload — the shape of a base subject generic enough to have merged unrelated
// posts. Such a set is unbuildable by construction rather than merely
// unfinished: it waits on files belonging to somebody else's upload.
func (p pendingSet) Collided() bool {
	return p.Span() >= spanCollisionMin && p.Have > 0
}
