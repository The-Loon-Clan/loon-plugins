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

// The contribution ladder: an operator's four rungs on releases and tenure, for
// a host with no tracker at all. Every byte criterion is zero here, so these
// tests fail if the release rule were ever quietly ANDed with an upload floor.
func contributorLadder() []Group {
	return []Group{
		{ID: 11, Slug: "member", Kind: "earned", SortOrder: 10, MinAgeDays: 7},
		{ID: 12, Slug: "uploader", Kind: "earned", SortOrder: 20, MinReleases: 5, MinAgeDays: 14},
		{ID: 13, Slug: "contributor", Kind: "earned", SortOrder: 30, MinReleases: 50, MinAgeDays: 90},
		{ID: 14, Slug: "archivist", Kind: "earned", SortOrder: 40, MinReleases: 500, MinAgeDays: 365},
	}
}

// contributed is a member with a PROVEN release count and nothing else.
func contributed(releases, ageDays int) pluginapi.MemberStats {
	s := stats(0, 0, 0, ageDays)
	s.ReleasesContributed = pluginapi.ReleaseCount(releases)
	return s
}

// unproven is the same member on a host that publishes no count at all — which
// is NOT the same as one proven to be zero, and the difference is the point.
func unproven(ageDays int) pluginapi.MemberStats { return stats(0, 0, 0, ageDays) }

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

