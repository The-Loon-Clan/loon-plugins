//go:build integration

package ranks

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// BadgeData exists purely to make the display path cheap enough to sit on a
// page render, so what needs proving is that it bought that cheapness WITHOUT
// changing an answer. Both halves are properties of the SQL — that the two
// reads compose the same way inside one transaction, and that skipping
// loadGrants leaves the rows otherwise intact — so they need a real database.

// The parity case: BadgeData must agree with the two readers it replaced. If
// it ever drifts, badges drift with it and nothing else would catch it.
func TestBadgeData_MatchesTheTwoReadersItReplaced(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	// Two users, one in a mirrored paid group and one in a hidden staff group.
	// Both memberships carry an expiry: AddMember is NOW() + interval and no
	// writer produces a NULL one yet, so the zero duration below is an
	// already-lapsed row rather than a permanent membership. That is still the
	// second shape worth covering here — a hidden group and a member whose
	// membership the filters must drop.
	staff := &Group{Name: "Staff", Slug: "staff", Kind: "assigned", Visible: false,
		Color: "dark", DurationDays: 30, SortOrder: 9}
	if err := st.CreateGroup(ctx, staff); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := st.AddMember(ctx, 1764, 5, 30*24*time.Hour); err != nil { // Arashi, seeded
		t.Fatalf("AddMember paid: %v", err)
	}
	if err := st.AddMember(ctx, 2141, staff.ID, 0); err != nil { // lapses immediately
		t.Fatalf("AddMember staff: %v", err)
	}

	users := []int{1764, 2141, 9999} // 9999 holds nothing

	wantMembers, err := st.MembershipsOfUsers(ctx, users)
	if err != nil {
		t.Fatalf("MembershipsOfUsers: %v", err)
	}
	wantGroups, err := st.Groups(ctx)
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}

	gotMembers, gotGroups, err := st.BadgeData(ctx, users)
	if err != nil {
		t.Fatalf("BadgeData: %v", err)
	}

	if !reflect.DeepEqual(gotMembers, wantMembers) {
		t.Errorf("memberships differ from MembershipsOfUsers\n got: %+v\nwant: %+v", gotMembers, wantMembers)
	}
	if len(gotGroups) != len(wantGroups) {
		t.Fatalf("catalog length = %d, want %d", len(gotGroups), len(wantGroups))
	}
	// Everything except Grants must match, in the same order — BadgesForBatch
	// indexes by id but the ordering is what makes the catalog read stable.
	for i := range gotGroups {
		g, w := gotGroups[i], wantGroups[i]
		g.Grants, w.Grants = nil, nil
		if !reflect.DeepEqual(g, w) {
			t.Errorf("group %d differs\n got: %+v\nwant: %+v", i, g, w)
		}
	}
}

// The cheapness case, stated as an observable: BadgeData must NOT have run
// loadGrants, while Groups still must. Asserting the empty map is the only
// way to see the skipped query from the outside, and it doubles as the
// contract the interface documents.
func TestBadgeData_SkipsGrantsButGroupsStillLoadsThem(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	if err := st.AddMember(ctx, 1764, 5, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	_, catalog, err := st.BadgeData(ctx, []int{1764})
	if err != nil {
		t.Fatalf("BadgeData: %v", err)
	}
	for _, g := range catalog {
		if len(g.Grants) != 0 {
			t.Errorf("group %q came back with %d grants; the display path must not pay for them",
				g.Slug, len(g.Grants))
		}
	}

	// The entitlement path is the other consumer of the catalog and it depends
	// on Grants being populated, so prove the skip is local to BadgeData.
	groups, err := st.Groups(ctx)
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	var withGrants int
	for _, g := range groups {
		if len(g.Grants) > 0 {
			withGrants++
		}
	}
	if withGrants == 0 {
		t.Error("Groups returned no grants at all — the entitlement sync would grant nothing")
	}
}

// The end-to-end shape: badges resolved through the capability over a real
// database, including the two rules a consumer must never have to think about
// (hidden groups never show, most-prominent-first).
func TestBadgesForBatch_OverPostgres(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()
	d := &groupDisplay{store: st}

	hidden := &Group{Name: "Staff", Slug: "staff", Kind: "assigned", Visible: false,
		Color: "dark", DurationDays: 30, SortOrder: 99}
	if err := st.CreateGroup(ctx, hidden); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	// Both a visible paid group and the hidden one, so precedence and the
	// visibility filter are exercised together: sort_order 99 would win if the
	// filter were missing.
	if err := st.AddMember(ctx, 1764, 5, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember paid: %v", err)
	}
	if err := st.AddMember(ctx, 1764, hidden.ID, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember hidden: %v", err)
	}

	byUser, err := d.BadgesForBatch(ctx, []int64{1764, 9999})
	if err != nil {
		t.Fatalf("BadgesForBatch: %v", err)
	}
	badges := byUser[1764]
	if len(badges) != 1 {
		t.Fatalf("got %d badges, want 1 (the hidden group must not surface): %+v", len(badges), badges)
	}
	if badges[0].Slug != "arashi" {
		t.Errorf("badge = %q, want arashi", badges[0].Slug)
	}
	if badges[0].TitleColor == "" {
		t.Error("TitleColor is empty — it is the only field the comment-author path reads")
	}
	if len(byUser[9999]) != 0 {
		t.Errorf("user with no memberships got %+v", byUser[9999])
	}
}

// MemberHistory is the store half of the audit capability, and the deleted-group
// case is the part that needs a real database: the row must survive its group
// with a NULL FK, which is a property of the ON DELETE SET NULL constraint
// rather than of the Go.
func TestMemberHistory_RowSurvivesItsDeletedGroup(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	doomed := &Group{Name: "Temporary", Slug: "temporary", Kind: "paid", Visible: true,
		Color: "info", DurationDays: 30, SortOrder: 2}
	if err := st.CreateGroup(ctx, doomed); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := st.RecordHistory(ctx, 1764, &doomed.ID, "purchased", "bought the temporary tier"); err != nil {
		t.Fatalf("RecordHistory: %v", err)
	}
	// Newer row against a group that stays, to prove ordering across the two.
	if err := st.RecordHistory(ctx, 1764, intPtr(5), "extended", "renewed Arashi"); err != nil {
		t.Fatalf("RecordHistory: %v", err)
	}

	if err := st.DeleteGroup(ctx, doomed.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	got, err := st.MemberHistory(ctx, 1764, 0)
	if err != nil {
		t.Fatalf("MemberHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 — deleting a group must not erase its audit trail: %+v", len(got), got)
	}
	if got[0].Action != "extended" || got[0].GroupName != "Arashi" {
		t.Errorf("newest = %+v, want the Arashi extension", got[0])
	}
	if got[1].Action != "purchased" {
		t.Fatalf("oldest = %+v, want the purchase", got[1])
	}
	if got[1].GroupName != "" || got[1].GroupSlug != "" {
		t.Errorf("deleted group still resolved to %q/%q; the FK should be NULL now",
			got[1].GroupName, got[1].GroupSlug)
	}
	if got[1].Details != "bought the temporary tier" {
		t.Errorf("Details = %q, want the recorded note", got[1].Details)
	}
}

func intPtr(i int) *int { return &i }
