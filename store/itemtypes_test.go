package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// A provider shaped like charity's: it prices itself from the buyer's own
// input, writes its own ledger code, and can refuse with a sentence.
type fakeItemType struct {
	grants  int
	gotCost int
	gotBand string
	err     error
	badRef  bool
}

func (f *fakeItemType) Describe(_ context.Context, ref string) pluginapi.StoreItemTypeInfo {
	info := pluginapi.StoreItemTypeInfo{
		Kind: "charity", Label: "Charity", CostFrom: "amount",
		ButtonLabel: "Give", Reason: "spend_charity", LedgerNote: "Charity",
		Fields: []pluginapi.StoreField{
			{Name: "amount", Label: "Amount", Kind: pluginapi.StoreFieldNumber,
				Min: 1000, Max: 50000, Default: "1000"},
		},
	}
	// A def that pins its band offers no chooser — the shape depends on the
	// item, which is why Describe is given the ref.
	if ref == "" {
		info.Fields = append(info.Fields, pluginapi.StoreField{
			Name: "band", Label: "band", Kind: pluginapi.StoreFieldSelect, Default: "0.5",
			Options: []pluginapi.StoreOption{{Value: "0.5"}, {Value: "1"}},
		})
	}
	return info
}

func (f *fakeItemType) Validate(_ context.Context, ref string, _ int) error {
	if f.badRef {
		return errors.New("pick one of the offered bands")
	}
	return nil
}

func (f *fakeItemType) Grant(_ context.Context, pur pluginapi.StorePurchase) (string, error) {
	f.grants++
	f.gotCost = pur.Cost
	f.gotBand = pur.Field("band")
	if f.err != nil {
		return "", f.err
	}
	return "reached 12 members", nil
}

