package store

import "github.com/the-loon-clan/loon/core"

// EventPurchased fires when a member buys something from the store.
//
// Countable: "spent points ten times" is a reasonable achievement in a way
// that "received points ten times" is not. The distinction matters — an
// achievement scored on points RECEIVED, that itself pays points, is a loop,
// which is why economy.points.granted is deliberately absent from the
// catalogue. Spending has no such feedback.
const EventPurchased = "store.item.purchased"

// Purchased is the Data payload of EventPurchased.
type Purchased struct {
	ItemID int
	Name   string
	// Cost in points, so a subscriber can total spend without asking the store
	// what anything was worth at the time.
	Cost int
	// Reward is what the item handed over, as the member saw it.
	Reward string
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventPurchased, Emitter: "store",
		Kind: core.EventMember, Countable: true, Stable: true,
		Summary: "a member bought something from the store",
		Payload: "store.Purchased",
	})
}
