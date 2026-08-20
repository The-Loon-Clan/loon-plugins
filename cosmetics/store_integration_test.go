//go:build integration

package cosmetics

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/pluginapi/pgtest"
)

// The cosmetics store against a real Postgres.
//
// Everything below is decided IN SQL and none of it is reachable from a unit
// test. Equip's ownership check is a SELECT inside the INSERT; expiry is a
// NOW() comparison; "buying it again extends rather than truncates" is a CASE
// inside an ON CONFLICT. A struct-cloning in-memory double can only restate
// what its author believed those did — which is precisely the belief worth
// checking, since the first of them is what stands between a forged form post
// and wearing an effect somebody else paid for.
//
// Verified by hand once, against a running site, on one afternoon. This is that
// afternoon, repeatable.

func testStore(t *testing.T) *PGStore {
	t.Helper()
	return NewPGStore(pgtest.SchemaDB(t, "cosmetics_store_test", migrations))
}

const (
	member  = int64(7)
	other   = int64(8)
	nameFX  = "name"
	avatar  = "avatar"
	glow    = "glow-gold"
	sparkle = "sparkle"
)

// ── Equip: the ownership check ──────────────────────────────────────

// TestEquipRefusesWhatIsNotOwned is the one that matters. The slot and slug on
// that form are whatever the poster typed.
func TestEquipRefusesWhatIsNotOwned(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ok, err := s.Equip(ctx, member, nameFX, glow)
	if err != nil {
		t.Fatalf("Equip: %v", err)
	}
	if ok {
		t.Error("equipped an effect the member does not own")
	}
	if got, _ := s.EquippedBy(ctx, member, nameFX); got != "" {
		t.Errorf("EquippedBy = %q after a refused equip; the row was written anyway", got)
	}
}

// TestEquipRefusesSomebodyElsesUnlock — the unlock exists, just not theirs.
// A check keyed on the slug alone would pass this.
func TestEquipRefusesSomebodyElsesUnlock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Unlock(ctx, other, glow, "store", 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if ok, err := s.Equip(ctx, member, nameFX, glow); err != nil || ok {
		t.Errorf("Equip = %v, %v — wore an unlock belonging to another member", ok, err)
	}
}

func TestEquipAcceptsWhatIsOwned(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Unlock(ctx, member, glow, "store", 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if ok, err := s.Equip(ctx, member, nameFX, glow); err != nil || !ok {
		t.Fatalf("Equip = %v, %v; want true, nil", ok, err)
	}
	if got, err := s.EquippedBy(ctx, member, nameFX); err != nil || got != glow {
		t.Errorf("EquippedBy = %q, %v; want %q", got, err, glow)
	}
}

// TestEquipRefusesAnExpiredUnlock. A VIP effect whose term ran out is still a
// row in cosmetic_owned — the expiry lives in the WHERE, not in a sweep.
func TestEquipRefusesAnExpiredUnlock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Straight to the table: Unlock cannot express "expired an hour ago", and
	// the point here is the read side.
	expireInThePast(t, s, member, glow, -time.Hour)

	if ok, err := s.Equip(ctx, member, nameFX, glow); err != nil || ok {
		t.Errorf("Equip = %v, %v — wore an unlock that had lapsed", ok, err)
	}
	if owns, err := s.Owns(ctx, member, glow); err != nil || owns {
		t.Errorf("Owns = %v, %v; a lapsed unlock is not owned", owns, err)
	}
	if owned, err := s.OwnedBy(ctx, member); err != nil || len(owned) != 0 {
		t.Errorf("OwnedBy returned %d rows; a lapsed unlock must not be listed", len(owned))
	}
}

// TestOneEffectPerSlot — the ON CONFLICT (user_id, slot) target. Two effects on
// one slot is a rendering with two effects and no way to choose.
func TestEquipReplacesWithinASlot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, slug := range []string{glow, sparkle} {
		if err := s.Unlock(ctx, member, slug, "store", 0); err != nil {
			t.Fatalf("Unlock %s: %v", slug, err)
		}
		if ok, err := s.Equip(ctx, member, nameFX, slug); err != nil || !ok {
			t.Fatalf("Equip %s: %v, %v", slug, ok, err)
		}
	}
	if got, _ := s.EquippedBy(ctx, member, nameFX); got != sparkle {
		t.Errorf("EquippedBy = %q, want the most recently equipped (%q)", got, sparkle)
	}
	live, err := s.LiveEquipped(ctx)
	if err != nil {
		t.Fatalf("LiveEquipped: %v", err)
	}
	if n := len(live[member]); n != 1 {
		t.Errorf("member holds %d effects in one slot, want 1: %v", n, live[member])
	}
}

