package messages

import (
	"context"
	"testing"
	"time"
)

// These pin the CONTRACT, not the implementation. Every rule here is one the
// Postgres store enforces in SQL, so the same expectations can be pointed at
// PGStore in an integration run — and where the two would drift, this is the
// file that says which one is wrong.

func TestThreadPairingIsCanonical(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	ab, created, err := m.EnsureDMThread(ctx, 7, 3)
	if err != nil || !created {
		t.Fatalf("first EnsureDMThread: id=%d created=%v err=%v", ab, created, err)
	}
	// The reverse ordering must land on the SAME thread, or two people can
	// hold two halves of one conversation and neither sees the other's.
	ba, created, err := m.EnsureDMThread(ctx, 3, 7)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("EnsureDMThread(3,7) created a second thread for the same pair")
	}
	if ba != ab {
		t.Errorf("pair mapped to %d and %d — a pair must map to one thread", ab, ba)
	}
	if _, _, err := m.EnsureDMThread(ctx, 5, 5); err == nil {
		t.Error("a thread with yourself was allowed")
	}
}

func TestBlocksAreSymmetric(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	if err := m.CreateDMBlock(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ a, b int }{{1, 2}, {2, 1}} {
		blocked, err := m.IsDMBlocked(ctx, c.a, c.b)
		if err != nil {
			t.Fatal(err)
		}
		if !blocked {
			t.Errorf("IsDMBlocked(%d,%d) = false — the block must hold BOTH ways, or a "+
				"blocker can block someone and keep messaging them", c.a, c.b)
		}
	}
	if err := m.CreateDMBlock(ctx, 1, 2); err != nil {
		t.Errorf("re-blocking must be idempotent: %v", err)
	}
	if err := m.RemoveDMBlock(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := m.IsDMBlocked(ctx, 1, 2); blocked {
		t.Error("still blocked after RemoveDMBlock")
	}
}

func TestUnreadCountsExcludeYourOwnMessages(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	m.Users = map[int]string{1: "alice", 2: "bob"}
	tid, _, _ := m.EnsureDMThread(ctx, 1, 2)

	if _, err := m.CreateDMMessage(ctx, tid, 1, "hello"); err != nil {
		t.Fatal(err)
	}
	// The sender must never see their own message as unread.
	if n, _ := m.CountUnreadDMs(ctx, 1); n != 0 {
		t.Errorf("sender has %d unread of their own message, want 0", n)
	}
	if n, _ := m.CountUnreadDMs(ctx, 2); n != 1 {
		t.Errorf("recipient has %d unread, want 1", n)
	}

	read, err := m.MarkDMThreadRead(ctx, tid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if read != 1 {
		t.Errorf("MarkDMThreadRead stamped %d rows, want 1", read)
	}
	// Re-reading must report zero rather than re-counting what it already
	// stamped — the caller decrements a badge by this number.
	if again, _ := m.MarkDMThreadRead(ctx, tid, 2); again != 0 {
		t.Errorf("second MarkDMThreadRead stamped %d rows, want 0", again)
	}
	if n, _ := m.CountUnreadDMs(ctx, 2); n != 0 {
		t.Errorf("recipient still has %d unread after reading", n)
	}
}

// Per-side soft delete, and the rule that makes it survivable: a new message
// brings the thread back for the person who deleted it. Without that, "delete"
// silently becomes "block" from the sender's point of view.
func TestSoftDeleteIsPerSideAndUndoneByANewMessage(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	m.Users = map[int]string{1: "alice", 2: "bob"}
	tid, _, _ := m.EnsureDMThread(ctx, 1, 2)
	if _, err := m.CreateDMMessage(ctx, tid, 1, "first"); err != nil {
		t.Fatal(err)
	}

	if err := m.SoftDeleteDMThreadForUser(ctx, tid, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.ListDMThreadsForUser(ctx, 2); len(got) != 0 {
		t.Errorf("deleter still sees %d thread(s)", len(got))
	}
	// The other side is untouched.
	if got, _ := m.ListDMThreadsForUser(ctx, 1); len(got) != 1 {
		t.Errorf("the OTHER party lost the thread too — soft delete must be one-sided")
	}

	if _, err := m.CreateDMMessage(ctx, tid, 1, "still there?"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.ListDMThreadsForUser(ctx, 2); len(got) != 1 {
		t.Error("a new message did not restore the thread for the person who deleted it")
	}
}

func TestThreadAccessIsParticipantOnly(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	tid, _, _ := m.EnsureDMThread(ctx, 1, 2)

	got, err := m.GetDMThreadForUser(ctx, tid, 3)
	if err != nil {
		t.Fatal(err)
	}
	// nil+nil, NOT an error: a distinguishable error would let a third party
	// probe whether a conversation between two others exists.
	if got != nil {
		t.Error("a non-participant read the thread")
	}
	if missing, err := m.GetDMThreadForUser(ctx, 9999, 1); err != nil || missing != nil {
		t.Errorf("absent thread = (%v, %v), want (nil, nil) — indistinguishable from 'not yours'", missing, err)
	}
	if mine, err := m.GetDMThreadForUser(ctx, tid, 1); err != nil || mine == nil {
		t.Errorf("participant could not read their own thread: %v", err)
	}
}

// The announcement half: who sees what, and the difference between read and
// dismissed.
func TestAnnouncementTargetingAndDismissal(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	if _, err := m.SendMessage(ctx, "System", "everyone", "b", "all", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendMessage(ctx, "System", "admins only", "b", "admin", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendMessage(ctx, "System", "just you", "b", userTarget(5), nil); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := m.SendMessage(ctx, "System", "expired", "b", "all", &past); err != nil {
		t.Fatal(err)
	}

	// A plain user: broadcast + their own, never the admin one, never expired.
	got, _ := m.GetMessagesForUser(ctx, 5, false)
	if len(got) != 2 {
		t.Fatalf("plain user sees %d announcement(s), want 2 (broadcast + addressed)", len(got))
	}
	// An admin also sees the admin-targeted one.
	if got, _ := m.GetMessagesForUser(ctx, 6, true); len(got) != 2 {
		t.Fatalf("admin sees %d, want 2 (broadcast + admin)", len(got))
	}

	if n, _ := m.GetUnreadCount(ctx, 5, false); n != 2 {
		t.Errorf("unread = %d, want 2", n)
	}
	// Read keeps the row in the list; only the count moves.
	if err := m.MarkMessageRead(ctx, 1, 5); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.GetUnreadCount(ctx, 5, false); n != 1 {
		t.Errorf("unread = %d after reading one, want 1", n)
	}
	if got, _ := m.GetMessagesForUser(ctx, 5, false); len(got) != 2 {
		t.Error("reading an announcement removed it from the list — that is dismiss, not read")
	}
	// Dismiss removes it for THIS viewer only.
	if err := m.DismissMessage(ctx, 1, 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetMessagesForUser(ctx, 5, false); len(got) != 1 {
		t.Error("dismissed announcement still listed")
	}
	if got, _ := m.GetMessagesForUser(ctx, 6, true); len(got) != 2 {
		t.Error("one viewer's dismissal hid the announcement from another")
	}
}

func TestGetAllMessagesPages(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	for i := 0; i < 5; i++ {
		if _, err := m.SendMessage(ctx, "System", "m", "b", "all", nil); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := m.GetAllMessages(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(page) != 2 {
		t.Errorf("page=%d total=%d, want 2 and 5 — total is UNPAGED or the composer's pager lies", len(page), total)
	}
	// An offset past the end must be empty, not a panic: page numbers arrive
	// from the query string.
	if page, _, err := m.GetAllMessages(ctx, 2, 99); err != nil || len(page) != 0 {
		t.Errorf("offset past the end = (%d rows, %v)", len(page), err)
	}
}
