package pluginapi

import "testing"

// TestZeroIsNeverAnOwner is the whole point of the function.
//
// Both halves matter and they are different failures. An anonymous viewer
// carries 0, so `owner == viewer` hands them any record whose owner is 0. And
// 0 is the reserved system id, so a record that ends up owned by the system is
// exactly the one that would be handed over.
func TestZeroIsNeverAnOwner(t *testing.T) {
	if OwnedBy(0, 0) {
		t.Error("an anonymous viewer owns a record owned by nobody")
	}
	if OwnedBy(int64(0), int64(0)) {
		t.Error("same, in int64")
	}
	for _, owner := range []int64{0, 1, 7, -1} {
		if OwnedBy(owner, 0) {
			t.Errorf("viewer 0 owns a record owned by %d", owner)
		}
	}
	// A negative viewer id should not sneak through either — it is not a real
	// member, and `> 0` is the check rather than `!= 0` for that reason.
	if OwnedBy(int64(-5), int64(-5)) {
		t.Error("a negative viewer id matched itself")
	}
}

func TestOwnedByMatchesARealOwner(t *testing.T) {
	if !OwnedBy(int64(7), int64(7)) {
		t.Error("a member does not own their own record")
	}
	if OwnedBy(int64(7), int64(8)) {
		t.Error("a member owns somebody else's record")
	}
}

// TestOwnedByWorksForEveryIdTypeInTheRepo. tickets stores a user id as int,
// most plugins use int64; a helper that only took one would push a conversion
// to every call site, which is one more place to get it wrong.
func TestOwnedByWorksForEveryIdTypeInTheRepo(t *testing.T) {
	if !OwnedBy(7, 7) {
		t.Error("int")
	}
	if !OwnedBy(int32(7), int32(7)) {
		t.Error("int32")
	}
	if !OwnedBy(int64(7), int64(7)) {
		t.Error("int64")
	}
	type userID int64 // a named type, which ~int64 admits
	if !OwnedBy(userID(7), userID(7)) {
		t.Error("a named id type")
	}
}

// TestVisibleToKeepsPrivilegeSeparateFromIdentity. Encoding "staff" as a magic
// id is what made comments.Delete remove anybody's comment: the sentinel that
// meant staff was also the id that meant nobody.
func TestVisibleToKeepsPrivilegeSeparateFromIdentity(t *testing.T) {
	// Staff see it whoever they are, including with no id resolved.
	if !VisibleTo(int64(7), int64(0), true) {
		t.Error("a privileged viewer was refused")
	}
	if !VisibleTo(int64(7), int64(8), true) {
		t.Error("a privileged non-owner was refused")
	}
	// And privilege is the ONLY way a non-owner gets in.
	if VisibleTo(int64(7), int64(8), false) {
		t.Error("a stranger saw somebody else's record")
	}
	if VisibleTo(int64(0), int64(0), false) {
		t.Error("an anonymous viewer saw a record owned by nobody")
	}
	if !VisibleTo(int64(7), int64(7), false) {
		t.Error("the owner was refused their own record")
	}
}