// Releases are ANDed with the criteria beside them, exactly like the others.
//
// The operator's ladder is releases PLUS tenure, and the pair has to be a
// conjunction in both directions: somebody who publishes fifty releases in their
// first week has not served the time, and somebody who has been here a year
// without publishing has not done the work.
func TestReleasesAndAgeAreConjunctive(t *testing.T) {
	contributor := contributorLadder()[2] // 50 releases, 90 days
	for _, tc := range []struct {
		name string
		s    pluginapi.MemberStats
		want bool
	}{
		{"meets both", contributed(80, 200), true},
		{"exactly on both thresholds", contributed(50, 90), true},
		{"releases but too new", contributed(80, 89), false},
		{"old enough but too few releases", contributed(49, 400), false},
		{"neither", contributed(0, 1), false},
		// Absent is not zero, and it is not "old enough either": a host that
		// publishes no count leaves this rung unearned no matter the tenure.
		{"no count published, plenty of age", unproven(4000), false},
	} {
		if got := contributor.Qualifies(tc.s, promoNow); got != tc.want {
			t.Errorf("%s: Qualifies = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A rung gated ONLY on releases is a complete, gated rule.
//
// This is the whole ladder on a host with no tracker, so Automatic has to say
// yes to it. If it did not, the group would sit in the catalog looking earned,
// promote nobody, and the sweep would report "no earned group has criteria set"
// while staring straight at one.
func TestARungGatedOnReleasesAloneIsGated(t *testing.T) {
	rung := Group{ID: 20, Slug: "publisher", Kind: "earned", SortOrder: 15, MinReleases: 10}
	if !rung.Automatic() {
		t.Fatal("a group asking for 10 releases and nothing else was not treated as gated")
	}
	for _, tc := range []struct {
		name string
		s    pluginapi.MemberStats
		want bool
	}{
		{"over the threshold", contributed(11, 0), true},
		{"exactly on it", contributed(10, 0), true},
		{"one short", contributed(9, 0), false},
		{"proven zero", contributed(0, 0), false},
		{"never published by the host", unproven(0), false},
	} {
		if got := rung.Qualifies(tc.s, promoNow); got != tc.want {
			t.Errorf("%s: Qualifies = %v, want %v", tc.name, got, tc.want)
		}
	}
	// And a group with NO criteria at all is still not automatic — adding a
	// fourth dimension must not have turned the zero-threshold guard off.
	if (Group{ID: 21, Kind: "earned", SortOrder: 99}).Automatic() {
		t.Error("an earned group with no criteria at all became automatic")
	}
}

// The tracker-era rung: releases AND bytes, and it needs both.
//
// Kept possible on purpose. When the BT tracker lands, a rank can ask a member
// to have both contributed releases and seeded them, and neither half alone can
// carry somebody into it — which is only true because 004 added a criterion
// beside the byte ones rather than replacing them.
func TestATrackerEraRungNeedsReleasesAndBytesTogether(t *testing.T) {
	rung := Group{ID: 30, Slug: "seeder-publisher", Kind: "earned", SortOrder: 50,
		MinReleases: 10, MinUploaded: 100 * gb, MinRatio: 1.0}

	releasesOnly := contributed(50, 400) // no bytes at all
	if rung.Qualifies(releasesOnly, promoNow) {
		t.Error("50 releases and zero bytes qualified for a rung that also demands 100 GB")
	}
	bytesOnly := stats(200*gb, 100*gb, 2.0, 400) // no count published
	if rung.Qualifies(bytesOnly, promoNow) {
		t.Error("200 GB with no release count qualified for a rung that also demands 10 releases")
	}
	// Proven zero is no better than unproven here, which is the half a host
	// could get wrong by defaulting the figure instead of omitting it.
	bytesAndNoReleases := bytesOnly
	bytesAndNoReleases.ReleasesContributed = pluginapi.ReleaseCount(0)
	if rung.Qualifies(bytesAndNoReleases, promoNow) {
		t.Error("200 GB and a proven zero releases qualified for a rung demanding 10")
	}

	both := stats(200*gb, 100*gb, 2.0, 400)
	both.ReleasesContributed = pluginapi.ReleaseCount(10)
	if !rung.Qualifies(both, promoNow) {
		t.Error("a member meeting every criterion did NOT qualify")
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

// The operator's ladder end to end: a member climbs it on releases and tenure.
func TestTheContributionLadderIsClimbedOnReleasesAndAge(t *testing.T) {
	g := contributorLadder()
	for _, tc := range []struct {
		name string
		s    pluginapi.MemberStats
		want string
	}{
		{"a week old, nothing published", contributed(0, 7), "member"},
		{"five releases, two weeks", contributed(5, 14), "uploader"},
		{"fifty releases but only two weeks", contributed(50, 14), "uploader"},
		{"fifty releases, three months", contributed(50, 90), "contributor"},
		{"five hundred releases, a year", contributed(500, 365), "archivist"},
		{"brand new with nothing", contributed(0, 1), ""},
		// TENURE MUST NOT CARRY ANYONE PAST THE COUNT. Both of these clear every
		// age threshold on the ladder, including the archivist's year, and both
		// stop where their releases run out. Without the release criterion they
		// would sit at the top rung, which is the whole failure this adds.
		{"years old, only five releases", contributed(5, 400), "uploader"},
		{"years old, no count published", unproven(4000), "member"},
	} {
		best, ok := bestClass(g, tc.s, promoNow)
		got := ""
		if ok {
			got = best.Slug
		}
		if got != tc.want {
			t.Errorf("%s: landed on %q, want %q", tc.name, got, tc.want)
		}
	}
}

// AN ABSENT COUNT NEVER PROMOTES, and this is the case the pointer exists for.
//
// The member below has been here for years, so the tenure-only bottom rung is
// theirs. Every rung above it asks for releases, and on a host publishing no
// count they must all stay out of reach — the failure being guarded against is a
// missing figure defaulting to zero and then reading as "a satisfied criterion"
// somewhere up the chain.
func TestAnAbsentReleaseCountNeverPromotes(t *testing.T) {
	g := contributorLadder()
	plan := planPromotions(g, nil, map[int64]pluginapi.MemberStats{
		7: unproven(4000),
	}, promoNow)
	if len(plan) != 1 || plan[0].Add != 11 {
		t.Fatalf("a member with no published count was planned %+v, want only the tenure rung (11)", plan)
	}

	// The other end of failing closed, asserted rather than left to be
	// discovered: a member ALREADY on a release-gated rung is deranked when the
	// count stops arriving, because an unsatisfied criterion is what takes a rank
	// back. That is why pluginapi tells a host which cannot read the figures to
	// omit the MEMBER — the case the test below covers — instead of reporting
	// them with the count missing.
	held := []Member{{UserID: 7, GroupID: 13}}
	plan = planPromotions(g, held, map[int64]pluginapi.MemberStats{
		7: unproven(4000),
	}, promoNow)
	if len(plan) != 1 || len(plan[0].Drop) != 1 || plan[0].Drop[0] != 13 || plan[0].Add != 11 {
		t.Fatalf("a contributor whose count stopped arriving was planned %+v, want drop 13 / add 11", plan)
	}

	// Omitted entirely: left exactly where they were.
	plan = planPromotions(g, held, map[int64]pluginapi.MemberStats{}, promoNow)
	if len(plan) != 0 {
		t.Fatalf("a member omitted from the stats map was planned %+v, want no change", plan)
	}
}
