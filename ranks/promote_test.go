package ranks

import (
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

var promoNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

const gb = 1 << 30

// The demo ladder's shape: three earned rungs, one bought rank, one staff rank.
func ladder() []Group {
	joined := func(d int) int { return d }
	return []Group{
		{ID: 1, Slug: "newcomer", Kind: "earned", SortOrder: 10, MinAgeDays: joined(7)},
		{ID: 2, Slug: "regular", Kind: "earned", SortOrder: 20, MinUploaded: 10 * gb, MinRatio: 1.0, MinAgeDays: joined(30)},
		{ID: 3, Slug: "elite", Kind: "earned", SortOrder: 30, MinUploaded: 100 * gb, MinRatio: 2.0, MinAgeDays: joined(90)},
		// Bought and staff-given. The sweep must never touch either.
		{ID: 4, Slug: "patron", Kind: "paid", SortOrder: 25, CostPoints: 2000},
		{ID: 5, Slug: "veteran", Kind: "assigned", SortOrder: 40},
		// Earned but with NO criteria — an operator who set the kind and stopped.
		{ID: 6, Slug: "halfdone", Kind: "earned", SortOrder: 35},
	}
}

func stats(up, down int64, ratio float64, ageDays int) pluginapi.MemberStats {
	return pluginapi.MemberStats{
		Uploaded: up, Downloaded: down, Ratio: ratio,
		JoinedAt: promoNow.AddDate(0, 0, -ageDays),
	}
}

// An earned group with no criteria set is NOT automatic.
//
// The guard that matters. Zero thresholds mean "not asked", and read the other
// way — everybody clears a threshold of zero — the halfdone group above has the
// HIGHEST sort_order of any earned rung, so a single sweep would promote the
// entire membership to a group an operator created and never finished.
func TestAGroupWithNoCriteriaIsNotAutomatic(t *testing.T) {
	for _, g := range ladder() {
		want := g.Slug == "newcomer" || g.Slug == "regular" || g.Slug == "elite"
		if got := g.Automatic(); got != want {
			t.Errorf("%s (kind=%s): Automatic() = %v, want %v", g.Slug, g.Kind, got, want)
		}
	}
}

// Criteria are conjunctive, and only over what is SET.
func TestQualifiesIsConjunctive(t *testing.T) {
	regular := ladder()[1] // 10 GB, ratio 1.0, 30 days
	for _, tc := range []struct {
		name string
		s    pluginapi.MemberStats
		want bool
	}{
		{"meets all three", stats(20*gb, 10*gb, 2.0, 60), true},
		{"exactly on every threshold", stats(10*gb, 10*gb, 1.0, 30), true},
		{"upload short", stats(9*gb, 1*gb, 9.0, 60), false},
		{"ratio short", stats(20*gb, 40*gb, 0.5, 60), false},
		{"too new", stats(20*gb, 10*gb, 2.0, 29), false},
	} {
		if got := regular.Qualifies(tc.s, promoNow); got != tc.want {
			t.Errorf("%s: Qualifies = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The upload-only trap, spelled out because it is why min_uploaded exists.
//
// A member who has downloaded NOTHING does not have an infinite ratio — the
// site's accounting reports their upload figure, so 5 GB uploaded and nothing
// grabbed reads as a ratio of 5,000,000,000 and clears any ratio threshold that
// can be written. Only the upload criterion stops them walking into Elite.
func TestRatioAloneCannotCarryAMemberUpTheLadder(t *testing.T) {
	elite := ladder()[2] // 100 GB, ratio 2.0, 90 days
	uploadOnly := stats(5*gb, 0, 5*gb, 365)
	if elite.Qualifies(uploadOnly, promoNow) {
		t.Error("a member with 5 GB up and nothing down qualified for Elite on ratio alone")
	}
	// And the ratio check itself is genuinely passed, which is the point: the
	// upload floor is doing the work, not the ratio one.
	ratioOnly := Group{ID: 9, Kind: "earned", SortOrder: 1, MinRatio: 2.0}
	if !ratioOnly.Qualifies(uploadOnly, promoNow) {
		t.Error("the ratio threshold was NOT passed, so this test proves nothing about min_uploaded")
	}
}

// A member lands on the HIGHEST rung they qualify for, not the first.
func TestBestClassPicksTheHighestRung(t *testing.T) {
	g := ladder()
	if best, ok := bestClass(g, stats(200*gb, 50*gb, 4.0, 400), promoNow); !ok || best.Slug != "elite" {
		t.Errorf("got %q, want elite", best.Slug)
	}
	if best, ok := bestClass(g, stats(20*gb, 10*gb, 2.0, 60), promoNow); !ok || best.Slug != "regular" {
		t.Errorf("got %q, want regular", best.Slug)
	}
	if _, ok := bestClass(g, stats(0, 0, 0, 1), promoNow); ok {
		t.Error("a brand-new account with nothing qualified for a rung")
	}
}

// DEMOTION, and the two things it must not do.
func TestPlanPromotionsDemotes(t *testing.T) {
	g := ladder()

	// 1. Falls from Elite to Regular: drops one rung, gains the other.
	fallen := []Member{{UserID: 7, GroupID: 3}}
	plan := planPromotions(g, fallen, map[int64]pluginapi.MemberStats{
		7: stats(20*gb, 10*gb, 2.0, 400),
	}, promoNow)
	if len(plan) != 1 || plan[0].Add != 2 || len(plan[0].Drop) != 1 || plan[0].Drop[0] != 3 {
		t.Fatalf("fall from elite to regular planned as %+v", plan)
	}

	// 2. Falls off the ladder entirely: dropped, nothing added.
	plan = planPromotions(g, fallen, map[int64]pluginapi.MemberStats{
		7: stats(0, 50*gb, 0, 1),
	}, promoNow)
	if len(plan) != 1 || plan[0].Add != 0 || len(plan[0].Drop) != 1 {
		t.Fatalf("fall off the ladder planned as %+v", plan)
	}

	// 3. Nothing to do is NOT a change. A sweep that re-granted the rung a
	//    member already holds would write a history line every hour, and the
	//    derank/promote log is where somebody looks to understand their account.
	onRung := []Member{{UserID: 7, GroupID: 2}}
	plan = planPromotions(g, onRung, map[int64]pluginapi.MemberStats{
		7: stats(20*gb, 10*gb, 2.0, 400),
	}, promoNow)
	if len(plan) != 0 {
		t.Fatalf("a member already on the right rung was planned a change: %+v", plan)
	}
}

// A bought rank and a staff rank survive any sweep.
//
// The member below qualifies for nothing, so every earned membership goes — and
// the paid and assigned ones must still be there afterwards. Losing a rank
// somebody PAID for because their ratio slipped is the failure that would cost
// real money and real trust.
func TestTheSweepNeverTouchesBoughtOrAssignedRanks(t *testing.T) {
	held := []Member{
		{UserID: 7, GroupID: 3}, // elite, earned
		{UserID: 7, GroupID: 4}, // patron, PAID
		{UserID: 7, GroupID: 5}, // veteran, ASSIGNED
	}
	plan := planPromotions(ladder(), held, map[int64]pluginapi.MemberStats{
		7: stats(0, 100*gb, 0, 1),
	}, promoNow)
	if len(plan) != 1 {
		t.Fatalf("expected one change, got %+v", plan)
	}
	for _, gid := range plan[0].Drop {
		if gid == 4 || gid == 5 {
			t.Errorf("the sweep planned to drop group %d, which is paid or staff-assigned", gid)
		}
	}
	if len(plan[0].Drop) != 1 || plan[0].Drop[0] != 3 {
		t.Errorf("dropped %v, want only the earned rung (3)", plan[0].Drop)
	}
}

// A member missing from the stats map is SKIPPED, never demoted.
//
// Omission is not zero. A stats query that returned half the membership — a
// timeout, a partial read, a tracker plugin mid-restart — would otherwise strip
// every rank on the site in one pass, log it as thousands of legitimate
// deranks, and put them all back on the next sweep.
func TestAMemberWithNoStatsIsLeftAlone(t *testing.T) {
	held := []Member{{UserID: 7, GroupID: 3}, {UserID: 8, GroupID: 2}}
	// Only member 8 has figures, and they still qualify.
	plan := planPromotions(ladder(), held, map[int64]pluginapi.MemberStats{
		8: stats(20*gb, 10*gb, 2.0, 400),
	}, promoNow)
	for _, ch := range plan {
		if ch.UserID == 7 {
			t.Errorf("member 7 has no stats and was planned %+v", ch)
		}
	}
}
