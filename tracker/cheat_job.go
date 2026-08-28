package tracker

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The cheat sweep: take a reading, compare it with the last one, record what
// looks impossible.
//
// Order matters and is the opposite of the obvious one. Candidates are read
// FIRST, against the snapshots left by the previous sweep, and only then are
// the snapshots overwritten. Saving first would compare every row against
// itself and find nothing, forever, while appearing to work perfectly.

// cheatJobName is what an operator sees on /admin/jobs.
const cheatJobName = "Tracker Cheat Sweep"

// CheatSweepInterval is how often the counters are sampled.
//
// Fifteen minutes is a compromise with one honest cost: a burst shorter than
// the interval averages out and is invisible. Shorter windows catch more but
// divide by less, and below a few minutes the arithmetic starts inventing its
// own impossible rates from ordinary announces — which is why EvaluateCheat
// refuses a window under MinSampleSeconds regardless of what this is set to.
const CheatSweepInterval = 15 * time.Minute

// cheatBatch bounds one sweep. user_stats is the largest table this plugin
// owns, and a detector that locks it for a minute has become the problem.
const cheatBatch = 5000

func (p *Plugin) runCheatSweep(ctx context.Context) {
	if p.cheatJob == nil || p.cheat == nil {
		return
	}
	p.cheatJob.SetRunning()
	// NOT a bare `defer SetIdle`, which is what this was. A deferred SetIdle
	// runs AFTER every SetError below and overwrites it, so a failed sweep
	// displayed as a clean idle run — the job card showed green while the
	// candidate query was erroring every fifteen minutes. The failure flag only
	// survives if the deferred call knows to stand down.
	failed := false
	defer func() {
		if !failed {
			p.cheatJob.SetIdle(time.Now().Add(CheatSweepInterval))
		}
	}()
	fail := func(msg string) {
		failed = true
		p.cheatJob.SetError(msg)
	}

	policy := p.cfg.Cheat.normalise()
	policy.Enabled = p.cfg.Cheat.Enabled
	if !policy.Enabled {
		// Snapshots are STILL taken while detection is off, so switching it on
		// starts working immediately rather than after a second sweep. A
		// feature that does nothing for its first fifteen minutes reads as
		// broken to whoever just enabled it.
		if _, err := p.cheat.SaveCheatSnapshots(ctx, time.Now()); err != nil {
			fail(err.Error())
			return
		}
		p.cheatJob.Log("detection off — sampled only (plugins.tracker.cheat.enabled)")
		return
	}

	cands, err := p.cheat.CheatCandidates(ctx, cheatBatch)
	if err != nil {
		fail(err.Error())
		return
	}
	flagged := 0
	for _, c := range cands {
		if ctx.Err() != nil {
			return
		}
		f, ok := EvaluateCheat(policy, CheatSample{
			UserID: c.UserID, InfoHash: c.InfoHash,
			PrevAt: c.PrevAt, CurAt: time.Now(),
			PrevUp: c.PrevUp, CurUp: c.CurUp,
			TorrentSize: c.TorrentSize, Peers: c.Peers,
		})
		if !ok {
			continue
		}
		if err := p.cheat.RecordCheatFlag(ctx, f); err != nil {
			// One unrecordable finding is not a reason to abandon the sweep and
			// leave the snapshots unmoved — that would re-judge the whole batch
			// next time and log the same failure again.
			p.core.Errors.Report(ctx, "tracker/cheat-flag", err)
			continue
		}
		flagged++
	}

	// Only now. See the note at the top of this file.
	n, err := p.cheat.SaveCheatSnapshots(ctx, time.Now())
	if err != nil {
		fail(err.Error())
		return
	}
	p.cheatJob.Log("compared %d pair(s), flagged %d, sampled %d", len(cands), flagged, n)
}

// registerCheatJob wires the sweep. Registered even when detection is off,
// because the sampling still runs and an operator should be able to see the job
// exists and when it last ran — a switch with no visible machinery behind it is
// one nobody trusts.
func (p *Plugin) registerCheatJob(c *core.Core) {
	if c.Scheduler == nil {
		return
	}
	p.cheatJob = c.Scheduler.RegisterJob(cheatJobName,
		"Samples the tracker's counters and flags readings no real client could have produced").
		MarkWrites()
	p.cheatJob.SetTriggerAsync(func() { p.runCheatSweep(p.ctx) })
}