// coreWith registers a provider on a real Core, which is what the handlers
// scan — a double for the registry would prove nothing about the lookup.
func coreWith(t *testing.T, p pluginapi.StoreItemType) *core.Core {
	t.Helper()
	c := &core.Core{}
	if p != nil {
		if err := c.Register(pluginapi.StoreItemTypePrefix+"charity", p); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func charityItem() *Item {
	return &Item{ID: 7, Name: "Charity", PointsCost: 1, RewardType: "charity",
		Stock: -1, Active: true, Flavour: "both"}
}

// The whole point of the seam: the member's own figure is what gets debited,
// recorded and handed on, and it reaches the ledger under the provider's code
// rather than reading as a shop sale.
func TestPurchaseOfAContributedTypeIsPricedByTheBuyer(t *testing.T) {
	it := charityItem()
	mem := newMemStore(it)
	pts := &fakePoints{balance: 20000}
	prov := &fakeItemType{}
	h := newHandlers(mem, pts, &fakeGranter{})
	h.core = coreWith(t, prov)

	reward, cost, err := h.purchase(context.Background(), 42, it,
		map[string]string{"amount": "2500", "band": "1"})
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if cost != 2500 {
		t.Errorf("cost=%d, want 2500 (the buyer's amount, not the def's %d)", cost, it.PointsCost)
	}
	if pts.balance != 17500 {
		t.Errorf("balance=%d, want 17500 (2500 debited)", pts.balance)
	}
	if prov.gotCost != 2500 {
		t.Errorf("provider was handed Cost=%d, want 2500", prov.gotCost)
	}
	if prov.gotBand != "1" {
		t.Errorf("provider was handed band=%q, want the submitted 1", prov.gotBand)
	}
	if reward != "reached 12 members" {
		t.Errorf("reward=%q, want the provider's label", reward)
	}
	// The audit row is what the member paid. Recording the def's figure would
	// make every variable-cost sale a lie in the store's own ledger.
	if len(mem.purchases) != 1 || mem.purchases[0].PointsSpent != 2500 {
		t.Errorf("recorded %+v, want one purchase of 2500", mem.purchases)
	}
}

// A field the member never touched still has a value, and it must be the one
// the form showed them.
func TestPurchaseAppliesTheControlsDefaults(t *testing.T) {
	it := charityItem()
	prov := &fakeItemType{}
	h := newHandlers(newMemStore(it), &fakePoints{balance: 20000}, &fakeGranter{})
	h.core = coreWith(t, prov)

	_, cost, err := h.purchase(context.Background(), 42, it, nil)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if cost != 1000 || prov.gotBand != "0.5" {
		t.Errorf("cost=%d band=%q, want the declared defaults 1000/0.5", cost, prov.gotBand)
	}
}

// An amount outside the type's bounds is refused before anything moves, and
// the buyer is told what to change — the store's generic "purchase failed"
// would leave them guessing at a number only the provider knows.
func TestPurchaseRefusesAnOutOfBoundsAmountForFree(t *testing.T) {
	it := charityItem()
	mem := newMemStore(it)
	mem.items[it.ID].Stock = 3
	pts := &fakePoints{balance: 20000}
	prov := &fakeItemType{}
	h := newHandlers(mem, pts, &fakeGranter{})
	h.core = coreWith(t, prov)

	_, _, err := h.purchase(context.Background(), 42, it, map[string]string{"amount": "5"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if s := memberSentence(err); !strings.Contains(s, "at least 1000") {
		t.Errorf("member was told %q, want the bound", s)
	}
	if pts.deductCalls != 0 || prov.grants != 0 {
		t.Errorf("a refused purchase moved points (%d) or granted (%d)", pts.deductCalls, prov.grants)
	}
	if it.Stock != 3 {
		t.Errorf("stock=%d, want 3 — a refused purchase claimed a unit", it.Stock)
	}
}

// A provider that cannot deliver unwinds the sale exactly like a missing
// rank: the member keeps their points, at the price they actually paid.
func TestProviderFailureRefundsWhatWasPaid(t *testing.T) {
	it := charityItem()
	pts := &fakePoints{balance: 20000}
	prov := &fakeItemType{err: pluginapi.StoreRefusal("nobody currently matches that band")}
	h := newHandlers(newMemStore(it), pts, &fakeGranter{})
	h.core = coreWith(t, prov)

	_, _, err := h.purchase(context.Background(), 42, it, map[string]string{"amount": "3000"})
	if err == nil {
		t.Fatal("expected the provider's failure")
	}
	if pts.balance != 20000 {
		t.Errorf("balance=%d, want 20000 (net zero after refund)", pts.balance)
	}
	// The provider's own sentence, not the store's — it names a fact only the
	// provider knows, and the member can act on it.
	if s := memberSentence(err); s != "nobody currently matches that band" {
		t.Errorf("member was told %q, want the provider's sentence", s)
	}
}

// An item whose plugin is not installed must not be sold. Hiding it is the
// same call the flavour filter makes: taking points for something nothing can
// deliver is worse than an item that is missing.
func TestContributedItemsAreHiddenWithoutTheirProvider(t *testing.T) {
	it := charityItem()
	h := newHandlers(newMemStore(it), &fakePoints{}, &fakeGranter{})
	h.core = coreWith(t, nil)

	if h.itemAvailable(it) {
		t.Error("a charity item is on sale with no charity plugin installed")
	}
	h.core = coreWith(t, &fakeItemType{})
	if !h.itemAvailable(it) {
		t.Error("a charity item is hidden with its provider present")
	}
}

// The def editor: a contributed type appears in the dropdown, and the store's
// own seven are still there.
func TestTypeInfosOffersBuiltinsAndContributions(t *testing.T) {
	h := newHandlers(newMemStore(), &fakePoints{}, &fakeGranter{})
	h.core = coreWith(t, &fakeItemType{})

	infos := h.typeInfos(context.Background())
	if len(infos) != len(builtinTypes)+1 {
		t.Fatalf("%d types offered, want %d", len(infos), len(builtinTypes)+1)
	}
	if infos[len(infos)-1].Kind != "charity" {
		t.Errorf("last type is %q, want the contributed charity", infos[len(infos)-1].Kind)
	}
	// Every builtin the buy path can grant must be offerable, or an operator
	// cannot create the item that reaches it — which is how the invite type
	// sat unreachable for months.
	for _, want := range []string{"rank", "invite", "perk", "flair", "upload_gb", "download_gb", "medal"} {
		found := false
		for _, i := range infos {
			if i.Kind == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the def editor cannot create a %q item", want)
		}
	}
}

// A def is validated by whoever owns the meaning of reward_ref: the store for
// its own types, the provider for a contributed one — at the admin form,
// where the person who can fix it is looking.
func TestValidateItemDelegatesToTheProvider(t *testing.T) {
	h := newHandlers(newMemStore(), &fakePoints{}, &fakeGranter{})
	prov := &fakeItemType{}
	h.core = coreWith(t, prov)
	ctx := context.Background()

	it := &Item{Name: "Charity", PointsCost: 1, RewardType: "charity", RewardRef: "0.5"}
	if err := h.validateItem(ctx, it); err != nil {
		t.Errorf("provider accepted the ref but the store refused it: %v", err)
	}
	prov.badRef = true
	if err := h.validateItem(ctx, it); err == nil {
		t.Error("the store saved a def its provider rejected")
	}
	// The store's own rules still run first: a contributed type does not get
	// to skip the shape checks every item shares.
	if err := h.validateItem(ctx, &Item{RewardType: "charity", PointsCost: 1}); err == nil {
		t.Error("a nameless item was accepted because its type was contributed")
	}
	// And a type nobody provides is still a mis-typed row, not a new feature.
	h.core = coreWith(t, nil)
	if err := h.validateItem(ctx, it); err == nil {
		t.Error("a def naming a type no plugin provides was accepted")
	}
}
