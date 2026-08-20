//go:build integration

package comments

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi/pgtest"
)

// The comments store against a real Postgres.
//
// Every authorisation rule in this file lives inside a statement's WHERE — that
// is the plugin's stated design, on the grounds that "a check that happens in a
// separate query is a check with a gap in it". It is the right call and it puts
// the rules somewhere no unit test can see them. These are the ones that decide
// whether a member can rewrite or remove somebody else's words.
//
// The withheld body is deliberately NOT tested here, because the store does not
// withhold it: `List` returns a removed comment WITH its text, and the view's
// switch decides who sees it (staff do; a moderator asked "why did you remove
// this" cannot answer from a row that shows nothing). That split is recorded at
// the bottom of this file, since "the store returns the body of deleted
// comments" reads like a bug until you know it is load-bearing.

func testStore(t *testing.T) *PGStore {
	t.Helper()
	return NewPGStore(pgtest.SchemaDB(t, "comments_store_test", migrations))
}

const (
	author = int64(7)
	rando  = int64(8)
	mod    = int64(99)
	kind   = "release"
	subj   = int64(1234)
)

func post(t *testing.T, s *PGStore, user int64, body string) int64 {
	t.Helper()
	id, err := s.Add(context.Background(), Comment{
		SubjectKind: kind, SubjectID: subj, UserID: user, Body: body,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return id
}

// ── Delete: staff widens the check, it does not skip it ─────────────

// TestAMemberCannotRemoveSomebodyElsesComment. The id on that form is a number
// in a URL; nothing but this WHERE stops it being any other comment's number.
func TestAMemberCannotRemoveSomebodyElsesComment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "something worth keeping")

	ok, err := s.Delete(ctx, id, rando, false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok {
		t.Error("a member removed a comment that was not theirs")
	}
	c, _, _ := s.Get(ctx, id)
	if c.Deleted() {
		t.Error("the comment was withheld anyway")
	}
}

func TestAnAuthorCanRemoveTheirOwn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "on reflection, no")

	if ok, err := s.Delete(ctx, id, author, false); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true, nil", ok, err)
	}
	c, found, _ := s.Get(ctx, id)
	if !found {
		t.Fatal("the row is gone; this is meant to be a SOFT delete")
	}
	if !c.Deleted() {
		t.Error("deleted_at was not set")
	}
	if c.DeletedBy == nil || *c.DeletedBy != author {
		t.Errorf("deleted_by = %v, want the author", c.DeletedBy)
	}
}

// TestStaffCanRemoveAnybodys — the widening half of the same statement.
func TestStaffCanRemoveAnybodys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "over the line")

	if ok, err := s.Delete(ctx, id, mod, true); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true, nil", ok, err)
	}
	c, _, _ := s.Get(ctx, id)
	if c.DeletedBy == nil || *c.DeletedBy != mod {
		t.Errorf("deleted_by = %v, want the moderator — this is the column that "+
			"tells an author changing their mind from a moderator acting", c.DeletedBy)
	}
}

// TestRemovingTwiceChangesNothing. The `deleted_at IS NULL` guard is what keeps
// the first remover's attribution: two moderators opening the same queue would
// otherwise have the second overwrite the first, and the answer to "who removed
// this and when" would quietly be wrong.
func TestRemovingTwiceChangesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "over the line")

	if ok, _ := s.Delete(ctx, id, mod, true); !ok {
		t.Fatal("first delete failed")
	}
	first, _, _ := s.Get(ctx, id)

	ok, err := s.Delete(ctx, id, mod+1, true)
	if err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if ok {
		t.Error("a second delete reported success; false is how the caller learns somebody got there first")
	}
	second, _, _ := s.Get(ctx, id)
	if second.DeletedBy == nil || *second.DeletedBy != mod {
		t.Errorf("deleted_by = %v, want it unchanged at %d", second.DeletedBy, mod)
	}
	if !second.DeletedAt.Equal(*first.DeletedAt) {
		t.Error("deleted_at moved on the second attempt")
	}
}

// TestDeleteWithNoSignedInUser is the anonymous case, and it is the reason this
// test exists rather than the rule being assumed.
//
// The statement reads `($3 = 0 OR user_id = $3)`, where $3 is 0 for staff so
// that the clause becomes "any author". A NON-staff caller whose user id is
// also 0 therefore takes the same branch — so if an unauthenticated request
// ever reached this method, it would remove anybody's comment.
//
// The routes are gated, so nothing can call it that way today. This pins the
// behaviour so that if it ever changes — in either direction — somebody has to
// look at it deliberately.
func TestDeleteWithNoSignedInUser(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "somebody else's words")

	ok, err := s.Delete(ctx, id, 0, false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	c, _, _ := s.Get(ctx, id)
	if ok || c.Deleted() {
		t.Errorf("an anonymous caller (user id 0) removed another member's comment "+
			"(ok=%v deleted=%v). The sentinel that means \"staff\" and the id that "+
			"means \"nobody\" are the same value; the routes are what stops this, "+
			"and that is now one layer of protection rather than two.", ok, c.Deleted())
	}
}

// ── Edit: the author, and only while it stands ──────────────────────

