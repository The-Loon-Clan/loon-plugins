package pluginapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

type fakeItemType struct{ kind string }

func (f fakeItemType) Describe(context.Context, string) StoreItemTypeInfo {
	return StoreItemTypeInfo{Kind: f.kind, Label: f.kind}
}
func (f fakeItemType) Validate(context.Context, string, int) error { return nil }
func (f fakeItemType) Grant(context.Context, StorePurchase) (string, error) {
	return f.kind, nil
}

// A variable-cost type shaped like charity's: a bounded amount that IS the
// price, and a closed set of bands.
func charityShape() StoreItemTypeInfo {
	return StoreItemTypeInfo{
		Kind: "charity", CostFrom: "amount",
		Fields: []StoreField{
			{Name: "amount", Label: "Amount", Kind: StoreFieldNumber, Min: 1000, Max: 50000, Default: "1000"},
			{Name: "ratio", Label: "band", Kind: StoreFieldSelect, Default: "0.5",
				Options: []StoreOption{{Value: "0.5"}, {Value: "1"}}},
		},
	}
}

// The buy control's rules are the whole file; these hold them still, with the
// hostile inputs guarded — every one of them arrives on a form a member can
// hand-craft, and each is checked BEFORE any points move.
func TestPrepareStorePurchase(t *testing.T) {
	cases := []struct {
		name     string
		info     StoreItemTypeInfo
		fixed    int
		fields   map[string]string
		wantCost int
		wantErr  string // substring; empty means success
	}{
		{
			name: "no fields keeps the item's own price",
			info: StoreItemTypeInfo{Kind: "medal"}, fixed: 250,
			wantCost: 250,
		},
		{
			name: "the amount field IS the price",
			info: charityShape(), fixed: 999,
			fields:   map[string]string{"amount": "2500", "ratio": "1"},
			wantCost: 2500,
		},
		{
			name: "an untouched control falls back to its defaults",
			info: charityShape(), fixed: 999,
			wantCost: 1000,
		},
		{
			name: "below the floor is refused in the member's words",
			info: charityShape(), fields: map[string]string{"amount": "10"},
			wantErr: "at least 1000",
		},
		{
			name: "above the ceiling likewise",
			info: charityShape(), fields: map[string]string{"amount": "999999"},
			wantErr: "at most 50000",
		},
		{
			name: "a number that is not one",
			info: charityShape(), fields: map[string]string{"amount": "lots"},
			wantErr: "needs a number",
		},
		{
			name: "a band nobody offered",
			info: charityShape(), fields: map[string]string{"amount": "2000", "ratio": "99"},
			wantErr: "offered band",
		},
		{
			name: "a field kind the store cannot draw is a wiring bug, not a purchase",
			info: StoreItemTypeInfo{Fields: []StoreField{{Name: "when", Kind: "datepicker"}}}, fixed: 10,
			wantErr: "misconfigured",
		},
		{
			name: "priced from a field that was never declared",
			info: StoreItemTypeInfo{CostFrom: "amount"}, fixed: 10,
			wantErr: "misconfigured",
		},
		{
			name: "a free item is not a purchase",
			info: StoreItemTypeInfo{Kind: "medal"}, fixed: 0,
			wantErr: "pick an amount",
		},
		{
			name: "a negative amount cannot be talked past the bounds",
			info: StoreItemTypeInfo{CostFrom: "amount", Fields: []StoreField{
				{Name: "amount", Kind: StoreFieldNumber, Default: "-5"}}},
			wantErr: "pick an amount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost, resolved, err := PrepareStorePurchase(tc.info, tc.fixed, tc.fields)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PrepareStorePurchase: %v", err)
			}
			if cost != tc.wantCost {
				t.Errorf("cost=%d, want %d", cost, tc.wantCost)
			}
			// Defaults must reach the granter: a member who touched nothing
			// still chose something, and an empty map here would hand the
			// provider a blank where the form showed a value.
			for _, f := range tc.info.Fields {
				if _, ok := resolved[f.Name]; !ok {
					t.Errorf("resolved values dropped %q", f.Name)
				}
			}
		})
	}
}

// The split that decides what the buyer is told: a value THEY chose badly is
// a sentence they can act on; a def the provider declared badly is not, and
// showing it as advice would ask a member to fix an operator's mistake.
func TestPrepareStorePurchaseTellsRefusalsFromWiringBugs(t *testing.T) {
	refusal := func(info StoreItemTypeInfo, fields map[string]string) bool {
		_, _, err := PrepareStorePurchase(info, 10, fields)
		if err == nil {
			t.Fatal("expected an error")
		}
		var r StoreRefusal
		return errors.As(err, &r)
	}
	if !refusal(charityShape(), map[string]string{"amount": "1"}) {
		t.Error("an out-of-bounds amount must reach the member as their own sentence")
	}
	if refusal(StoreItemTypeInfo{Fields: []StoreField{{Name: "when", Kind: "datepicker"}}}, nil) {
		t.Error("a provider's unknown field kind was offered to the member as advice")
	}
	if refusal(StoreItemTypeInfo{CostFrom: "nope"}, nil) {
		t.Error("a provider's undeclared cost field was offered to the member as advice")
	}
}

func TestStoreItemTypeDiscovery(t *testing.T) {
	c := &core.Core{}
	// Registered out of alphabetical order, because the def editor's dropdown
	// must not reshuffle between page loads.
	for _, kind := range []string{"pot", "charity"} {
		if err := c.Register(StoreItemTypePrefix+kind, StoreItemType(fakeItemType{kind})); err != nil {
			t.Fatal(err)
		}
	}
	// A neighbour under a different prefix must not be mistaken for one.
	if err := c.Register(MultiplierSourcePrefix+"medals", fakeSource{}); err != nil {
		t.Fatal(err)
	}

	got := StoreItemTypes(c)
	if len(got) != 2 {
		t.Fatalf("found %d types, want 2", len(got))
	}
	if k := got[0].Describe(context.Background(), "").Kind; k != "charity" {
		t.Errorf("first type is %q, want charity (sorted)", k)
	}
	if _, ok := LookupStoreItemType(c, "charity"); !ok {
		t.Error("LookupStoreItemType missed a registered kind")
	}
	if _, ok := LookupStoreItemType(c, "rank"); ok {
		t.Error("LookupStoreItemType invented a kind nobody registered")
	}
	if _, ok := LookupStoreItemType(nil, "charity"); ok {
		t.Error("a nil core answered a lookup")
	}
}