// TestSlotsAreIndependent — replacing a name effect must not disturb an avatar
// frame. The conflict target is (user_id, slot), not (user_id).
func TestSlotsAreIndependent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, slug := range []string{glow, sparkle} {
		if err := s.Unlock(ctx, member, slug, "store", 0); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}
	mustEquip(t, s, member, nameFX, glow)
	mustEquip(t, s, member, avatar, sparkle)

	if got, _ := s.EquippedBy(ctx, member, nameFX); got != glow {
		t.Errorf("name slot = %q, want %q", got, glow)
	}
	if got, _ := s.EquippedBy(ctx, member, avatar); got != sparkle {
		t.Errorf("avatar slot = %q, want %q", got, sparkle)
	}
}

// TestEquipWithAnEmptySlugTakesItOff, and only from the slot named — the
// unequip path is a DELETE with its own WHERE and is easy to get too wide.
func TestEquipWithAnEmptySlugTakesItOff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, slug := range []string{glow, sparkle} {
		if err := s.Unlock(ctx, member, slug, "store", 0); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}
	mustEquip(t, s, member, nameFX, glow)
	mustEquip(t, s, member, avatar, sparkle)

	if ok, err := s.Equip(ctx, member, nameFX, ""); err != nil || !ok {
		t.Fatalf("unequip: %v, %v", ok, err)
	}
	if got, _ := s.EquippedBy(ctx, member, nameFX); got != "" {
		t.Errorf("name slot = %q after unequipping", got)
	}
	if got, _ := s.EquippedBy(ctx, member, avatar); got != sparkle {
		t.Errorf("avatar slot = %q — unequipping one slot cleared another", got)
	}
}

// TestUnequippingSomebodyElseIsNotPossible. The DELETE is keyed on user_id;
// this is here because an unequip that ignored it would be silent.
func TestUnequipTouchesOnlyTheCaller(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Unlock(ctx, other, glow, "store", 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	mustEquip(t, s, other, nameFX, glow)

	if _, err := s.Equip(ctx, member, nameFX, ""); err != nil {
		t.Fatalf("unequip: %v", err)
	}
	if got, _ := s.EquippedBy(ctx, other, nameFX); got != glow {
		t.Errorf("the other member's slot = %q, want %q — one member's unequip cleared another's", got, glow)
	}
}

// ── LiveEquipped: expiry without a sweep ────────────────────────────

// TestLapsedUnlockStopsRenderingButKeepsTheChoice is the design decision this
// table's JOIN encodes, and the reason there is no expiry sweep: the equipped
// row STAYS when an unlock lapses, so renewing brings the effect back exactly
// as it was rather than leaving the member to set it up again.
func TestLapsedUnlockStopsRenderingButKeepsTheChoice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Unlock(ctx, member, glow, "vip", 30); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	mustEquip(t, s, member, nameFX, glow)

	live, err := s.LiveEquipped(ctx)
	if err != nil {
		t.Fatalf("LiveEquipped: %v", err)
	}
	if live[member][nameFX] != glow {
		t.Fatalf("a live unlock is not rendering: %v", live)
	}

	// The term runs out.
	expireInThePast(t, s, member, glow, -time.Minute)

	if live, err = s.LiveEquipped(ctx); err != nil {
		t.Fatalf("LiveEquipped: %v", err)
	}
	if _, still := live[member]; still {
		t.Errorf("a lapsed effect is still rendering: %v", live)
	}
	// But the choice survives, which is the half a sweep would destroy.
	if got, _ := s.EquippedBy(ctx, member, nameFX); got != glow {
		t.Errorf("EquippedBy = %q; the equipped row must survive so a renewal restores it", got)
	}

	// Renew, and it comes back without the member touching anything.
	if err := s.Unlock(ctx, member, glow, "vip", 30); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if live, err = s.LiveEquipped(ctx); err != nil {
		t.Fatalf("LiveEquipped: %v", err)
	}
	if live[member][nameFX] != glow {
		t.Errorf("renewing did not restore the effect: %v", live)
	}
}

// ── Unlock: the arithmetic in the ON CONFLICT ───────────────────────

