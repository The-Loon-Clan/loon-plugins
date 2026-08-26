package agent

import (
	"context"
	"errors"
	"testing"
)

// A gate retrofitted onto a LIVE capability must default to today's
// behaviour. Hosts have run agents for years; failing closed on an absent
// seam would switch off a working fleet the moment they upgraded. This is
// deliberately the opposite of tracker.access, which gated a capability from
// birth and so fails closed.
func TestNilSeamAllows(t *testing.T) {
	SetDeps(Deps{})
	if !allowed(context.Background(), 5) {
		t.Error("a host that has not wired CanUseAgents lost its fleet on upgrade")
	}
}

func TestSeamDecides(t *testing.T) {
	SetDeps(Deps{
		CanUseAgents: func(_ context.Context, uid int) (bool, error) { return uid == 7, nil },
	})
	if !allowed(context.Background(), 7) {
		t.Error("an entitled member was refused")
	}
	if allowed(context.Background(), 8) {
		t.Error("an unentitled member was allowed")
	}
}

// An error is a refusal. The seam exists to answer this question, and a seam
// that failed has not answered it — guessing "yes" would hand out the
// capability precisely when the decider is broken.
func TestSeamErrorRefuses(t *testing.T) {
	SetDeps(Deps{
		CanUseAgents: func(_ context.Context, _ int) (bool, error) {
			return true, errors.New("entitlements service down")
		},
	})
	if allowed(context.Background(), 7) {
		t.Error("an errored check was treated as permission, and it returned true " +
			"alongside the error — exactly the shape that must not open the door")
	}
}

// The host grants it and the plugin checks it, so the string cannot drift.
func TestEntitlementKeyIsStable(t *testing.T) {
	if EntitlementKey != "agent.use" {
		t.Errorf("EntitlementKey = %q — a host granting the old string would "+
			"entitle members to something nothing checks", EntitlementKey)
	}
}
