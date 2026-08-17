package pointstore

import (
	"context"
	"testing"
)

// mockStore is an in-memory Store for testing without a database.
type mockStore struct{ byUser map[int64]string }

func newMock() *mockStore { return &mockStore{byUser: map[int64]string{}} }

var _ Store = (*mockStore)(nil)

func (m *mockStore) Flair(_ context.Context, userID int64) (string, error) {
	return m.byUser[userID], nil
}
func (m *mockStore) SetFlair(_ context.Context, userID int64, flairID string) error {
	m.byUser[userID] = flairID
	return nil
}

func TestFlairCatalog(t *testing.T) {
	if _, ok := flairByID("nope"); ok {
		t.Fatal("unknown flair should not resolve")
	}
	f, ok := flairByID("vip")
	if !ok || f.Cost != 25 {
		t.Fatalf("vip = %+v ok=%v", f, ok)
	}
}

func TestSetAndReplaceFlair(t *testing.T) {
	ctx := context.Background()
	m := newMock()
	if fl, _ := m.Flair(ctx, 1); fl != "" {
		t.Fatal("no flair expected initially")
	}
	_ = m.SetFlair(ctx, 1, "supporter")
	if fl, _ := m.Flair(ctx, 1); fl != "supporter" {
		t.Fatalf("flair = %q", fl)
	}
	// buying another replaces it (one equipped flair per user)
	_ = m.SetFlair(ctx, 1, "legend")
	if fl, _ := m.Flair(ctx, 1); fl != "legend" {
		t.Fatalf("flair after replace = %q", fl)
	}
}

// The granter's two promises to the store that calls it.
//
// The refund test that lived here died with buy(): the deduct-grant-refund
// unwind is the STORE plugin's purchase path now, tested there. What this
// plugin still owes is narrower — an unknown flair id must ERROR (so the
// store refunds; equipping a guess would sell a member something they did not
// choose), and a known one must equip by replacement.
func TestEquipFlairErrorsOnUnknownAndReplacesOnKnown(t *testing.T) {
	st := newMock()
	p := &Plugin{st: st}

	if _, err := p.EquipFlair(context.Background(), 7, "no-such-flair"); err == nil {
		t.Fatal("an unknown flair id equipped something — the store would never refund")
	}
	if name, err := p.EquipFlair(context.Background(), 7, "supporter"); err != nil || name != "Supporter" {
		t.Fatalf("EquipFlair(supporter) = %q, %v", name, err)
	}
	if name, err := p.EquipFlair(context.Background(), 7, "vip"); err != nil || name != "VIP" {
		t.Fatalf("EquipFlair(vip) = %q, %v", name, err)
	}
	if got, _ := st.Flair(context.Background(), 7); got != "vip" {
		t.Fatalf("flair after two equips = %q, want vip (replacement, not accumulation)", got)
	}
}
