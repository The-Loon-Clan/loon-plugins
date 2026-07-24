package usenet

import (
	"context"
	"encoding/json"
	"time"
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
	return tv
}

// telemetryView returns the telemetry the CURRENT process should render: its
// own when it runs the jobs (worker / all), the worker-published copy
// otherwise (web). A missing or unparsable published copy degrades to zero
// values — the page shows an idle crawler, exactly what it showed before
// publishing existed.
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
	return tv
}

// publishTelemetry ships the local snapshot every few seconds. Identical
// consecutive payloads are not rewritten, so an idle crawler costs zero
// writes; during a pass the moving counters make each tick one small settings
// UPDATE — which is what lets the web page's status poll show a crawl
// mid-flight.
func (p *Plugin) publishTelemetry(ctx context.Context) {
	var last string
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			tv := p.localTelemetry()
			tv.UpdatedAt = time.Time{} // stamp excluded from the change check
			b, err := json.Marshal(tv)
			if err != nil {
				continue
			}
			if s := string(b); s != last {
				last = s
				tv.UpdatedAt = time.Now()
				stamped, _ := json.Marshal(tv)
				if err := p.st.setSetting(ctx, telemetrySettingKey, string(stamped)); err != nil {
					p.reportErr(ctx, "usenet/telemetry-publish", err)
				}
			}
		}
	}
}