func TestOnlyTheAuthorCanEdit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "original")

	if ok, err := s.Edit(ctx, id, rando, "rewritten by somebody else"); err != nil || ok {
		t.Errorf("Edit = %v, %v — a member rewrote another's comment", ok, err)
	}
	c, _, _ := s.Get(ctx, id)
	if c.Body != "original" {
		t.Errorf("body = %q, want it untouched", c.Body)
	}
	if c.EditedAt != nil {
		t.Error("edited_at was stamped by a refused edit")
	}
}

// TestEditingARemovedCommentIsRefused. Without the `deleted_at IS NULL` clause
// an author could edit a moderated comment back into view, undoing a moderator
// with nothing recorded anywhere.
func TestEditingARemovedCommentIsRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "over the line")

	if ok, _ := s.Delete(ctx, id, mod, true); !ok {
		t.Fatal("delete failed")
	}
	if ok, err := s.Edit(ctx, id, author, "actually this is fine now"); err != nil || ok {
		t.Errorf("Edit = %v, %v — edited a removed comment back into existence", ok, err)
	}
	c, _, _ := s.Get(ctx, id)
	if c.Body != "over the line" {
		t.Errorf("body = %q; the removed text was rewritten", c.Body)
	}
	if !c.Deleted() {
		t.Error("the comment is no longer marked removed")
	}
}

func TestAnAuthorCanEditTheirOwn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "original")

	if ok, err := s.Edit(ctx, id, author, "  second thoughts  "); err != nil || !ok {
		t.Fatalf("Edit = %v, %v; want true, nil", ok, err)
	}
	c, _, _ := s.Get(ctx, id)
	if c.Body != "second thoughts" {
		t.Errorf("body = %q, want it trimmed", c.Body)
	}
	if c.EditedAt == nil {
		t.Error("edited_at was not stamped — a comment that changed after people " +
			"replied to it is a different comment, and hiding that is how a thread stops making sense")
	}
}

// ── List: the shape of a thread ─────────────────────────────────────

// TestListKeepsRemovedCommentsInPlace. The soft delete exists so a thread keeps
// its shape: "this was removed" between two replies is legible, where a vanished
// row makes the replies read as non-sequiturs.
func TestListKeepsRemovedCommentsInPlace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := post(t, s, author, "first")
	middle := post(t, s, rando, "middle")
	post(t, s, author, "last")

	if ok, _ := s.Delete(ctx, middle, mod, true); !ok {
		t.Fatal("delete failed")
	}

	rows, err := s.List(ctx, kind, subj, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("List returned %d comments, want 3 — the removed one must hold its place", len(rows))
	}
	if rows[0].ID != first || !rows[1].Deleted() {
		t.Errorf("thread order or removal flag is wrong: %+v", rows)
	}
	// The store hands the body over; the VIEW is what withholds it, and only
	// from non-staff. Recorded rather than asserted-away: if this ever starts
	// coming back empty, the staff "why was this removed" path breaks and the
	// only symptom is a moderator seeing nothing.
	if rows[1].Body != "middle" {
		t.Errorf("body = %q; the store is meant to return it and let the view decide "+
			"— staff see what was said", rows[1].Body)
	}
}

// TestListIsScopedToItsSubject. The key is (subject_kind, subject_id), and the
// kind is the half that is easy to drop: a release and a series can share an id.
func TestListIsScopedToItsSubject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	post(t, s, author, "about the release")
	if _, err := s.Add(ctx, Comment{
		SubjectKind: "series", SubjectID: subj, UserID: author, Body: "about the series",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add(ctx, Comment{
		SubjectKind: kind, SubjectID: subj + 1, UserID: author, Body: "another release",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	rows, err := s.List(ctx, kind, subj, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Body != "about the release" {
		t.Errorf("List returned %d rows: %+v — a same-numbered subject of another kind leaked in", len(rows), rows)
	}
}

// ── Thanks ──────────────────────────────────────────────────────────

// TestThanksToggle — twice is off, and the count is per comment.
func TestThanksToggle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "useful")

	on, _, err := s.Toggle(ctx, id, rando)
	if err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if !on {
		t.Error("the first thanks did not register")
	}
	counts, mine, err := s.ThanksFor(ctx, []int64{id}, rando)
	if err != nil {
		t.Fatalf("ThanksFor: %v", err)
	}
	if counts[id] != 1 || !mine[id] {
		t.Errorf("counts=%v mine=%v, want one thanks from this viewer", counts, mine)
	}

	if on, _, err = s.Toggle(ctx, id, rando); err != nil || on {
		t.Errorf("Toggle = %v, %v; thanking twice must take it back", on, err)
	}
	if counts, mine, _ = s.ThanksFor(ctx, []int64{id}, rando); counts[id] != 0 || mine[id] {
		t.Errorf("counts=%v mine=%v after untoggling", counts, mine)
	}
}

// TestThanksAreOnePerMember — the unique constraint, not application logic.
func TestThanksAreOnePerMember(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := post(t, s, author, "useful")

	for _, u := range []int64{rando, mod, rando} { // rando twice
		if _, _, err := s.Toggle(ctx, id, u); err != nil {
			t.Fatalf("Toggle(%d): %v", u, err)
		}
	}
	// rando thanked, un-thanked; mod thanked. One left.
	counts, _, err := s.ThanksFor(ctx, []int64{id}, 0)
	if err != nil {
		t.Fatalf("ThanksFor: %v", err)
	}
	if counts[id] != 1 {
		t.Errorf("count = %d, want 1", counts[id])
	}
}
