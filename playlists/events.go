package playlists

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// What playlists announce.
//
// Neither is countable. Both are free actions on rows nobody else has to
// accept: a member can create ten empty playlists in a minute, or add the same
// release to a playlist and remove it again all afternoon. A count measures
// clicking.
//
// What WOULD be worth a badge here is a curation metric — public playlists
// with real length, or ones other people follow — and that is an absolute
// count over current rows, which the metric path already handles and
// self-heals when a member deletes half their collection. An event stream
// cannot: it only ever adds, so a "50 items" badge earned by adding fifty and
// deleting forty-nine would stay earned.
const (
	EventPlaylistCreated = "playlists.created"
	EventItemAdded       = "playlists.item.added"
)

// PlaylistCreated is the Data payload of EventPlaylistCreated.
type PlaylistCreated struct {
	PlaylistID int64
	Slug       string
	Name       string
	Public     bool
}

// ItemAdded is the Data payload of EventItemAdded.
type ItemAdded struct {
	PlaylistID int64
	ReleaseID  int64
}

func declareEvents(c *core.Core) error {
	for _, d := range []core.EventDef{
		{Name: EventPlaylistCreated, Emitter: "playlists", Kind: core.EventMember, Stable: true,
			Summary: "a member created a playlist",
			Payload: "playlists.PlaylistCreated"},
		{Name: EventItemAdded, Emitter: "playlists", Kind: core.EventMember, Stable: true,
			Summary: "a member added a release to one of their playlists",
			Payload: "playlists.ItemAdded"},
	} {
		if err := c.DeclareEvent(d); err != nil {
			return err
		}
	}
	return nil
}

// emit announces, after the write has committed.
func (h *Handlers) emit(ctx context.Context, name string, userID int64, data any) {
	if h.core == nil {
		return
	}
	h.core.Emit(ctx, core.Event{Name: name, UserID: userID, Data: data})
}
