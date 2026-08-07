package offers

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// Delivering a requested file is the strongest contribution signal on this
// site — you must actually hold the thing and upload it — and it is
// deliberately NOT countable.
//
// The reason is a decision already taken about this surface: fulfilment is
// meant to become anonymous, so that once a request is served there is no
// record of who served it. A countable event is precisely the opposite: a
// permanent, per-member, monotonic ledger of that attribution, with badges
// minted from it that cannot be withdrawn without taking the badges back.
//
// This test exists because flipping the flag looks like an improvement.
func TestDeliveryStaysUncountable(t *testing.T) {
	c := &core.Core{}
	if err := declareEvents(c); err != nil {
		t.Fatalf("declare: %v", err)
	}
	defs := c.EventDefs()
	if len(defs) != 2 {
		t.Fatalf("%d events declared, want 2", len(defs))
	}
	for _, d := range defs {
		if !strings.HasPrefix(d.Name, "offers.") {
			t.Errorf("%s does not carry the plugin's namespace", d.Name)
		}
		if d.Payload == "" {
			t.Errorf("%s carries Data but names no type to assert to", d.Name)
		}
		if d.Countable {
			t.Errorf("%s is countable. Achievements subscribe to every countable event and "+
				"keep a permanent per-member total; on this surface that is the attribution "+
				"anonymous fulfilment has to erase.", d.Name)
		}
	}
	if err := declareEvents(c); err == nil {
		t.Error("declaring twice was accepted")
	}
}

// The delivery emit must stay inside the `if ok2` branch. ok2 false means the
// claim had already been served or released, so emitting unconditionally would
// credit an offerer a second time for one file — and the notification beside
// it is already guarded the same way, which is the tell that the guard matters.
func TestDeliveredFiresOnlyOnARealDelivery(t *testing.T) {
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), "EventRequestDelivered")
	if i < 0 {
		t.Fatal("nothing emits EventRequestDelivered any more")
	}
	// Everything between the ok2 assignment and the emit, which must contain
	// the guard and no intervening close of it.
	head := string(src)[:i]
	guard := strings.LastIndex(head, "if ok2 {")
	assign := strings.LastIndex(head, "ok2, err := deps.DeliverRequest")
	if guard < assign {
		t.Error("the delivery emit is no longer behind `if ok2` — a rejected or duplicate " +
			"delivery would now announce itself and credit the offerer twice")
	}
}

// A Handlers with no Core must announce nothing rather than panic.
func TestEmitIsInertWithoutCore(t *testing.T) {
	(&Handlers{}).emit(context.Background(), EventRequestDelivered, 1, nil)
}
