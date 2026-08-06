package offers

// The offer system's background jobs. They run ONLY in worker-capable
// processes: Plugin.Start gates on the process kind captured at Provision, so
// a split-mode web process registers the routes but never races these loops.

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

var offerSweeperJob = schedule.RegisterJob(
	"Offer Sweeper",
	"Reopens offer_requests whose claim expired without delivery. Bumps the claimer's failed_count and notifies the requester.",
)

var offerPrunerJob = schedule.RegisterJob(
	"Offer Pruner",
	"Drops offer rows whose Python script hasn't heartbeat-pinged in 60+ days (zero-fulfillment only — reputation evidence is kept).",
)

// offerSweeper expires stale claims on offer_requests.
//
// When an offerer claims a request, the row gets status='claimed' and
// claim_expires_at = NOW()+15m. If the claimer's script crashes or they get
// bored mid-download, the row would stay locked forever. This runs every
// minute, reopens expired claims, and bumps the claimer's failed_count so
// their reputation reflects reality.
//
// Cheap: one indexed UPDATE per tick. 99% of ticks process zero rows.
type offerSweeper struct {
	deps JobDeps
	job  *schedule.JobInfo
}

func newOfferSweeper(deps JobDeps) *offerSweeper {
	s := &offerSweeper{deps: deps, job: offerSweeperJob}
	// Background, not the boot context: a manual trigger from /admin/jobs must
	// not be cancelled by whatever fired it.
	offerSweeperJob.SetTrigger(func() { go s.runOnce(context.Background()) })
	return s
}

// start runs the sweeper every minute. Boot delay 90s so the rest of the
// worker has finished warming up — the sweeper does not touch hot data, so
// the delay is politeness rather than necessity. ctx comes from Start, so
// SIGTERM unblocks the loop.
func (s *offerSweeper) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, s.job, 90*time.Second, 60*time.Second, s.runOnce)
}

func (s *offerSweeper) runOnce(ctx context.Context) {
	s.job.SetRunning()
	expired, err := s.deps.ExpireStaleClaims(ctx)
	if err != nil {
		s.job.SetError(err.Error())
		s.deps.ReportError(ctx, "offer-sweeper", err)
		return
	}
	if len(expired) > 0 {
		s.job.Log("Reopened %d expired claim(s)", len(expired))
		// Optional: the row reopening is the canonical event, so a missing
		// notifier degrades the experience without losing the outcome.
		if s.deps.NotifyFailed != nil {
			for _, e := range expired {
				s.deps.NotifyFailed(ctx, e.RequesterUserID, e.RequestID)
			}
		}
	}
	s.job.SetIdle(time.Now().Add(60 * time.Second))
}

// offerPruner drops stale rows from the offers table.
//
// The script heartbeats last_seen_at on every sync. When somebody stops
// running it their old rows would linger forever and pollute the
// "available from" surfaces. Rows with any fulfilled or failed count are
// KEPT — an offerer's track record is part of the public record — and the
// buckets survive independently, since offer_hash stays the dedup key.
//
// Cheap: one indexed DELETE per tick, daily. There is no urgency.
type offerPruner struct {
	deps       JobDeps
	job        *schedule.JobInfo
	staleAfter time.Duration
}

func newOfferPruner(deps JobDeps) *offerPruner {
	s := &offerPruner{deps: deps, job: offerPrunerJob, staleAfter: 60 * 24 * time.Hour}
	offerPrunerJob.SetTrigger(func() { go s.runOnce(context.Background()) })
	return s
}

// start fires once daily, after a 5-minute boot delay so it does not compete
// with the worker's warm-up scans.
func (s *offerPruner) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, s.job, 5*time.Minute, 24*time.Hour, s.runOnce)
}

func (s *offerPruner) runOnce(ctx context.Context) {
	s.job.SetRunning()
	n, err := s.deps.PruneStaleOffers(ctx, s.staleAfter)
	if err != nil {
		s.job.SetError(err.Error())
		s.deps.ReportError(ctx, "offer-pruner", err)
		return
	}
	if n > 0 {
		s.job.Log("Pruned %d stale offer row(s) (no fulfillment, last seen >%dd ago)",
			n, int(s.staleAfter/(24*time.Hour)))
	}
	s.job.SetIdle(time.Now().Add(24 * time.Hour))
}
