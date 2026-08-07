package events

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Store is the events data layer.
//
// Definitions are addressed by SLUG at this boundary even though the tables key
// on id, because every consumer holds a slug and the id is this schema's private
// business. The window rows carry the id internally; nothing outside sees it.
type Store interface {
	// ListEvents returns every definition, enabled or not. Admin dropdowns need
	// the disabled ones too: picking one has to show that it will never open.
	ListEvents(ctx context.Context) ([]pluginapi.ScheduledEvent, error)

	// GetEvent resolves one by slug. The bool is false when there is no such
	// event, which is not an error — a consumer holding the slug of a deleted
	// event needs to tell "gone" from "the query failed".
	GetEvent(ctx context.Context, slug string) (pluginapi.ScheduledEvent, bool, error)

	// UpsertEvent creates or replaces a definition by slug.
	UpsertEvent(ctx context.Context, ev pluginapi.ScheduledEvent) error

	// DeleteEvent removes a definition and, by cascade, its windows.
	DeleteEvent(ctx context.Context, slug string) error

	// OpenWindows returns, in ONE query, the window of each named slug that
	// contains `at`. Slugs with no open window are absent from the map rather
	// than present-and-zero, so a caller cannot mistake "closed" for "open at
	// the epoch".
	OpenWindows(ctx context.Context, slugs []string, at time.Time) (map[string]pluginapi.EventWindow, error)

	// AllOpen returns every slug open at `at`. The set form, for feeds that
	// would otherwise ask per row.
	AllOpen(ctx context.Context, at time.Time) (map[string]bool, error)

	// LastWindowEnd returns when the furthest-ahead window of an event closes,
	// so generation resumes from there instead of from now. Zero time when the
	// event has no windows yet.
	//
	// Resuming from the last END rather than from now is what stops a contiguous
	// event growing a hole every time the generator runs late — and a hole in a
	// daily reset is a day the reward does not exist.
	LastWindowEnd(ctx context.Context, slug string) (time.Time, error)

	// InsertWindows adds windows, ignoring any that already exist. Returns how
	// many were new. Idempotent by (event, starts_at), which is what lets the
	// generator re-run over a range it has already covered.
	InsertWindows(ctx context.Context, slug string, ws []pluginapi.EventWindow) (int, error)

	// ListWindows returns an event's windows, newest first, for the admin view.
	ListWindows(ctx context.Context, slug string, limit int) ([]pluginapi.EventWindow, error)
}
