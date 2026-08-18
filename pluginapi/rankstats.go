// rankstats.go declares the contract the HOST publishes so the ranks plugin can
// promote and demote members automatically.
//
// Like InviteGranterName and unlike RankGranterName, this comes from the host
// rather than a sibling plugin — and for a sharper reason than invites. The
// figures a class ladder is judged on live in four different places: uploaded
// and downloaded in the tracker plugin's own schema, the join date in the
// host's users table, and the count of releases the member has contributed in
// whichever plugin owns publishing. No single plugin can read all of them, and
// a plugin schema does not reference host tables. The host is the only
// component that can answer, so the host answers.
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

	// ReleasesContributed is how many RELEASES the member has contributed. It is
	// a count of things, not a quantity of bytes, and it is the figure an indexer
	// with no tracker can actually answer: Uploaded and Ratio are tracker
	// accounting, so on a host that has never run one they are zero for everybody
	// and the only usable rung left is tenure — which rewards waiting rather than
	// contributing.
	//
	// THE NAME IS DELIBERATELY NOT "Uploads". That is one character from
	// Uploaded, both would be numbers, and a rule that compared a byte threshold
	// against a release count would compile, run, and be wrong by nine orders of
	// magnitude — the ladder's own comments already show how much a one-word slip
	// costs here, which is why Ratio is supplied rather than derived. The type
	// carries the same distinction the name does: int, not the int64 the byte
	// figures use, so the compiler refuses the mix-up the name only discourages.
	//
	// NIL IS "NOT PROVEN", NEVER ZERO — the rule JoinedAt states, and for the
	// same reason, but a count needs a pointer to say it: zero is a legitimate
	// answer for a member who has contributed nothing, so there is no in-band
	// value left to mean "this host does not supply the figure". A criterion
	// gated on an absent count is never satisfied. That makes the field ADDITIVE:
	// a host that does not implement it compiles and boots unchanged, and a rung
	// asking for releases is simply never earned there rather than wrongly
	// granted to everybody at zero.
	//
	// WHAT A HOST THAT DOES ANSWER OWES: keep answering. Promotion is safe either
	// way — neither "unproven" nor "zero" earns a rung — but DEMOTION is not
	// symmetric, because a criterion that stops being satisfied takes the rank
	// back. A host that supplied counts, promoted members on them, then reported
	// the same members with a nil count would derank that whole rung. To signal
	// "I could not read the figures this pass", omit the MEMBER, which AllStats
	// already defines as leave-them-alone — never report a member with the count
	// missing.
	ReleasesContributed *int
}

// Releases is the contributed-release count and whether the host proved it.
//
// Two return values on purpose. A single int with 0 standing in for "unknown"
// would work for promotion by accident — both fail a positive threshold — and
// would quietly lose the distinction for every other reader, which is the exact
// erosion the field's comment above exists to prevent.
func (s MemberStats) Releases() (count int, known bool) {
	if s.ReleasesContributed == nil {
		return 0, false
	}
	return *s.ReleasesContributed, true
}

// ReleaseCount boxes a proven count, for a host filling in MemberStats.
// Absence is the zero value of the field, so there is nothing to box for it.
func ReleaseCount(n int) *int { return &n }

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
