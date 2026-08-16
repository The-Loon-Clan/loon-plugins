// rankstats.go declares the contract the HOST publishes so the ranks plugin can
// promote and demote members automatically.
//
// Like InviteGranterName and unlike RankGranterName, this comes from the host
// rather than a sibling plugin — and for a sharper reason than invites. The
// figures a class ladder is judged on live in three different places: uploaded
// and downloaded in the tracker plugin's own schema, the join date in the
// host's users table. No single plugin can read all of them, and a plugin
// schema does not reference host tables. The host is the only component that
// can answer, so the host answers.
package pluginapi

import (
	"context"
	"time"
)

// RankStatsName is the Core extension-registry key under which the host
// publishes its RankStats. Absent means automatic promotion is simply not
// available — a host with no tracker has no upload figures to judge anyone on,
// and that is a legitimate host rather than a broken one.
const RankStatsName = "ranks.stats"

// MemberStats is one member's standing, as a promotion rule sees it.
//
// RATIO IS SUPPLIED, NOT DERIVED, and that is deliberate. "Ratio" already has a
// definition in this stack — tracker.Totals.Ratio, mirrored by the host's
// storage.TrackerTotals.Ratio, which says outright that it exists so there is
// not a second one. Computing it here would make a third, and the day the rule
// changed two of the three would be wrong.
//
// THE TRAP THAT DEFINITION CARRIES, because a ladder is where it bites: a
// member who has downloaded NOTHING does not have an infinite ratio, they have
// their upload figure. Somebody who uploaded 5 GB and downloaded zero reports a
// ratio of 5,000,000,000 and passes any threshold you can write. This is why a
// class must be gated on uploaded AND ratio together, never on ratio alone —
// which is what every real tracker does, for exactly this reason.
type MemberStats struct {
	Uploaded   int64
	Downloaded int64
	// Ratio as the site's accounting defines it. See above before using it.
	Ratio float64
	// JoinedAt is when the account was created. Zero when the host does not
	// know, which a rule must read as "no age proven" rather than as "infinitely
	// old" — an unknown join date promoting somebody on tenure is the failure
	// this comment exists to prevent.
	JoinedAt time.Time
}

// AgeDays is how long the account has existed, floored, and 0 when unknown.
func (s MemberStats) AgeDays(now time.Time) int {
	if s.JoinedAt.IsZero() || now.Before(s.JoinedAt) {
		return 0
	}
	return int(now.Sub(s.JoinedAt).Hours() / 24)
}

// RankStats supplies every member's figures for the promotion sweep.
type RankStats interface {
	// AllStats returns userID -> stats for every member worth considering.
	//
	// ONE call for the whole membership, the same shape as rewards.MetricSource
	// and for the same reason: the sweep runs hourly and a per-member read to
	// discover that almost nobody moved is thousands of queries to learn
	// nothing.
	//
	// A member may be omitted, and omission means "no figures" rather than
	// "zero" — the caller must not demote somebody for being absent from a map,
	// because a query that returned half the membership would otherwise strip
	// the other half's ranks.
	AllStats(ctx context.Context) (map[int64]MemberStats, error)
}
