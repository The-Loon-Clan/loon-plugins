//go:build integration

package ranks

import (
	"context"
	"testing"
)

// Re-parenting is the one catalog write with real invariants: a cycle would
// make entitlement resolution loop, and an over-deep chain violates the CHECK.
// The BEFORE trigger keeps child.depth = parent.depth + 1 for a single insert,
// but a MOVE reads a depth that is about to change — so these exercise the
// advisory-locked walk in SetParent, not the trigger.

func TestSetParent_BuildsAChainAndRestampsDescendants(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	for _, m := range [][2]int{{3, 2}, {4, 3}} {
		p := m[1]
		if err := st.SetParent(ctx, m[0], &p); err != nil {
			t.Fatalf("SetParent(%d -> %d): %v", m[0], p, err)
		}
	}
	for id, want := range map[int]int{2: 0, 3: 1, 4: 2} {
		g, err := st.Group(ctx, id)
		if err != nil {
			t.Fatalf("Group(%d): %v", id, err)
		}
		if g.Depth != want {
			t.Errorf("group %d depth = %d, want %d", id, g.Depth, want)
		}
	}

	// Moving the middle of the chain must re-stamp everything under it, not
	// just the row that was written.
	if err := st.SetParent(ctx, 3, nil); err != nil {
		t.Fatalf("detach: %v", err)
	}
	g3, _ := st.Group(ctx, 3)
	g4, _ := st.Group(ctx, 4)
	if g3.Depth != 0 {
		t.Errorf("detached group depth = %d, want 0", g3.Depth)
	}
	if g4.Depth != 1 {
		t.Errorf("descendant depth = %d, want 1 — the subtree was not re-stamped", g4.Depth)
	}
}

func TestSetParent_RejectsCyclesAndOverDeepChains(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	two, three, four, five := 2, 3, 4, 5
	for _, m := range []struct{ child, parent int }{{3, two}, {4, three}, {5, four}} {
		p := m.parent
		if err := st.SetParent(ctx, m.child, &p); err != nil {
			t.Fatalf("chain %d->%d: %v", m.child, p, err)
		}
	}
	// 2(0) -> 3(1) -> 4(2) -> 5(3): the chain is now at the depth limit.

	if err := st.SetParent(ctx, 2, &two); err != ErrParentCycle {
		t.Errorf("self-parent err = %v, want ErrParentCycle", err)
	}
	// The direct loop: 2 under its own descendant.
	if err := st.SetParent(ctx, 2, &five); err != ErrParentCycle {
		t.Errorf("descendant-parent err = %v, want ErrParentCycle", err)
	}
	// A fifth level is one past the limit, and it must be refused with a
	// usable error rather than a raw CHECK violation.
	if err := st.SetParent(ctx, 1, &five); err != ErrParentTooDeep {
		t.Errorf("over-deep err = %v, want ErrParentTooDeep", err)
	}
	// And nothing was written by the rejected attempts.
	g1, _ := st.Group(ctx, 1)
	if g1.ParentID != nil {
		t.Errorf("a rejected move still re-parented the group: %v", g1.ParentID)
	}
}

// Moving a group that HAS descendants must account for the subtree's height,
// or a child lands past the limit and fails on the CHECK instead of here.
func TestSetParent_RejectsAMoveThatWouldPushADescendantTooDeep(t *testing.T) {
	st, _ := storeFixture(t)
	ctx := context.Background()

	three, four := 3, 4
	if err := st.SetParent(ctx, 4, &three); err != nil { // 3(0) -> 4(1)
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetParent(ctx, 5, &four); err != nil { // -> 5(2)
		t.Fatalf("seed: %v", err)
	}
	two := 2
	if err := st.SetParent(ctx, 2, nil); err != nil {
		t.Fatalf("detach 2: %v", err)
	}
	one := 1
	if err := st.SetParent(ctx, 2, &one); err != nil { // 1(0) -> 2(1)
		t.Fatalf("seed 2 under 1: %v", err)
	}
	// Moving 3 (height 2) under 2 (depth 1) would put 5 at depth 4.
	if err := st.SetParent(ctx, 3, &two); err != ErrParentTooDeep {
		t.Errorf("err = %v, want ErrParentTooDeep for a subtree that would overflow", err)
	}
}
