package ranks

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// expiryInterval is the cadence and the SetIdle horizon, in one place so the
// two cannot drift — the admin page's "next run" is only honest if it matches
// what the loop actually waits.
const expiryInterval = time.Hour

// rankExpiry runs "Rank Expiry": removes lapsed memberships, logs the deranks
// to history, and revokes what those memberships conferred.
//
// Scheduling comes off core.Scheduler rather than the host's pkg/services. The
// two are the same registry underneath — loon's schedule package backs both —
// but going through the Core seam is what removed this plugin's last host
// import, and with it JobDeps/SetJobDeps: the scheduler arrives on *core.Core
// like every other capability, so there is nothing left for cmd/main.go to
// hand over.
type rankExpiry struct {
	store Store
	ents  *entSync
	sched core.SchedulerService
	job   core.Job
}

func newRankExpiry(store Store, ents *entSync, sched core.SchedulerService) *rankExpiry {
	s := &rankExpiry{store: store, ents: ents, sched: sched}
	s.job = sched.RegisterJob("Rank Expiry",
		"Removes expired rank subscriptions and logs deranks to history")
	// The manual "run now" button. It does NOT go through RunLoop, so the
	// pause check inside run() is what stops it firing on a paused job.
	s.job.SetTrigger(func() { go s.run(context.Background()) })
	return s
}

// start runs shortly after boot, then hourly. RunLoop owns the boot delay, the
// off-peak gate, the admin interval override and cancellation; it also records
// the cadence on the job, so this no longer sets IntervalMin by hand.
func (s *rankExpiry) start(ctx context.Context) {
	s.sched.RunLoop(ctx, s.job, time.Minute, expiryInterval, s.run)
}

func (s *rankExpiry) run(ctx context.Context) {
	// RunLoop already skips a paused job, so this is here for the manual
	// trigger, which bypasses the loop entirely.
	if s.job.IsPaused() {
		return
	}
	s.job.SetRunning()
	expired, err := s.store.ExpireMemberships(ctx)
	if err != nil {
		s.job.SetError(fmt.Sprintf("expire memberships: %v", err))
		return
	}
	if len(expired) > 0 {
		s.job.Log("Expired %d membership(s)", len(expired))
	}
	// Revoke what those memberships conferred. The grants also carry the
	// membership's expiry, so they had already stopped counting — this clears
	// the rows rather than closing a window.
	if s.ents != nil && s.ents.ents != nil {
		for _, m := range expired {
			if err := s.ents.revokeMembership(ctx, m.UserID, m.GroupID, nil); err != nil {
				s.job.Log("WARN revoke entitlements for user %d: %v", m.UserID, err)
			}
		}
	}
	s.job.SetIdle(time.Now().Add(expiryInterval))
}
