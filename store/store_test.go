package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// --- test doubles ---

// memStore is an in-memory Store for the transaction tests. ClaimStock /
// RestoreStock mirror the SQL guards in store_pg.go exactly so the
// compensation logic is exercised against the same semantics.
type memStore struct {
	items     map[int]*Item
	purchases []Purchase
	claimErr  error // injected failure for ClaimStock
	recordErr error // injected failure for RecordPurchase
}

func newMemStore(items ...*Item) *memStore {
	m := &memStore{items: map[int]*Item{}}
	for _, it := range items {
		m.items[it.ID] = it
	}
	return m
}

func (m *memStore) ListItems(_ context.Context, activeOnly bool) ([]*Item, error) {
	var out []*Item
	for _, it := range m.items {
		if !activeOnly || it.Active {
			out = append(out, it)
		}
	}
	return out, nil
}
func (m *memStore) GetItem(_ context.Context, id int) (*Item, error) {
	if it, ok := m.items[id]; ok {
		return it, nil
	}
	return nil, errors.New("not found")
}
func (m *memStore) CreateItem(_ context.Context, it *Item) error { m.items[it.ID] = it; return nil }
func (m *memStore) UpdateItem(_ context.Context, it *Item) error { m.items[it.ID] = it; return nil }
func (m *memStore) DeleteItem(_ context.Context, id int) error   { delete(m.items, id); return nil }

func (m *memStore) ClaimStock(_ context.Context, id int) (bool, error) {
	if m.claimErr != nil {
		return false, m.claimErr
	}
	it := m.items[id]
	if it.Stock < 0 { // unlimited
		return true, nil
	}
	if it.Stock > 0 {
		it.Stock--
		return true, nil
	}
	return false, nil
}
func (m *memStore) RestoreStock(_ context.Context, id int) error {
	if it := m.items[id]; it != nil && it.Stock >= 0 {
		it.Stock++
	}
	return nil
}
func (m *memStore) RecordPurchase(_ context.Context, userID, itemID, pointsSpent int) error {
	if m.recordErr != nil {
		return m.recordErr
	}
	m.purchases = append(m.purchases, Purchase{UserID: userID, ItemID: itemID, PointsSpent: pointsSpent})
	return nil
}

type fakePoints struct {
	balance     int
	deductCalls int
	refundCalls int
	deductErr   error
}

func (f *fakePoints) svc() core.PointsService {
	return core.NewPoints(core.PointsAdapter{
		BalanceFn: func(context.Context, int64) (int, error) { return f.balance, nil },
		DeductFn: func(_ context.Context, _ int64, n int, _, _ string, _ int64) (int, error) {
			f.deductCalls++
			if f.deductErr != nil {
				return 0, f.deductErr
			}
			f.balance -= n
			return f.balance, nil
		},
		RefundFn: func(_ context.Context, _ int64, n int, _, _ string, _ int64) (int, error) {
			f.refundCalls++
			f.balance += n
			return f.balance, nil
		},
	})
}

type fakeGranter struct {
	calls int
	name  string
	err   error
}

func (g *fakeGranter) GrantRank(context.Context, int, int, time.Duration) (string, error) {
	g.calls++
	if g.err != nil {
		return "", g.err
	}
	return g.name, nil
}

func newHandlers(store Store, pts *fakePoints, gr *fakeGranter) *Handlers {
	return &Handlers{
		store:   store,
		points:  pts.svc(),
		granter: gr,
		errs:    core.NewErrorReporter(core.ErrorAdapter{}),
	}
}

func rankItem() *Item {
	return &Item{ID: 1, Name: "VIP", PointsCost: 100, RewardType: "rank", RewardRef: "5", RewardDays: 30, Stock: 3, Active: true}
}

// --- validation ---

