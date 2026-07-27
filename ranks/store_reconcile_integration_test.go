//go:build integration

package ranks

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// What survived the mirror removal. This file was the dual-write and Reconcile
// suite; both are gone with the legacy tables (ENTITLEMENTS.md Stage 3.4), and
// their tests went with them. This one stays because it is not about the
// mirror at all: it pins the CASE in AddMember that stops a permanent
// membership being handed an expiry.

func TestAddMember_KeepsAPermanentMembershipPermanent(t *testing.T) {
	st, db := storeFixture(t)
	ctx := context.Background()

	staff := &Group{Name: "Staff", Slug: "staff", Kind: "assigned", Visible: false, DurationDays: 30}
	if err := st.CreateGroup(ctx, staff); err != nil {
		t.Fatalf("create staff: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+testSchema+`.group_members (user_id, group_id, expires_at)
	                      VALUES (901, $1, NULL)`, staff.ID); err != nil {
		t.Fatalf("seed permanent: %v", err)
	}

	if err := st.AddMember(ctx, 901, staff.ID, 30*24*time.Hour); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	var exp sql.NullTime
	if err := db.QueryRow(`SELECT expires_at FROM `+testSchema+
		`.group_members WHERE user_id=901 AND group_id=$1`, staff.ID).Scan(&exp); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if exp.Valid {
		t.Errorf("a permanent membership was given an expiry (%s) — GREATEST swallowed the NULL", exp.Time)
	}
}

// The critical one. An old pod mid-rolling-deploy EXTENDS an existing
// subscription in the legacy table only. The import must ADOPT the later
// expiry: skipping it as "already present" lets the mirror push stamp the stale
// expiry back over it, and the renewal the user paid for is gone.
