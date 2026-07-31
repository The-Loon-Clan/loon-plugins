package messages

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// canSendDM decides who may START a conversation, so it is worth a test even
// though the rest of this plugin has none. It used to read
// user_rank_subscriptions directly; it now asks core for the dm.initiate
// entitlement, which moved the mod half of the rule out of this file and into
// the host's role baseline. These pin that BOTH halves still hold, because a
// regression here is silent: the compose form simply stops appearing.

// gate builds an entitlements service wired the way cmd/entitlements_wiring.go
// wires the real one — RoleMod and above hold dm.initiate through the baseline
// — so the test exercises the actual composition rather than a stand-in.
func gate(t *testing.T, roles map[int64]core.Role) (core.EntitlementsService, *core.MemEntitlementStore) {
	t.Helper()
	store := core.NewMemEntitlementStore()
	svc := core.NewEntitlements(core.EntitlementsConfig{
		Store: store,
		RoleOf: func(_ context.Context, userID int64) (core.Role, bool, error) {
			r, ok := roles[userID]
			return r, ok, nil
		},
		Baseline: map[core.Role][]core.EntitlementGrant{
			core.RoleMod: {{Key: entDMInitiate, Val: 1}},
		},
	})
	return svc, store
}

func user(id int, role core.Role) *viewer {
	return &viewer{ID: id, role: role}
}

func TestCanSendDM_ModsHoldItThroughTheRoleBaseline(t *testing.T) {
	ctx := context.Background()
	svc, _ := gate(t, map[int64]core.Role{10: core.RoleMod, 11: core.RoleAdmin})

	for _, u := range []*viewer{user(10, core.RoleMod), user(11, core.RoleAdmin)} {
		if !canSendDM(ctx, svc, u) {
			t.Errorf("user %d (role %d) cannot start a DM — the role baseline should cover mod+", u.ID, int(u.role))
		}
	}
}

func TestCanSendDM_PlainUserCannotUntilAGroupGrantsIt(t *testing.T) {
	ctx := context.Background()
	svc, _ := gate(t, map[int64]core.Role{20: core.RoleUser})
	u := user(20, core.RoleUser)

	if canSendDM(ctx, svc, u) {
		t.Fatal("a plain user could start a DM with no grant")
	}

	// What a paid tier does now: the group grants the key.
	if err := svc.Grant(ctx, 20, entDMInitiate, 1, "group:arashi", nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if !canSendDM(ctx, svc, u) {
		t.Error("a paid tier's grant did not enable DMs")
	}

	// ...and losing the membership takes it away again.
	if err := svc.Revoke(ctx, 20, entDMInitiate, "group:arashi"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if canSendDM(ctx, svc, u) {
		t.Error("DM access survived the group revoke")
	}
}

// The gate must fail CLOSED on anything it cannot answer: an anonymous request,
// and a host that never wired entitlements. Failing open here would let anyone
// start conversations.
func TestCanSendDM_FailsClosed(t *testing.T) {
	ctx := context.Background()
	svc, _ := gate(t, map[int64]core.Role{30: core.RoleUser})

	if canSendDM(ctx, svc, nil) {
		t.Error("nil user was allowed to start a DM")
	}
	if canSendDM(ctx, nil, user(30, core.RoleUser)) {
		t.Error("an unwired entitlements service allowed a DM")
	}
	// An unwired core service (no store) must also refuse rather than panic.
	if canSendDM(ctx, core.NewEntitlements(core.EntitlementsConfig{}), user(30, core.RoleUser)) {
		t.Error("an entitlements service with no store allowed a DM")
	}
}

// Two sources granting the same key must not cancel each other: a mod who also
// buys a tier keeps access when the tier lapses, because the baseline is a
// separate source from the group grant.
func TestCanSendDM_ModKeepsAccessWhenTheirPaidTierLapses(t *testing.T) {
	ctx := context.Background()
	svc, _ := gate(t, map[int64]core.Role{40: core.RoleMod})
	u := user(40, core.RoleMod)

	if err := svc.Grant(ctx, 40, entDMInitiate, 1, "group:arashi", nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := svc.Revoke(ctx, 40, entDMInitiate, "group:arashi"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !canSendDM(ctx, svc, u) {
		t.Error("revoking the group grant took the mod's baseline access with it")
	}
}
