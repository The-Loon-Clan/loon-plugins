package ranks

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The grant path is write-only and inert in production, so these tests are the
// only thing asserting it is CORRECT before Stage 3 makes something depend on
// it. core's in-memory entitlement store is the double: it enforces the same
// (user, key, source) row identity and the same MAX composition as production,
// so the resolution rules are exercised rather than restated.

func newSync(t *testing.T) (*entSync, *MemStore, core.EntitlementsService) {
	t.Helper()
	st := NewMemStore()
	svc := core.NewEntitlements(core.EntitlementsConfig{Store: core.NewMemEntitlementStore()})
	return &entSync{ents: svc, store: st}, st, svc
}

func TestEffectiveGrants_InheritsAlongTheParentChain(t *testing.T) {
	base := &Group{ID: 1, Slug: "base", Grants: map[string]int64{
		entDownloadDaily: 100, entDMInitiate: 1}}
	mid := &Group{ID: 2, Slug: "mid", ParentID: &base.ID, Grants: map[string]int64{
		entDownloadDaily: 500}}
	top := &Group{ID: 3, Slug: "top", ParentID: &mid.ID, Grants: map[string]int64{
		entAPIDaily: 9000}}
	byID := map[int]*Group{1: base, 2: mid, 3: top}

	got := effectiveGrants(byID, 3)
	// Inherited from two levels up...
	if got[entDMInitiate] != 1 {
		t.Errorf("%s = %d, want 1 inherited from the root", entDMInitiate, got[entDMInitiate])
	}
	// ...the more generous value wins, and a child raising a parent's limit
	// must not be lowered back by the ancestor it inherits from.
	if got[entDownloadDaily] != 500 {
		t.Errorf("%s = %d, want the child's more generous 500", entDownloadDaily, got[entDownloadDaily])
	}
	if got[entAPIDaily] != 9000 {
		t.Errorf("%s = %d, want the group's own 9000", entAPIDaily, got[entAPIDaily])
	}
}

// A malformed chain must terminate. The schema prevents cycles, but a resolver
// that spins on one is a hang rather than an error.
func TestEffectiveGrants_TerminatesOnACycle(t *testing.T) {
	a := &Group{ID: 1, Slug: "a", Grants: map[string]int64{entDownloadDaily: 10}}
	b := &Group{ID: 2, Slug: "b", Grants: map[string]int64{entAPIDaily: 20}}
	a.ParentID, b.ParentID = &b.ID, &a.ID // impossible via SetParent; assert anyway
	byID := map[int]*Group{1: a, 2: b}

	done := make(chan map[string]int64, 1)
	go func() { done <- effectiveGrants(byID, 1) }()
	select {
	case got := <-done:
		if got[entDownloadDaily] != 10 || got[entAPIDaily] != 20 {
			t.Errorf("resolved %v, want both levels visited once", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("effectiveGrants spun on a cycle")
	}
}

func TestGrantMembership_WritesTheGroupsKeys(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid", Visible: true,
		DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 5000, entDMInitiate: 1}})
	exp := time.Now().Add(30 * 24 * time.Hour)

	if err := e.grantMembership(ctx, 1764, g.ID, &exp); err != nil {
		t.Fatalf("grantMembership: %v", err)
	}
	if !svc.Has(ctx, 1764, entDMInitiate) {
		t.Error("the paid tier did not confer dm.initiate")
	}
	if got := svc.Limit(ctx, 1764, entDownloadDaily, 100); got != 5000 {
		t.Errorf("download.daily = %d, want 5000", got)
	}
	// Another user must be untouched.
	if svc.Has(ctx, 9999, entDMInitiate) {
		t.Error("the grant leaked to a non-member")
	}
}

func TestRevokeMembership_RemovesWhatTheGroupConferred(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid", Visible: true,
		DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 5000, entDMInitiate: 1}})
	exp := time.Now().Add(time.Hour)
	if err := e.grantMembership(ctx, 1764, g.ID, &exp); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if err := e.revokeMembership(ctx, 1764, g.ID, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if svc.Has(ctx, 1764, entDMInitiate) {
		t.Error("dm.initiate survived the revoke")
	}
	if got := svc.Limit(ctx, 1764, entDownloadDaily, 100); got != 100 {
		t.Errorf("download.daily = %d, want the caller default back", got)
	}
}

