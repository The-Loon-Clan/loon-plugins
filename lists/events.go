package lists

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the lists plugin announces.
//
// All three are member actions and all three are countable, but they count
// DIFFERENT people and that is the part worth stating: lists.followed counts
// the FOLLOWER, not the list's owner. A "100 followers" achievement is a
// reasonable thing to want and this is not it — the owner's follower count is
// a query against their lists, not a total of events they did not cause.
const (
	EventListCreated = "lists.created"
	EventItemAdded   = "lists.item.added"
	EventFollowed    = "lists.followed"
)

// ListCreated is the Data payload of EventListCreated.
type ListCreated struct {
	Name   string
	Public bool
}

// ItemAdded is the Data payload of EventItemAdded.
type ItemAdded struct {
	ListID int
	NzbID  int64
}

// Followed is the Data payload of EventFollowed. UserID on the event is the
// follower; OwnerID is whose list it was.
type Followed struct {
	ListID int
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventListCreated, Emitter: "lists", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member created a list", Payload: "lists.ListCreated"},
		{Name: EventItemAdded, Emitter: "lists", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member added something to a list", Payload: "lists.ItemAdded"},
		{Name: EventFollowed, Emitter: "lists", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member followed a list (UserID is the follower, not the owner)",
			Payload: "lists.Followed"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, if a host wired the bus. Package-level to match this
// plugin's Deps shape, where the handlers are a bare struct and everything
// they need arrives through `deps`.
func emit(ctx context.Context, name string, userID int, subject string, data any) {
	if busCore == nil {
		return
	}
	busCore.Emit(ctx, core.Event{
		Name: name, UserID: int64(userID), Subject: subject, Data: data,
	})
}

// busCore is set at Provision. Nil in tests, which then emit nothing.
var busCore *core.Core
