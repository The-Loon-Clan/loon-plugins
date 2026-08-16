package pointstore

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
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

// A failed equip must refund the deduction.
//
// Deduct and SetFlair are two writes in two stores with no shared transaction,
// and the first version of buy() reported "Purchase failed." after the second
// write failed — with the member's points already gone. The unwind is the
// contract (the store plugin runs the same shape), so this pins it: on a
// SetFlair failure the member's balance must come back.
func TestBuyRefundsWhenEquipFails(t *testing.T) {
	ledger := &fakePoints{balance: 100}
	p := &Plugin{
		core: &core.Core{Auth: fakeAuth{}, Points: ledger, Logger: slog.Default()},
		st:   failingStore{},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/p/store/buy", strings.NewReader("flair=vip"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if _, err := p.buy(c); err != nil {
		t.Fatalf("buy returned error: %v", err)
	}
	if ledger.balance != 100 {
		t.Fatalf("balance after failed equip = %d, want 100 (deducted %d, refunded %d)",
			ledger.balance, ledger.deducted, ledger.awarded)
	}
	if ledger.deducted == 0 || ledger.awarded == 0 {
		t.Fatalf("expected a deduct AND a refund; deducted=%d awarded=%d",
			ledger.deducted, ledger.awarded)
	}
}

type fakeAuth struct{ core.AuthService }

func (fakeAuth) CurrentUser(*gin.Context) (*core.User, bool) {
	return &core.User{ID: 7}, true
}

type fakePoints struct {
	core.PointsService
	balance, deducted, awarded int
}

func (f *fakePoints) Deduct(_ context.Context, _ int64, n int, _, _ string, _ int64) (int, error) {
	f.balance -= n
	f.deducted += n
	return f.balance, nil
}
func (f *fakePoints) Award(_ context.Context, _ int64, n int, _, _ string, _ int64) (int, error) {
	f.balance += n
	f.awarded += n
	return f.balance, nil
}
func (f *fakePoints) Balance(context.Context, int64) (int, error) { return f.balance, nil }

// failingStore equips nothing, which is the failure under test.
type failingStore struct{ Store }

func (failingStore) Flair(context.Context, int64) (string, error) { return "", nil }
func (failingStore) SetFlair(context.Context, int64, string) error {
	return errors.New("boom")
}
