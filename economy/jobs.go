package economy

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// ── Points Tenure Bonus ──────────────────────────────────────────────────────
// Awards annual tenure points to users on their join anniversary. Extracted
// verbatim from pkg/services.PointsTenureService.
type tenureBonus struct {
	deps   Deps
	points core.PointsService
	job    *schedule.JobInfo
}

func newTenureBonus(deps Deps, points core.PointsService) *tenureBonus {
	s := &tenureBonus{deps: deps, points: points}
	s.job = schedule.RegisterJob("Points Tenure Bonus", "Awards points to users on their registration anniversary")
	s.job.IntervalMin = 24 * 60
	s.job.SetTrigger(func() { go s.run(context.Background()) })
	return s
}

func (s *tenureBonus) start(ctx context.Context) {
	// Bare ServiceLoop: the host installs the off-peak / interval-override /
	// panic hooks globally, so the plugin needs no store of its own — the
	// in-tree version carried one only because the site's re-export took it.
	go schedule.ServiceLoop(ctx, s.job, 10*time.Minute, 24*time.Hour, s.run)
}

func (s *tenureBonus) run(ctx context.Context) {
	job := s.job
	job.SetRunning()

	perYear := s.deps.PointsTenurePerYear(ctx)
	if perYear <= 0 {
		job.Log("Tenure bonus disabled (0 pts/year)")
		job.SetIdle(time.Now().Add(24 * time.Hour))
		return
	}

	rows, err := s.deps.TenureEligible(ctx)
	if err != nil {
		job.SetError(fmt.Sprintf("query: %v", err))
		return
	}

	awarded := 0
	for _, u := range rows {
		years, pts := tenureAward(u.CreatedAt, time.Now(), perYear)
		if pts <= 0 {
			continue
		}
		if _, err := s.points.Award(ctx, int64(u.ID), pts, reasonTenure,
			fmt.Sprintf("Membership anniversary: %d year(s) x %d pts", years, perYear), 0); err != nil {
			log.Printf("economy/tenure: award uid=%d: %v", u.ID, err)
			continue
		}
		awarded++
	}

	if awarded > 0 {
		job.Log("Awarded tenure bonus to %d user(s)", awarded)
	}
	job.SetIdle(time.Now().Add(24 * time.Hour))
}

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
		"Awards points to uploaders for each NZB download/grab on their uploads")
	s.job.IntervalMin = 24 * 60
	s.job.SetTrigger(func() { go s.run(context.Background()) })
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
		// recorded (reference_id = total-grabs-at-award-time).
		alreadyCredited, _ := s.deps.GrabsAlreadyCredited(ctx, row.UserID)
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

// tenureAward is the anniversary arithmetic: completed years of membership and
// what they are worth.
//
// Years are floored from elapsed time, so an account exactly on its
// anniversary reads as a whole year rather than as 0.999 of one. Eligibility —
// whether this user has already been paid this year — is NOT decided here: the
// host's query owns that, and a second opinion in the plugin is how a member
// gets paid twice.
func tenureAward(createdAt, now time.Time, pointsPerYear int) (years, points int) {
	if pointsPerYear <= 0 || createdAt.IsZero() || !createdAt.Before(now) {
		return 0, 0
	}
	years = int(now.Sub(createdAt).Hours() / 24 / 365)
	if years < 1 {
		return 0, 0
	}
	return years, years * pointsPerYear
}