// Two groups conferring the same key must not clobber each other: the source
// tag keeps them as separate rows, and losing one leaves the other standing.
func TestGrants_FromTwoGroupsComposeAndSurviveEachOthersRevoke(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	small := seedGroup(t, st, &Group{Name: "Kirisame", Slug: "kirisame", Kind: "paid",
		Visible: true, DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 150}})
	big := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 5000}})
	exp := time.Now().Add(time.Hour)
	_ = e.grantMembership(ctx, 7, small.ID, &exp)
	_ = e.grantMembership(ctx, 7, big.ID, &exp)

	if got := svc.Limit(ctx, 7, entDownloadDaily, 100); got != 5000 {
		t.Errorf("limit = %d, want the most generous group's 5000", got)
	}
	if err := e.revokeMembership(ctx, 7, big.ID, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := svc.Limit(ctx, 7, entDownloadDaily, 100); got != 150 {
		t.Errorf("limit = %d, want the remaining group's 150 — the other group's revoke took both", got)
	}
}

// The case core cannot express: a key REMOVED from a group. There is no way to
// enumerate a user's grants, so the removed set must be passed in explicitly or
// the stale grant lingers until its expiry.
func TestResyncGroup_RevokesAKeyTheGroupNoLongerConfers(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid", Visible: true,
		DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 5000, entDMInitiate: 1}})
	if err := st.AddMember(ctx, 1764, g.ID, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := e.rebuildAll(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !svc.Has(ctx, 1764, entDMInitiate) {
		t.Fatal("setup: the member should hold dm.initiate")
	}

	// The admin drops the DM ability from the tier.
	g.Grants = map[string]int64{entDownloadDaily: 5000}
	if err := st.UpdateGroup(ctx, g); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if err := e.resyncGroup(ctx, g.ID, []string{entDMInitiate}); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if svc.Has(ctx, 1764, entDMInitiate) {
		t.Error("a key removed from the group is still granted to its member")
	}
	if got := svc.Limit(ctx, 1764, entDownloadDaily, 100); got != 5000 {
		t.Errorf("the surviving key was lost too: %d", got)
	}
}

// Raising a parent's limit has to reach the members of its CHILDREN, since
// entitlements inherit and nothing else would notice.
func TestResyncGroup_ReachesDescendantMembers(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	parent := seedGroup(t, st, &Group{Name: "Base", Slug: "base", Kind: "paid", Visible: true,
		DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 100}})
	child := seedGroup(t, st, &Group{Name: "Child", Slug: "child", Kind: "paid", Visible: true,
		DurationDays: 30, ParentID: &parent.ID, Grants: map[string]int64{}})
	if err := st.AddMember(ctx, 55, child.ID, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := e.rebuildAll(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := svc.Limit(ctx, 55, entDownloadDaily, 1); got != 100 {
		t.Fatalf("setup: inherited limit = %d, want 100", got)
	}

	parent.Grants[entDownloadDaily] = 9000
	if err := st.UpdateGroup(ctx, parent); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if err := e.resyncGroup(ctx, parent.ID, nil); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if got := svc.Limit(ctx, 55, entDownloadDaily, 1); got != 9000 {
		t.Errorf("child member's inherited limit = %d, want the parent's new 9000", got)
	}
}

func TestRebuildAll_IsIdempotentAndSkipsExpired(t *testing.T) {
	ctx := context.Background()
	e, st, svc := newSync(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid", Visible: true,
		DurationDays: 30, Grants: map[string]int64{entDownloadDaily: 5000}})
	if err := st.AddMember(ctx, 1764, g.ID, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	n1, err := e.rebuildAll(ctx)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	n2, err := e.rebuildAll(ctx)
	if err != nil || n1 != n2 {
		t.Errorf("rebuild is not idempotent: %d then %d (%v)", n1, n2, err)
	}
	if got := svc.Limit(ctx, 1764, entDownloadDaily, 100); got != 5000 {
		t.Errorf("limit after replay = %d, want 5000", got)
	}

	// A lapsed membership must not be rebuilt into a live grant.
	st.SetClock(func() time.Time { return time.Now().Add(60 * 24 * time.Hour) })
	fresh := core.NewEntitlements(core.EntitlementsConfig{Store: core.NewMemEntitlementStore()})
	e2 := &entSync{ents: fresh, store: st}
	if _, err := e2.rebuildAll(ctx); err != nil {
		t.Fatalf("rebuild after expiry: %v", err)
	}
	if fresh.Limit(ctx, 1764, entDownloadDaily, 100) != 100 {
		t.Error("an expired membership was rebuilt into a live grant")
	}
}
