package mediainfo

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What mediainfo announces.
//
// Both are real contributions to a release page — somebody went and got
// something the indexer could not — which is the shape worth counting.

const (
	// EventReportPosted fires when a member posts a MediaInfo report.
	EventReportPosted = "mediainfo.report.posted"
	// EventShotAdded fires when a member adds a screenshot.
	EventShotAdded = "mediainfo.shot.added"
)

// ReportPosted is the Data payload of EventReportPosted.
type ReportPosted struct{ ReleaseID int64 }

// ShotAdded is the Data payload of EventShotAdded.
type ShotAdded struct {
	ReleaseID int64
	Bytes     int64
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		// COUNTABLE. A report is one per member per release — the unique key
		// says so — so the ceiling is the number of releases, not the number
		// of times somebody can press a button. Re-posting over your own
		// replaces rather than accumulating.
		{Name: EventReportPosted, Emitter: "mediainfo", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member posted a MediaInfo report",
			Payload: "mediainfo.ReportPosted"},
		// COUNTABLE, with a caveat worth stating: a screenshot is keyed on
		// (release, source URL), so one member CAN add several to a release by
		// supplying different links. That is a real contribution rather than a
		// button press — each one is a picture somebody found — but a site
		// scoring on it should expect a higher ceiling than reports.
		{Name: EventShotAdded, Emitter: "mediainfo", Kind: core.EventMember,
			Countable: true, Stable: true,
			Summary: "a member added a screenshot to a release",
			Payload: "mediainfo.ShotAdded"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, after the write has committed.
func (p *Plugin) emit(ctx context.Context, name string, userID int64, data any) {
	if p == nil || p.core == nil {
		return
	}
	p.core.Emit(ctx, core.Event{Name: name, UserID: userID, Count: 1, Data: data})
}
