package ranks

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// An earned rank must be PERMANENT, and this file exists because it was not.
//
// The promotion sweep grants with a zero duration and says, in a comment, that
// this means permanent: "an earned class is held for as long as it is earned,
// and the sweep is what takes it away. A duration here would expire a rank the
// member still qualifies for and re-grant it on the next pass." That was a
// description of intent, not of behaviour. Zero fell through to the timed path,
// where the interval is clamped UP to one hour, so the two hourly jobs formed a
// loop: Rank Promotion granted, Rank Expiry deleted an hour later, Rank
// Promotion granted again — with a fresh "promote" row written to the member's
// own audit trail on every lap, forever, and the badge disappearing in whatever
// slice of the hour fell between the two ticks.
//
// Nothing errored, both jobs reported success, and the only visible symptom was
// an audit trail nobody reads growing by one row per earned member per hour.
// That is the shape of bug a test has to pin, because a person will not notice
// it.

// The mock is the right level here despite the bug living in SQL. What is being
// asserted is the CONTRACT — dur <= 0 means a NULL expiry — and the two stores
// must agree on it or every unit test of the sweep is written against semantics
// production does not have. The PGStore half is covered by
// earned_ladder_integration_test.go against a real database.
func TestAnEarnedRankIsPermanentAndSurvivesTheExpirySweep(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	g := &Group{Slug: "uploader", Name: "Uploader", Kind: "earned",
		Visible: true, DurationDays: 30, MinReleases: 5, MinAgeDays: 30}
	if err := st.CreateGroup(ctx, g); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Exactly what promote.go does when it promotes somebody.
	if err := st.AddMember(ctx, 604, g.ID, 0); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	held, err := st.MembershipsOfUsers(ctx, []int{604})
	if err != nil || len(held) != 1 {
		t.Fatalf("MembershipsOfUsers = %+v, %v; want the new membership", held, err)
	}
	if held[0].ExpiresAt != nil {
		t.Fatalf("earned membership expires at %v; an earned rank is held for as long "+
			"as it is earned and must carry a NULL expiry", *held[0].ExpiresAt)
	}

	// Wind the clock past any plausible clamp. One hour was the real value; a
	// year makes the test independent of what the clamp happens to be.
	st.SetClock(func() time.Time { return time.Now().Add(365 * 24 * time.Hour) })
	expired, err := st.ExpireMemberships(ctx)
	if err != nil {
		t.Fatalf("ExpireMemberships: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("the expiry sweep took %d earned membership(s); it must spare permanent ones "+
			"or the promotion job spends every hour re-granting what it granted last hour", len(expired))
	}
}

// The steady state, stated as the property that actually matters: re-running
// the sweep over an unchanged world must decide to do NOTHING.
//
// planPromotions is pure, so this is the whole loop minus the store writes —
// and it is the assertion that catches the churn from the other end. When the
// membership survived only an hour, the second pass saw a member holding
// nothing and planned the same promotion again; every hour, for every earned
// member, one more audit row.
func TestASecondSweepOverAnUnchangedWorldPlansNothing(t *testing.T) {
	groups := contributorLadder()
	// Three real shapes from the site this was written against: the largest
	// contributor, a mid-ladder one, and somebody sitting exactly on the bottom
	// rung's threshold. All long-tenured, so age is not what is being tested.
	stats := map[int64]pluginapi.MemberStats{
		604: contributed(6663, 400),
		102: contributed(4740, 400),
		813: contributed(5, 400),
	}

	first := planPromotions(groups, nil, stats, promoNow)
	if len(first) == 0 {
		t.Fatal("the first sweep planned nothing; the fixture is not exercising promotion")
	}

	// Apply what the first sweep decided, as permanent memberships — which is
	// what AddMember(…, 0) now writes.
	var members []Member
	for _, ch := range first {
		if ch.Add != 0 {
			members = append(members, Member{UserID: ch.UserID, GroupID: ch.Add, Source: "purchase"})
		}
	}

	second := planPromotions(groups, members, stats, promoNow)
	if len(second) != 0 {
		t.Errorf("a second sweep over an unchanged world planned %d change(s): %+v\n"+
			"a settled ladder must be a no-op, or every tick writes history for members who have not moved",
			len(second), second)
	}
}
