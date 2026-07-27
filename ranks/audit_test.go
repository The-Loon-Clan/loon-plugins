package ranks

import (
	"context"
	"testing"
	"time"
)

// The audit capability replaces the host's legacy user_rank_history query, and
// the admin table it feeds renders four fields per row. What is worth pinning
// is the ordering (newest first), the limit, and the deleted-group case — that
// last one is the whole reason the history FK is ON DELETE SET NULL, and a
// blank group name in an audit table reads as a bug.

func newAudit(t *testing.T) (*groupAudit, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &groupAudit{store: st}, st
}

func TestHistoryFor_NewestFirst(t *testing.T) {
	ctx := context.Background()
	a, st := newAudit(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, DurationDays: 30})

	for _, action := range []string{"purchased", "extended", "expired"} {
		if err := st.RecordHistory(ctx, 1764, &g.ID, action, action+" it"); err != nil {
			t.Fatalf("RecordHistory: %v", err)
		}
	}

	got, err := a.HistoryFor(ctx, 1764, 0)
	if err != nil {
		t.Fatalf("HistoryFor: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	if got[0].Action != "expired" || got[2].Action != "purchased" {
		t.Errorf("order = %q..%q, want newest (expired) first", got[0].Action, got[2].Action)
	}
	if got[0].Group != "Arashi" || got[0].GroupSlug != "arashi" {
		t.Errorf("group = %q/%q, want Arashi/arashi", got[0].Group, got[0].GroupSlug)
	}
	if got[0].Details != "expired it" {
		t.Errorf("Details = %q — the mock used to drop this field", got[0].Details)
	}
	if got[0].At.IsZero() {
		t.Error("At is zero; the admin table formats it as a date")
	}
}

func TestHistoryFor_RespectsTheLimit(t *testing.T) {
	ctx := context.Background()
	a, st := newAudit(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, DurationDays: 30})
	for i := 0; i < 5; i++ {
		if err := st.RecordHistory(ctx, 1764, &g.ID, "purchased", ""); err != nil {
			t.Fatal(err)
		}
	}

	got, err := a.HistoryFor(ctx, 1764, 2)
	if err != nil {
		t.Fatalf("HistoryFor: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2", len(got))
	}
}

// A group deleted since the row was written leaves a NULL FK, so the store
// returns an empty name. Showing a blank cell would read as a bug; the legacy
// query said "Deleted" and the admin table must keep saying it.
func TestHistoryFor_NamesADeletedGroup(t *testing.T) {
	ctx := context.Background()
	a, st := newAudit(t)

	// No group id at all is the same shape the SET NULL leaves behind.
	if err := st.RecordHistory(ctx, 1764, nil, "expired", "tier removed"); err != nil {
		t.Fatalf("RecordHistory: %v", err)
	}

	got, err := a.HistoryFor(ctx, 1764, 0)
	if err != nil {
		t.Fatalf("HistoryFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Group != deletedGroupLabel {
		t.Errorf("Group = %q, want %q", got[0].Group, deletedGroupLabel)
	}
	if got[0].GroupSlug != "" {
		t.Errorf("GroupSlug = %q, want empty — there is no group to key off", got[0].GroupSlug)
	}
}

func TestHistoryFor_NoHistoryIsEmptyNotAnError(t *testing.T) {
	a, _ := newAudit(t)

	got, err := a.HistoryFor(context.Background(), 9999, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}

// The expiry sweep and the granter both record history, so a real user's feed
// interleaves sources. This pins that HistoryFor does not care which wrote it.
func TestHistoryFor_MixesSources(t *testing.T) {
	ctx := context.Background()
	a, st := newAudit(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, DurationDays: 30})
	st.SetClock(func() time.Time { return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) })

	_ = st.RecordHistory(ctx, 1764, &g.ID, "purchased", "bought")
	_ = st.RecordHistory(ctx, 1764, &g.ID, "admin_grant", "comped")

	got, err := a.HistoryFor(ctx, 1764, 0)
	if err != nil {
		t.Fatalf("HistoryFor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Action != "admin_grant" {
		t.Errorf("newest = %q, want admin_grant", got[0].Action)
	}
}
