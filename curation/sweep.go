package curation

import (
	"context"
	"fmt"
	"time"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

const batchSize = 500

// counters is one sweep's tally, keyed by rule plus the write outcomes.
type counters struct {
	scanned      int
	byRule       map[string]int
	wrote        int
	writeErrs    int
	factsMissing int
}

// runSweep walks every completed anime release whose season is still NULL and
// applies the rules. Stateless on purpose: unresolved rows are re-scanned on
// every run (the facts cache keeps that cheap — one metadata read per anime,
// not per release), which is what lets a newly linked TMDB id or a corrected
// entry name resolve yesterday's failures without any bookkeeping.
func (p *Plugin) runSweep(ctx context.Context) {
	if !p.runMu.TryLock() {
		return // manual trigger raced the scheduled loop; one sweep is plenty
	}
	defer p.runMu.Unlock()

	p.job.SetRunning()
	start := time.Now()
	c := counters{byRule: make(map[string]int)}
	facts := make(map[int]*AnimeFacts) // aid → facts (nil = known-missing)

	var afterID int64
	for {
		if ctx.Err() != nil {
			p.job.Log("interrupted after %d rows", c.scanned)
			break
		}
		rows, err := p.deps.ListSeasonNull(ctx, afterID, batchSize)
		if err != nil {
			p.job.SetError(fmt.Sprintf("worklist: %v", err))
			return
		}
		if len(rows) == 0 {
			break
		}
		afterID = rows[len(rows)-1].ID

		for _, r := range rows {
			c.scanned++
			s, e := p.deps.ParseSeasonEpisode(r.Title)
			var f *AnimeFacts
			if s == nil {
				var ok bool
				if f, ok = facts[r.AnimeID]; !ok {
					f, err = p.deps.AnimeFacts(ctx, r.AnimeID)
					if err != nil {
						p.job.Log("facts aid=%d: %v", r.AnimeID, err)
					}
					facts[r.AnimeID] = f
				}
				if f == nil {
					c.factsMissing++
				}
			}
			d := Decide(s, e, f)
			c.byRule[d.Rule]++
			if d.Season == nil && d.Episode == nil {
				continue
			}
			if err := p.deps.SetSeasonEpisode(ctx, r.ID, d.Season, d.Episode); err != nil {
				c.writeErrs++
				p.job.Log("write id=%d: %v", r.ID, err)
			} else {
				c.wrote++
			}
		}
		p.job.SetProgress("Scanned %d (filled %d, unresolved %d)",
			c.scanned, c.wrote, c.byRule[RuleUnresolved])
	}

	summary := fmt.Sprintf(
		"scanned %d: title %d, entry-name %d, single-season %d, non-seasonal %d, unresolved %d (no metadata %d), wrote %d, write errors %d — %s",
		c.scanned, c.byRule[RuleTitle], c.byRule[RuleMetaOrdinal],
		c.byRule[RuleMetaSingleSeason], c.byRule[RuleNonSeasonal],
		c.byRule[RuleUnresolved], c.factsMissing,
		c.wrote, c.writeErrs, time.Since(start).Round(time.Millisecond))
	p.job.Log("Done: %s", summary)
	p.notifyOps(c, summary)
	p.job.SetIdle(time.Now().Add(defaultInterval))
}

// notifyOps delivers the run summary to the operators' channel when a bridge
// publishes one. Skipped for all-quiet runs (nothing scanned) so a healthy
// steady state does not post a daily zero.
func (p *Plugin) notifyOps(c counters, summary string) {
	if c.scanned == 0 || p.core == nil {
		return
	}
	v, ok := p.core.Lookup(lpapi.OpsNotifierName)
	if !ok {
		return
	}
	n, ok := v.(lpapi.OpsNotifier)
	if !ok {
		return
	}
	n.NotifyOps("Season curation", summary)
}
