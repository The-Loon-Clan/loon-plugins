package cosmetics

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What cosmetics announce.
//
// Two events, and the interesting thing about them is that only one is
// countable. The pair is the clearest example in this repo of the distinction
// the other plugins reason about individually.

const (
	// EventUnlocked fires when a member gains the right to wear something —
	// bought from the store, or granted.
	EventUnlocked = "cosmetics.unlocked"
	// EventEquipped fires when a member puts something on.
	//
	// The ON leg only. Taking an effect off announces nothing: a subscriber
	// counting cannot un-count reliably, and "what are they wearing now" is a
	// question the store answers directly rather than something to reconstruct
	// from a stream.
	EventEquipped = "cosmetics.equipped"
)

// Unlocked is the Data payload of EventUnlocked.
type Unlocked struct {
	Slug string
	// Source is where it came from — SourceStore and the rest. A subscriber
	// scoring "bought ten effects" wants to exclude the ones an admin granted.
	Source string
	// Days is the term, 0 for permanent.
	Days int
}

// Equipped is the Data payload of EventEquipped.
type Equipped struct {
	Slot string
	Slug string
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		// COUNTABLE. An unlock costs points or an admin's decision, so the
		// count measures something that was spent rather than something that
		// was clicked, and a member cannot manufacture more of them.
		{Name: EventUnlocked, Emitter: "cosmetics", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member unlocked a cosmetic effect",
			Payload: "cosmetics.Unlocked"},
		// NOT COUNTABLE, and this is the case the flag exists for. Equipping
		// is free and unlimited: a member can put an effect on and take it off
		// all afternoon, so a total measures fiddling with a dropdown. It is
		// still worth ANNOUNCING — a subscriber may want to react to what
		// somebody is wearing — which is exactly the difference between "worth
		// hearing" and "worth totalling".
		{Name: EventEquipped, Emitter: "cosmetics", Kind: core.EventMember,
			Countable: false, Stable: true,
			Summary: "a member put a cosmetic effect on",
			Payload: "cosmetics.Equipped"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, after the write has committed. Nil-core tolerant: a missing
// mediator must not be what stops somebody wearing a hat they paid for.
func (p *Plugin) emit(ctx context.Context, name string, userID int64, data any) {
	if p == nil || p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{Name: name, UserID: userID, Count: 1, Data: data})
}
