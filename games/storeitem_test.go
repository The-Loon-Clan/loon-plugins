package games

import (
	"context"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The bands must survive the round trip through the form. A store select
// carries strings; recipients() compares floats against a closed set by
// equality, so a value that formats to something ParseFloat cannot return
// exactly would refuse every purchase at the last step, after the points had
// already been taken.
func TestBandValuesRoundTrip(t *testing.T) {
	for _, r := range charityRatios {
		got, ok := parseBand(bandValue(r))
		if !ok || got != r {
			t.Errorf("band %v rendered as %q and came back %v (ok=%v)", r, bandValue(r), got, ok)
		}
	}
	for _, bad := range []string{"", "0.3", "47.3", "half", "1e400"} {
		if _, ok := parseBand(bad); ok {
			t.Errorf("parseBand(%q) accepted a band nobody offers", bad)
		}
	}
}

// A def either pins a band or lets the buyer choose; anything else is a typo
// an admin should meet at the form, not a member at a purchase.
func TestCharityItemValidate(t *testing.T) {
	c := charityItemType{}
	if err := c.Validate(context.Background(), "", 0); err != nil {
		t.Errorf("a blank ref is the open item, not an error: %v", err)
	}
	if err := c.Validate(context.Background(), "0.5", 0); err != nil {
		t.Errorf("an offered band was refused: %v", err)
	}
	err := c.Validate(context.Background(), "0.55", 0)
	if err == nil {
		t.Fatal("a band nobody offers was accepted")
	}
	// The refusal has to say what IS allowed, or an admin is left guessing.
	if !strings.Contains(err.Error(), "0.25") {
		t.Errorf("the refusal %q does not name the offered bands", err)
	}
}

// The buy control's shape follows the def: a pinned band has nothing to
// choose, an open one does. Both are priced by the buyer.
func TestCharityItemDescribeShapesTheControlFromTheDef(t *testing.T) {
	c := charityItemType{&Plugin{st: nil}}
	// st is nil, so Settings fails and Describe falls back to the defaults —
	// which is the case that must not take a page render down with it.
	open := c.Describe(context.Background(), "")
	if open.CostFrom != "amount" {
		t.Errorf("CostFrom=%q, want the buyer's own amount", open.CostFrom)
	}
	if len(open.Fields) != 2 || open.Fields[1].Name != "band" {
		t.Fatalf("an open charity item offers %d fields, want amount + band", len(open.Fields))
	}
	if got := open.Fields[0].Min; got != defaults().CharityMin {
		t.Errorf("amount floor=%d, want the operator's %d — the shop and the charity page must bound alike",
			got, defaults().CharityMin)
	}
	if len(open.Fields[1].Options) != len(charityRatios) {
		t.Errorf("the band chooser offers %d options, want %d", len(open.Fields[1].Options), len(charityRatios))
	}

	pinned := c.Describe(context.Background(), "0.5")
	if len(pinned.Fields) != 1 {
		t.Errorf("a pinned item offers %d fields, want the amount alone", len(pinned.Fields))
	}
	if !strings.Contains(pinned.Note, "0.5") {
		t.Errorf("a pinned item's note %q does not say which members it reaches", pinned.Note)
	}

	// The declared control and the store's checker have to agree, or a member
	// meets a refusal the form gave them no way to avoid.
	if _, _, err := pluginapi.PrepareStorePurchase(open, 0, map[string]string{"amount": "1000"}); err != nil {
		t.Errorf("the store refused what this control offers: %v", err)
	}
}

// Grant is reached only after the store has debited, so it must have no way
// to charge again. Proven by the type's own dependencies: it pays through
// distribute, and give — the only deducting path — is not on its call graph.
func TestCharityItemGrantRefusesWithoutFigures(t *testing.T) {
	// stats nil: the site cannot find need. The buyer's points are the
	// store's to refund, and the sentence is theirs to read.
	c := charityItemType{&Plugin{}}
	_, err := c.Grant(context.Background(), pluginapi.StorePurchase{
		UserID: 1, ItemID: 7, Ref: "0.5", Cost: 2000,
	})
	if err == nil {
		t.Fatal("granted charity on a host with no member figures")
	}
	var refusal pluginapi.StoreRefusal
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("err=%v, want the charity page's own sentence", err)
	}
	if _, ok := any(err).(pluginapi.StoreRefusal); !ok {
		t.Errorf("err is %T, want %T so the buyer reads it", err, refusal)
	}
}