// TestBuyingAgainExtendsRatherThanTruncates. Somebody with three weeks left who
// buys another thirty days should have seven weeks, not thirty days — the
// CASE adds the new run to what remains. Getting this backwards takes time
// people paid for, and it does so silently.
func TestBuyingAgainExtendsRatherThanTruncates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Unlock(ctx, member, glow, "store", 30); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	first := expiryOf(t, s, member, glow)
	if first == nil {
		t.Fatal("a dated purchase stored no expiry")
	}

	if err := s.Unlock(ctx, member, glow, "store", 30); err != nil {
		t.Fatalf("second purchase: %v", err)
	}
	second := expiryOf(t, s, member, glow)
	if second == nil {
		t.Fatal("the second purchase cleared the expiry")
	}
	// Roughly sixty days out, and certainly beyond the first.
	if !second.After(first.Add(20 * 24 * time.Hour)) {
		t.Errorf("expiry went %v → %v; the second thirty days were not added", first, second)
	}
}

// TestPermanentStaysPermanent. A NULL expiry is absorbing in both directions:
// a dated grant on top of a permanent one must not put an end date on it, and
// a permanent grant on top of a dated one must clear the date.
func TestPermanentIsAbsorbing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Permanent, then dated.
	if err := s.Unlock(ctx, member, glow, "store", 0); err != nil {
		t.Fatalf("permanent: %v", err)
	}
	if err := s.Unlock(ctx, member, glow, "promo", 7); err != nil {
		t.Fatalf("dated on top: %v", err)
	}
	if exp := expiryOf(t, s, member, glow); exp != nil {
		t.Errorf("a dated grant put an end date (%v) on something already owned outright", exp)
	}

	// Dated, then permanent.
	if err := s.Unlock(ctx, other, sparkle, "promo", 7); err != nil {
		t.Fatalf("dated: %v", err)
	}
	if err := s.Unlock(ctx, other, sparkle, "store", 0); err != nil {
		t.Fatalf("permanent on top: %v", err)
	}
	if exp := expiryOf(t, s, other, sparkle); exp != nil {
		t.Errorf("buying outright left an end date (%v)", exp)
	}
}

// TestUnlockDoesNotDuplicate — the conflict target is (user_id, slug), so
// buying twice is one row. Two rows would make Owns and OwnedBy disagree.
func TestUnlockDoesNotDuplicate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.Unlock(ctx, member, glow, "store", 0); err != nil {
			t.Fatalf("Unlock %d: %v", i, err)
		}
	}
	owned, err := s.OwnedBy(ctx, member)
	if err != nil {
		t.Fatalf("OwnedBy: %v", err)
	}
	if len(owned) != 1 {
		t.Errorf("OwnedBy returned %d rows for one effect bought three times", len(owned))
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func mustEquip(t *testing.T, s *PGStore, user int64, slot, slug string) {
	t.Helper()
	ok, err := s.Equip(context.Background(), user, slot, slug)
	if err != nil || !ok {
		t.Fatalf("Equip(%d, %s, %s) = %v, %v", user, slot, slug, ok, err)
	}
}

// expireInThePast puts an unlock's expiry behind us. Unlock cannot express it
// — it only ever adds time — and every expiry rule needs a lapsed row to read.
func expireInThePast(t *testing.T, s *PGStore, user int64, slug string, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := s.Unlock(ctx, user, slug, "vip", 1); err != nil {
		t.Fatalf("seed unlock: %v", err)
	}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE cosmetic_owned SET expires_at = NOW() + make_interval(secs => $3)
			  WHERE user_id = $1 AND slug = $2`,
			user, slug, ago.Seconds())
		return err
	})
	if err != nil {
		t.Fatalf("age the unlock: %v", err)
	}
}

func expiryOf(t *testing.T, s *PGStore, user int64, slug string) *time.Time {
	t.Helper()
	// Into a STRUCT field rather than a []*time.Time. sqlx allocates one
	// time.Time per element and scans into it, so a NULL expiry — the exact
	// case these tests are about — fails with "unsupported Scan, storing
	// driver.Value type <nil>". A pointer field on a struct is scanned as a
	// pointer and takes the NULL.
	var rows []struct {
		ExpiresAt *time.Time `db:"expires_at"`
	}
	err := s.db.WithTx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Select(&rows,
			`SELECT expires_at FROM cosmetic_owned WHERE user_id = $1 AND slug = $2`, user, slug)
	})
	if err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row for (%d, %s), got %d", user, slug, len(rows))
	}
	return rows[0].ExpiresAt
}
