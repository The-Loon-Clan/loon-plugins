package applications

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What applications announce.
//
// Both are SYSTEM events, and that is the interesting part. An application is
// made by somebody with no account — that is the whole point of the plugin —
// so there is no member id to attribute it to, and a member event with no
// member is a contradiction the directory would happily carry.

const (
	// EventSubmitted fires when somebody asks to join.
	EventSubmitted = "applications.submitted"
	// EventDecided fires when staff accept or reject one.
	EventDecided = "applications.decided"
)

// Submitted is the Data payload of EventSubmitted.
//
// It carries NO email and NO IP hash. A subscriber that wanted either could
// learn who is trying to join a closed site from a stream several plugins can
// read, and the queue this plugin builds is exactly the surface where staff
// look at that under an access gate. Only the fact travels.
type Submitted struct{ ApplicationID int64 }

// Decided is the Data payload of EventDecided.
type Decided struct {
	ApplicationID int64
	// Status is StatusAccepted or StatusRejected.
	Status string
	// DecidedBy is the staff member who decided, so an audit surface can
	// attribute it. The APPLICANT is still not named.
	DecidedBy int64
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		// NOT COUNTABLE, and it could not be even if it were desirable: a
		// countable event is totalled per MEMBER, and the person who submitted
		// this has no account yet. Kind is System for the same reason.
		{Name: EventSubmitted, Emitter: "applications", Kind: core.EventSystem,
			Countable: false, Stable: true,
			Summary: "somebody applied to join",
			Payload: "applications.Submitted"},
		{Name: EventDecided, Emitter: "applications", Kind: core.EventSystem,
			Countable: false, Stable: true,
			Summary: "staff accepted or rejected an application",
			Payload: "applications.Decided"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, after the write has committed.
//
// UserID is deliberately 0 on both: there is no member to attribute a system
// event to, and core's subscriber path already refuses to score anything whose
// UserID is 0, so a mis-flagged countable could not silently credit somebody.
func (p *Plugin) emit(ctx context.Context, name string, data any) {
	if p == nil || p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{Name: name, Count: 1, Data: data})
}
