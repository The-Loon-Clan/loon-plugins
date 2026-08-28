package ranks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Automatic promotion: the evaluator that makes kind='earned' mean something.
//
// 'earned' shipped in the schema's CHECK, in the admin dropdown and in the Go
// model, and nothing ever evaluated it — a member could reach an earned group
// by no path at all. This is that path.
//
// WHY A SWEEP AND NOT AN EVENT. Most of the criteria have nothing to fire on.
// Ratio moves on every announce, and a promotion check there would sit on the
// tracker's hottest path to answer a question that changes for one member a
// week. Account age moves with no event whatsoever: nothing happens on the day
// somebody turns thirty days old, and the rewards plugin already makes exactly
// this argument about tenure — "no plugin announces an anniversary, and none
// should, because the member does not do anything on the day."
//
// Release count is the one criterion that DOES have an event — a publish — and
// it is still judged here, because a rung gated on releases and age cannot be
// decided at publish time anyway: the member usually clears the count long
// before the tenure, and a ladder that promoted on one clock and demoted on
// another would move people twice for the same qualification.
//
// WHY NOT AN ACHIEVEMENT, which is the reuse that looks obvious. Three
// differences, and the third is fatal:
//
//	criteria     an achievement has ONE threshold on ONE metric; a class is
//	             conjunctive — uploaded AND ratio AND age AND releases.
//	cardinality  achievements are a bag, you hold every one you earn; a class is
//	             exclusive, you hold the highest you qualify for.
//	direction    an achievement completes once and cannot walk backwards. A
//	             class must, or a ratio requirement means nothing.
//
// WHAT IT WILL NEVER TOUCH. Only memberships of groups whose kind is 'earned'.
// A purchased rank and a staff-assigned one are somebody's decision, and this
// job does not get to overrule either — which also means an earned class and a
// bought rank coexist as two memberships rather than fighting over one slot.

// Automatic reports whether this group is awarded by the promotion sweep.
//
// Requires kind='earned' AND at least one criterion above zero. That second
// half is the guard that matters: zero thresholds mean "no criterion", and read
// the other way — everyone clears a threshold of zero — a half-configured group
// would promote the entire membership on the next sweep.
func (g Group) Automatic() bool {
	if g.Kind != "earned" {
		return false
	}
	// Any ONE criterion is enough to make the group gated. A rung asking only
	// for releases is a complete rule — it is the whole ladder on a host with no
	// tracker — so leaving it out here would leave that group looking
	// half-configured and never promoting anyone.
	return g.MinUploaded > 0 || g.MinRatio > 0 || g.MinAgeDays > 0 || g.MinReleases > 0
}

// Qualifies reports whether these figures meet every criterion this group sets.
//
// Conjunctive, and only over criteria that are SET: a group asking for 100 GB
// and nothing else is judged on uploaded alone. A zero threshold is not "must
// be at least zero", it is "not asked".
func (g Group) Qualifies(s pluginapi.MemberStats, now time.Time) bool {
	if g.MinUploaded > 0 && s.Uploaded < g.MinUploaded {
		return false
	}
	if g.MinRatio > 0 && s.Ratio < g.MinRatio {
		return false
	}
	if g.MinAgeDays > 0 && s.AgeDays(now) < g.MinAgeDays {
		return false
	}
	if g.MinReleases > 0 {
		// Absent is not zero, and neither one earns the rung: a count the host
		// never proved cannot satisfy a threshold. Fails CLOSED like every
		// criterion here, which is also what lets a rung require releases AND
		// bytes together — the tracker-era case where a member has to have both
		// contributed and seeded, not either.
		//
		// The cost of failing closed is on the host, and pluginapi documents it:
		// a host that supplies counts, promotes on them and then reports the same
		// members with the count missing deranks that rung. Omit the member
		// instead — planPromotions leaves an omitted member alone.
		n, known := s.Releases()
		if !known || n < g.MinReleases {
			return false
		}
	}
	return true
}

