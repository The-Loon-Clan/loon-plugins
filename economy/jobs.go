package economy

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// ── Points Grab Bonus ────────────────────────────────────────────────────────
// Awards uploaders points for each new grab on their NZBs (delta since the last
// run, tracked via the earn_grabs ledger reference). Extracted verbatim from
// pkg/services.PointsGrabService. (The EarnRule-plugin candidate from
// FRAMEWORK-ARCHITECTURE.md §4 — once the earn-rule registry lands, this
// registers as "earn:grabs".)
type grabBonus struct {
	deps   Deps
	points core.PointsService
	job    *schedule.JobInfo
}

func newGrabBonus(deps Deps, points core.PointsService) *grabBonus {
	s := &grabBonus{deps: deps, points: points}
	s.job = schedule.RegisterJob("Points Grab Bonus",
		"Awards points to uploaders for each NZB download/grab on their uploads").
		MarkWrites()
	s.job.IntervalMin = 24 * 60
	s.job.SetTriggerAsync(func() { s.run(context.Background()) })
	return s
}

func (s *grabBonus) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, s.job, 15*time.Minute, 24*time.Hour, s.run)
}

func (s *grabBonus) run(ctx context.Context) {
	job := s.job
	job.SetRunning()

	ptsPerGrab := s.deps.PointsPerGrab(ctx)
	if ptsPerGrab <= 0 {
		job.Log("Grab bonus disabled (0 pts/grab)")
		job.SetIdle(time.Now().Add(24 * time.Hour))
		return
	}

	rows, err := s.deps.UploaderGrabTotals(ctx)
	if err != nil {
		job.SetError(fmt.Sprintf("query grab totals: %v", err))
		return
	}

	awarded := 0
	totalPts := 0
	for _, row := range rows {
		// Delta idempotency: only credit grabs beyond what earn_grabs already
		// recorded (reference_id = total-grabs-at-award-time). A FAILED read
		// must skip the row, not default to zero — zero means "never
		// credited", which re-awards the user's entire grab history.
		alreadyCredited, err := s.deps.GrabsAlreadyCredited(ctx, row.UserID)
		if err != nil {
			log.Printf("economy/grabs: high-water read uid=%d: %v", row.UserID, err)
			continue
		}
		newGrabs, pts := grabAward(row.TotalGrabs, alreadyCredited, ptsPerGrab)
		if pts <= 0 {
			continue
		}
		// The reference is the new high-water mark, so a re-run reads it back
		// through GrabsAlreadyCredited and computes a zero delta. Awarding
		// before recording it would credit twice on a crash between the two —
		// which is why this is one call and not two.
		if _, err := s.points.Award(ctx, int64(row.UserID), pts, reasonGrabs,
			fmt.Sprintf("%d new grab(s) x %d pts (+%d pts)", newGrabs, ptsPerGrab, pts),
			int64(row.TotalGrabs)); err != nil {
			log.Printf("economy/grabs: award uid=%d: %v", row.UserID, err)
			continue
		}
		awarded++
		totalPts += pts
	}

	if awarded > 0 {
		job.Log("Awarded grab bonus to %d uploader(s), %d total pts", awarded, totalPts)
	} else {
		job.Log("No new grabs to award")
	}
	job.SetIdle(time.Now().Add(24 * time.Hour))
}

// grabAward is the delta arithmetic: how many grabs are new since the last
// award, and what they are worth.
//
// Split out because it is the only place this plugin can lose money. Credited
// is a high-water mark read back from the ledger, and every failure mode is a
// subtraction going the wrong way: a mark AHEAD of the total (a grab count
// that fell after a purge, a manual ledger edit) must award nothing rather
// than a negative, and an equal mark must award nothing rather than one more
// round of the same grabs.
func grabAward(totalGrabs, alreadyCredited, pointsPerGrab int) (newGrabs, points int) {
	if pointsPerGrab <= 0 {
		return 0, 0
	}
	newGrabs = totalGrabs - alreadyCredited
	if newGrabs <= 0 {
		return 0, 0
	}
	return newGrabs, newGrabs * pointsPerGrab
}
