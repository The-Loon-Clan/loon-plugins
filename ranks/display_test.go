package ranks

import (
	"context"
	"testing"
	"time"
)

// GroupDisplay is the read half of the model, and its contract carries one
// hard rule the consumers must never have to think about: an invisible group
// grants but never shows. These pin that, plus the ordering the legacy
// GetActiveSubscription had, and the batch shape that keeps the comment-author
// path off an N+1.

func newDisplay(t *testing.T) (*groupDisplay, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &groupDisplay{store: st}, st
}

func TestBadgesFor_ExcludesHiddenGroups(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	paid := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, Color: "success", DurationDays: 30})
	staff := seedGroup(t, st, &Group{Name: "Staff", Slug: "staff", Kind: "assigned",
		Visible: false, Color: "dark", DurationDays: 30})
	for _, g := range []*Group{paid, staff} {
		if err := st.AddMember(ctx, 1764, g.ID, 24*time.Hour); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
	}

	badges, err := d.BadgesFor(ctx, 1764)
	if err != nil {
		t.Fatalf("BadgesFor: %v", err)
	}
	if len(badges) != 1 {
		t.Fatalf("got %d badges, want only the visible one: %+v", len(badges), badges)
	}
	if badges[0].Slug != "arashi" {
		t.Errorf("badge = %q, want arashi — a hidden group must never surface", badges[0].Slug)
	}
}

// Most prominent first, matching what `ORDER BY sort_order DESC LIMIT 1` gave
// the legacy readers, so a caller taking the head gets the same badge it used
// to show.
func TestBadgesFor_OrdersMostProminentFirst(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	low := seedGroup(t, st, &Group{Name: "Kirisame", Slug: "kirisame", Kind: "paid",
		Visible: true, SortOrder: 1, DurationDays: 30})
	high := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, SortOrder: 4, DurationDays: 30})
	for _, g := range []*Group{low, high} {
		if err := st.AddMember(ctx, 7, g.ID, 24*time.Hour); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
	}

	badges, err := d.BadgesFor(ctx, 7)
	if err != nil {
		t.Fatalf("BadgesFor: %v", err)
	}
	if len(badges) != 2 || badges[0].Slug != "arashi" {
		t.Fatalf("order = %+v, want the highest sort_order first", badges)
	}
}

func TestBadgesForBatch_ResolvesManyUsersAndKeepsThemApart(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	a := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, SortOrder: 4, DurationDays: 30})
	k := seedGroup(t, st, &Group{Name: "Kirisame", Slug: "kirisame", Kind: "paid",
		Visible: true, SortOrder: 1, DurationDays: 30})
	if err := st.AddMember(ctx, 1764, a.ID, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(ctx, 2141, k.ID, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	got, err := d.BadgesForBatch(ctx, []int64{1764, 2141, 9999})
	if err != nil {
		t.Fatalf("BadgesForBatch: %v", err)
	}
	if len(got[1764]) != 1 || got[1764][0].Slug != "arashi" {
		t.Errorf("user 1764 = %+v, want arashi", got[1764])
	}
	if len(got[2141]) != 1 || got[2141][0].Slug != "kirisame" {
		t.Errorf("user 2141 = %+v, want kirisame", got[2141])
	}
	// A user with no groups is absent, not an error — the contract says so.
	if len(got[9999]) != 0 {
		t.Errorf("user 9999 = %+v, want nothing", got[9999])
	}
}

// A lapsed membership must stop showing its badge. The store filters expiry,
// but this is the property a reader depends on.
func TestBadgesFor_DropsExpiredMemberships(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	g := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, DurationDays: 30})
	if err := st.AddMember(ctx, 1764, g.ID, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	st.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })

	badges, err := d.BadgesFor(ctx, 1764)
	if err != nil {
		t.Fatalf("BadgesFor: %v", err)
	}
	if len(badges) != 0 {
		t.Errorf("a lapsed membership still shows a badge: %+v", badges)
	}
}

func TestBadgesForBatch_EmptyInputIsNotAQuery(t *testing.T) {
	got, err := (&groupDisplay{store: NewMemStore()}).BadgesForBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want an empty map", got)
	}
}

// ExpiresAt is what the profile and admin rank rows render beside the badge, so
// it has to be the MEMBERSHIP's expiry, not the group's duration, and nil for a
// permanent one rather than a zero time the template would format as year 1.
func TestBadgesFor_CarriesTheMembershipExpiry(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	paid := seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, SortOrder: 4, DurationDays: 30})
	staff := seedGroup(t, st, &Group{Name: "Staff", Slug: "staff", Kind: "assigned",
		Visible: true, SortOrder: 1, DurationDays: 30})
	if err := st.AddMember(ctx, 1764, paid.ID, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Written straight into the store because no writer produces a permanent
	// membership yet: AddMember always sets NOW() + interval, so passing a zero
	// duration yields an already-expired row, not a NULL expiry. The column is
	// nullable and every read and the expiry sweep guard against NULL, so the
	// state is designed-for and the badge path has to handle it — this is the
	// only way to reach it today.
	st.members[[2]int{1764, staff.ID}] = &Member{
		UserID: 1764, GroupID: staff.ID, GrantedAt: time.Now(), Source: "assigned",
	}

	badges, err := d.BadgesFor(ctx, 1764)
	if err != nil {
		t.Fatalf("BadgesFor: %v", err)
	}
	if len(badges) != 2 {
		t.Fatalf("got %d badges, want 2: %+v", len(badges), badges)
	}
	if badges[0].Slug != "arashi" || badges[0].ExpiresAt == nil {
		t.Errorf("paid badge = %+v, want arashi with an expiry", badges[0])
	}
	if badges[1].Slug != "staff" || badges[1].ExpiresAt != nil {
		t.Errorf("permanent badge = %+v, want staff with a nil expiry", badges[1])
	}
}

// Catalog answers "what groups exist" for the Discord role map. Two rules: the
// same visibility filter as everywhere else, and no membership attached.
func TestCatalog_VisibleOnlyAndMembershipFree(t *testing.T) {
	ctx := context.Background()
	d, st := newDisplay(t)
	seedGroup(t, st, &Group{Name: "Arashi", Slug: "arashi", Kind: "paid",
		Visible: true, SortOrder: 4, Color: "warning", DurationDays: 30})
	seedGroup(t, st, &Group{Name: "Kirisame", Slug: "kirisame", Kind: "paid",
		Visible: true, SortOrder: 1, Color: "info", DurationDays: 30})
	seedGroup(t, st, &Group{Name: "Staff", Slug: "staff", Kind: "assigned",
		Visible: false, SortOrder: 9, DurationDays: 30})
	// A membership must make no difference to a catalog read.
	if err := st.AddMember(ctx, 1764, 1, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	got, err := d.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2 (staff is hidden): %+v", len(got), got)
	}
	// Most prominent first, same as BadgesFor, so a consumer never has to ask
	// which order it is looking at.
	if got[0].Slug != "arashi" || got[1].Slug != "kirisame" {
		t.Errorf("order = %q, %q; want arashi then kirisame", got[0].Slug, got[1].Slug)
	}
	if got[0].Color != "warning" {
		t.Errorf("Color = %q, want the group's badge class", got[0].Color)
	}
	for _, b := range got {
		if b.ExpiresAt != nil {
			t.Errorf("catalog entry %q carries an expiry: a group is not a membership", b.Slug)
		}
	}
}

func TestCatalog_EmptyCatalogIsNotAnError(t *testing.T) {
	got, err := (&groupDisplay{store: NewMemStore()}).Catalog(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