// bestClass picks the highest class these figures earn, or ok=false for none.
//
// "Highest" is SortOrder, which is what an operator already uses to order the
// ladder on the catalog page — so the rung a member lands on is the one they
// see above the last, rather than a second ordering nobody can see. Ties break
// on ID so a sweep is deterministic; two rungs sharing a sort_order is an
// operator's mistake and picking arbitrarily would make it an intermittent one.
func bestClass(groups []Group, s pluginapi.MemberStats, now time.Time) (Group, bool) {
	var best Group
	var found bool
	for _, g := range groups {
		if !g.Automatic() || !g.Qualifies(s, now) {
			continue
		}
		if !found || g.SortOrder > best.SortOrder ||
			(g.SortOrder == best.SortOrder && g.ID > best.ID) {
			best, found = g, true
		}
	}
	return best, found
}

// promotionChange is what the sweep decided for one member.
type promotionChange struct {
	UserID int
	// Add is the class to grant, or 0 for none.
	Add int
	// Drop are the earned memberships to remove — plural because an earlier
	// bug, a hand-edited row or a changed ladder can leave a member holding two.
	Drop []int
}

// planPromotions decides every change, and is the whole rule.
//
// Pure, and separated from the store for the reason the rest of this file
// exists: the interesting behaviour is what it does with awkward input, and
// none of that needs a database to demonstrate.
//
// stats may OMIT a member, and omission is not zero. A member absent from the
// map is skipped entirely rather than demoted — otherwise a query that returned
// half the membership would strip the other half's ranks, once, silently, and
// the next sweep would put them back.
func planPromotions(groups []Group, members []Member, stats map[int64]pluginapi.MemberStats, now time.Time) []promotionChange {
	earned := map[int]bool{}
	for _, g := range groups {
		if g.Kind == "earned" {
			earned[g.ID] = true
		}
	}
	// What each member currently holds of the earned ladder. Paid and assigned
	// memberships are not collected, so they cannot be dropped.
	held := map[int][]int{}
	for _, m := range members {
		if earned[m.GroupID] {
			held[m.UserID] = append(held[m.UserID], m.GroupID)
		}
	}
	// Every member with figures, plus every member currently holding an earned
	// class — the second half is what makes DEMOTION possible for somebody
	// whose stats have gone.
	subjects := map[int]bool{}
	for id := range stats {
		subjects[int(id)] = true
	}

	out := make([]promotionChange, 0, len(subjects))
	for userID := range subjects {
		s, ok := stats[int64(userID)]
		if !ok {
			continue
		}
		want, qualifies := bestClass(groups, s, now)
		ch := promotionChange{UserID: userID}
		for _, gid := range held[userID] {
			if qualifies && gid == want.ID {
				continue // already on the right rung
			}
			ch.Drop = append(ch.Drop, gid)
		}
		alreadyRight := qualifies && len(ch.Drop) == 0 && contains(held[userID], want.ID)
		if qualifies && !alreadyRight {
			ch.Add = want.ID
		}
		if ch.Add == 0 && len(ch.Drop) == 0 {
			continue
		}
		sort.Ints(ch.Drop)
		out = append(out, ch)
	}
	// Deterministic order, so a log of one sweep can be compared with the next.
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ── The job ─────────────────────────────────────────────────────────────────

// promotionInterval is the sweep cadence, and the SetIdle horizon, in one place
// so the admin page's "next run" matches what the loop waits — the same rule
// expiryInterval states for rank expiry.
//
// Hourly. A class ladder is not a live figure: nobody expects the rung to
// change the instant their ratio crosses, and a member who was promoted within
// the hour has no complaint. Cheaper than it looks, too — the whole sweep is
// one stats call, one catalog read and one membership read.
const promotionInterval = time.Hour

type rankPromotion struct {
	store Store
	ents  *entSync
	stats pluginapi.RankStats
	sched core.SchedulerService
	job   core.Job
}

func newRankPromotion(store Store, ents *entSync, stats pluginapi.RankStats, sched core.SchedulerService) *rankPromotion {
	p := &rankPromotion{store: store, ents: ents, stats: stats, sched: sched}
	p.job = sched.RegisterJob("Rank Promotion",
		"Promotes and demotes members between earned ranks on releases contributed, upload, ratio and account age").
		MarkWrites()
	p.job.SetTriggerAsync(func() { p.run(context.Background()) })
	return p
}

func (p *rankPromotion) start(ctx context.Context) {
	p.sched.RunLoop(ctx, p.job, 2*time.Minute, promotionInterval, p.run)
}

func (p *rankPromotion) run(ctx context.Context) {
	if p.job.IsPaused() {
		return
	}
	p.job.SetRunning()
	// NOTE: every path below this line must reach SetIdle or SetError. A run
	// that returns after SetRunning and does neither leaves the job displayed
	// as "running" forever — and the scheduler will not re-trigger one it
	// believes is still going, so the job silently never runs again. Found
	// exactly that way: the first sweep worked, and every manual trigger after
	// it returned 200 and did nothing.
	if p.stats == nil {
		// No host seam: nothing to judge anyone on. Said rather than silently
		// doing nothing, because an operator who has configured a ladder and
		// sees no promotions needs to know the figures never arrived.
		p.job.SetError("no RankStats capability registered — the host supplies the release-count/upload/ratio/age figures, and without it no rank can be earned")
		return
	}

	groups, err := p.store.Groups(ctx)
	if err != nil {
		p.job.SetError(fmt.Sprintf("catalog: %v", err))
		return
	}
	var automatic int
	for _, g := range groups {
		if g.Automatic() {
			automatic++
		}
	}
	if automatic == 0 {
		p.job.Log("No earned group has criteria set, so there is nothing to promote to")
		p.job.SetIdle(time.Now().Add(promotionInterval))
		return
	}

	stats, err := p.stats.AllStats(ctx)
	if err != nil {
		p.job.SetError(fmt.Sprintf("member stats: %v", err))
		return
	}
	ids := make([]int, 0, len(stats))
	for id := range stats {
		ids = append(ids, int(id))
	}
	members, err := p.store.MembershipsOfUsers(ctx, ids)
	if err != nil {
		p.job.SetError(fmt.Sprintf("memberships: %v", err))
		return
	}

	promoted, demoted := 0, 0
	for _, ch := range planPromotions(groups, members, stats, time.Now()) {
		for _, gid := range ch.Drop {
			if err := p.store.RemoveMember(ctx, ch.UserID, gid); err != nil {
				p.job.Log("user %d: removing group %d: %v", ch.UserID, gid, err)
				continue
			}
			// LOUD in the member's own history, because a silent derank is the
			// one that becomes a support ticket. RecordHistory is what the
			// expiry sweep already writes, so a member sees promotions,
			// deranks and expiries in one list.
			_ = p.store.RecordHistory(ctx, ch.UserID, &gid, "derank",
				"automatic: no longer meets the criteria for this rank")
			if p.ents != nil && p.ents.ents != nil {
				_ = p.ents.revokeMembership(ctx, ch.UserID, gid, nil)
			}
			demoted++
		}
		if ch.Add == 0 {
			continue
		}
		// Permanent: an earned class is held for as long as it is earned, and
		// the sweep is what takes it away. A duration here would expire a rank
		// the member still qualifies for and re-grant it on the next pass.
		if err := p.store.AddMember(ctx, ch.UserID, ch.Add, 0); err != nil {
			p.job.Log("user %d: granting group %d: %v", ch.UserID, ch.Add, err)
			continue
		}
		// Grant what the rank confers, mirroring the revoke in the drop loop
		// above. The two were asymmetric: a demotion revoked immediately, a
		// promotion waited for the next process Start to pick the grants up in
		// rebuildAll. On its own that is a delay; PAIRED with the drop loop it
		// is a hole, because a member who moved rung — dropped one, gained the
		// next — lost the old rank's entitlements at once and gained the new
		// rank's only after a restart. Permanent by construction (the grant
		// carries no expiry because the membership has none), and best-effort
		// for the same reason the history write is: the membership is already
		// written, and rebuildAll at boot is the repair path.
		if p.ents != nil && p.ents.ents != nil {
			_ = p.ents.grantMembership(ctx, ch.UserID, ch.Add, nil)
		}
		_ = p.store.RecordHistory(ctx, ch.UserID, &ch.Add, "promote",
			"automatic: meets the criteria for this rank")
		promoted++
	}
	if promoted > 0 || demoted > 0 {
		p.job.Log("Promoted %d, demoted %d", promoted, demoted)
	}
	p.job.SetIdle(time.Now().Add(promotionInterval))
}