func TestValidItem(t *testing.T) {
	base := func() *Item {
		return &Item{Name: "VIP", PointsCost: 100, RewardType: "rank", RewardRef: "5"}
	}
	cases := []struct {
		name    string
		mutate  func(*Item)
		wantErr bool
	}{
		{"valid rank", func(*Item) {}, false},
		{"missing name", func(i *Item) { i.Name = "" }, true},
		{"zero cost", func(i *Item) { i.PointsCost = 0 }, true},
		{"negative cost", func(i *Item) { i.PointsCost = -5 }, true},
		{"unknown reward type", func(i *Item) { i.RewardType = "freeleech" }, true},
		{"non-numeric rank ref", func(i *Item) { i.RewardRef = "gold" }, true},
		{"zero rank ref", func(i *Item) { i.RewardRef = "0" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := base()
			tc.mutate(it)
			err := validItem(it)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validItem() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestInStock(t *testing.T) {
	for _, tc := range []struct {
		stock int
		want  bool
	}{{-1, true}, {0, false}, {1, true}, {99, true}} {
		if got := (&Item{Stock: tc.stock}).InStock(); got != tc.want {
			t.Errorf("InStock(stock=%d)=%v, want %v", tc.stock, got, tc.want)
		}
	}
}

// --- transaction ---

func TestPurchaseHappyPath(t *testing.T) {
	it := rankItem()
	mem := newMemStore(it)
	pts := &fakePoints{balance: 500}
	gr := &fakeGranter{name: "VIP"}
	h := newHandlers(mem, pts, gr)

	reward, err := h.purchase(context.Background(), 42, it)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if reward != "VIP rank" {
		t.Errorf("reward=%q, want %q", reward, "VIP rank")
	}
	if it.Stock != 2 {
		t.Errorf("stock=%d, want 2 (one claimed)", it.Stock)
	}
	if pts.balance != 400 {
		t.Errorf("balance=%d, want 400 (100 deducted)", pts.balance)
	}
	if gr.calls != 1 {
		t.Errorf("granter calls=%d, want 1", gr.calls)
	}
	if len(mem.purchases) != 1 {
		t.Errorf("recorded purchases=%d, want 1", len(mem.purchases))
	}
}

func TestPurchaseUnlimitedStock(t *testing.T) {
	it := rankItem()
	it.Stock = -1
	mem := newMemStore(it)
	h := newHandlers(mem, &fakePoints{balance: 500}, &fakeGranter{name: "VIP"})

	if _, err := h.purchase(context.Background(), 42, it); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if it.Stock != -1 {
		t.Errorf("unlimited stock changed to %d", it.Stock)
	}
}

func TestPurchaseOutOfStock(t *testing.T) {
	it := rankItem()
	it.Stock = 0
	mem := newMemStore(it)
	pts := &fakePoints{balance: 500}
	gr := &fakeGranter{name: "VIP"}
	h := newHandlers(mem, pts, gr)

	_, err := h.purchase(context.Background(), 42, it)
	if !errors.Is(err, errOutOfStock) {
		t.Fatalf("err=%v, want errOutOfStock", err)
	}
	if pts.deductCalls != 0 {
		t.Errorf("points deducted for a sold-out item (%d calls)", pts.deductCalls)
	}
	if gr.calls != 0 {
		t.Errorf("reward granted for a sold-out item")
	}
}

func TestPurchaseInsufficientPoints(t *testing.T) {
	it := rankItem()
	mem := newMemStore(it)
	pts := &fakePoints{balance: 500, deductErr: core.ErrInsufficientPoints}
	gr := &fakeGranter{name: "VIP"}
	h := newHandlers(mem, pts, gr)

	_, err := h.purchase(context.Background(), 42, it)
	if !errors.Is(err, core.ErrInsufficientPoints) {
		t.Fatalf("err=%v, want ErrInsufficientPoints", err)
	}
	if it.Stock != 3 {
		t.Errorf("stock=%d, want 3 (claimed unit restored)", it.Stock)
	}
	if gr.calls != 0 {
		t.Errorf("reward granted despite insufficient points")
	}
	if len(mem.purchases) != 0 {
		t.Errorf("purchase recorded despite insufficient points")
	}
}

func TestPurchaseGrantFailureRefunds(t *testing.T) {
	it := rankItem()
	mem := newMemStore(it)
	pts := &fakePoints{balance: 500}
	gr := &fakeGranter{err: errors.New("rank not found")}
	h := newHandlers(mem, pts, gr)

	_, err := h.purchase(context.Background(), 42, it)
	if err == nil {
		t.Fatal("expected grant failure, got nil")
	}
	if pts.deductCalls != 1 || pts.refundCalls != 1 {
		t.Errorf("deduct=%d refund=%d, want 1/1 (deduct then refund)", pts.deductCalls, pts.refundCalls)
	}
	if pts.balance != 500 {
		t.Errorf("balance=%d, want 500 (net-zero after refund)", pts.balance)
	}
	if it.Stock != 3 {
		t.Errorf("stock=%d, want 3 (claimed unit restored)", it.Stock)
	}
	if len(mem.purchases) != 0 {
		t.Errorf("purchase recorded despite grant failure")
	}
}

// A failed audit-ledger write must NOT roll back the completed economic
// transaction (points already spent, rank already granted).
func TestPurchaseRecordFailureStillSucceeds(t *testing.T) {
	it := rankItem()
	mem := newMemStore(it)
	mem.recordErr = errors.New("db down")
	pts := &fakePoints{balance: 500}
	gr := &fakeGranter{name: "VIP"}
	h := newHandlers(mem, pts, gr)

	reward, err := h.purchase(context.Background(), 42, it)
	if err != nil {
		t.Fatalf("purchase should succeed despite record failure: %v", err)
	}
	if reward != "VIP rank" {
		t.Errorf("reward=%q, want %q", reward, "VIP rank")
	}
	if pts.refundCalls != 0 {
		t.Errorf("points refunded on audit-ledger failure — economic tx wrongly rolled back")
	}
}

func TestGrantRewardUnknownType(t *testing.T) {
	h := newHandlers(newMemStore(), &fakePoints{}, &fakeGranter{})
	_, err := h.grantReward(context.Background(), 1, &Item{ID: 1, RewardType: "mystery"})
	if err == nil {
		t.Fatal("expected error for unknown reward type")
	}
}
