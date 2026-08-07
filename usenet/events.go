package usenet

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What the crawler announces.
//
// The first SYSTEM event on the site, and the reason core has a kind at all.
// Nobody did this: a crawler assembled a release out of articles that were
// already on a news server. There is no member to credit, `UserID` stays zero,
// and core refuses to let a system event be countable — so no achievement can
// ever be scored on "releases indexed", which is correct. Rewarding a member
// for something a machine did is the failure the kind field exists to make
// impossible rather than merely unwise.
//
// Who wants it: caches (a release page that should stop saying "not found"),
// stats, and anything showing recent activity. All of which previously had to
// poll or be told by the host.
const EventReleaseIndexed = "usenet.release.indexed"

// ReleaseIndexed is the Data payload of EventReleaseIndexed.
type ReleaseIndexed struct {
	Title string
	Group string
	// Size in bytes of the assembled release.
	Size int64
}

func declareEvents(c *core.Core) error {
	return c.DeclareEvent(core.EventDef{
		Name: EventReleaseIndexed, Emitter: "usenet",
		Kind: core.EventSystem, Stable: true,
		// NOT countable, and core would refuse it anyway: a system event has
		// no member to total against.
		Summary: "the crawler assembled and indexed a release (no member: the crawler did it)",
		Payload: "usenet.ReleaseIndexed",
	})
}

// emitIndexed announces one assembled release.
//
// Called from the crawl pass, which builds many releases in a loop, so this is
// on a hot-ish path: delivery is synchronous and a slow subscriber would slow
// the crawler. Documented on core.EventHandler, and worth repeating at the one
// emit site in this repo that fires in bulk.
func (p *Plugin) emitIndexed(ctx context.Context, title, group string, size int64) {
	if p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{
		Name: EventReleaseIndexed, Subject: title,
		Data: ReleaseIndexed{Title: title, Group: group, Size: size},
	})
}
