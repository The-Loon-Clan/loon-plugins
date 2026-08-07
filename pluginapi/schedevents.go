package pluginapi

import (
	"context"
	"time"
)

// Scheduled events: named spans of time other plugins can hang behaviour on.
//
// A season, a launch week, a daily reset, a one-off announcement date. The
// definition is a cron expression plus an optional duration, and a job
// materialises concrete WINDOWS ahead of time so that "is event X open right
// now" is one indexed lookup rather than a cron evaluation per query.
//
// Published by the events plugin under ScheduledEventsName; consumed by anything
// that needs to know whether a span is currently running. It lived inside the
// rewards plugin first, whose own schema comment admitted it was "not
// reward-specific in meaning even though it lives here for now" — rewards gates
// recurring payouts on a window, news wants to publish a post when an event
// opens, and neither should have to reach into the other.
//
// Consumers reference an event by SLUG, never by id. Ids belong to the owning
// plugin's schema; a slug survives the table being rebuilt, moved, or restored
// from a dump taken elsewhere, and it is what an operator sees in a dropdown.

// ScheduledEventsName is the extension-registry key.
const ScheduledEventsName = "events.scheduled"

// ScheduledEvent is a definition — the rule, not an occurrence.
type ScheduledEvent struct {
	Slug        string
	Name        string
	Description string

	// Cron generates recurring windows. Empty means a one-off, whose single
	// window starts at StartsAt.
	Cron string

	// Duration bounds each window: it closes this long after opening, leaving
	// gaps between firings. Zero means the window runs until the NEXT firing —
	// contiguous, no gaps — and for a one-off, with no next firing, that means
	// it never closes. Those are the same rule, which is worth stating because
	// they read like two.
	Duration time.Duration

	// StartsAt is when a one-off opens. Nil for a cron-driven event, whose
	// starts come from the expression.
	StartsAt *time.Time

	// Timezone the cron is evaluated in. "Midnight" is a timezone-relative
	// claim, and a daily reset firing at UTC midnight is 8pm in New York.
	Timezone string

	Enabled bool
}

// OneOff reports whether this event has no recurrence.
func (e ScheduledEvent) OneOff() bool { return e.Cron == "" }

// Perpetual reports whether a window of this event, once open, never closes.
// True only for a one-off with no duration — a recurring event with no duration
// is contiguous rather than perpetual, because the next firing ends the current
// window.
func (e ScheduledEvent) Perpetual() bool { return e.OneOff() && e.Duration == 0 }

// EventWindow is one concrete occurrence. Half-open: a window contains an
// instant when Starts <= t < Ends, so back-to-back windows of a contiguous
// event neither overlap nor leave a gap at the boundary.
type EventWindow struct {
	Slug   string
	Starts time.Time
	Ends   time.Time
}

// Contains reports whether t falls inside this window.
func (w EventWindow) Contains(t time.Time) bool {
	return !t.Before(w.Starts) && t.Before(w.Ends)
}

// Key names this occurrence: "<slug>@<start in RFC3339 UTC>".
//
// THE cross-system identifier for "this event, this time round". Rewards needs
// one so a recurring payout pays once per occurrence rather than once ever; news
// needs one to say which run of an event a post belongs to; a leaderboard needs
// one to scope a season's standings. One string all of them can hold.
//
// Derived rather than stored, and derived from the SLUG rather than from a row
// id, which is the whole point. A window id is private to the events schema, so
// a consumer holding one is coupled to this plugin's table — and the id changes
// if the table is ever rebuilt or restored from another host's dump, silently
// detaching every consumer. The slug and the start do not change, because they
// are what the operator configured.
//
// Readable on purpose. This value ends up in other plugins' rows, so it will be
// read in a psql session by somebody debugging why a member was or was not paid,
// and "summer-2026@2026-08-01T00:00:00Z" answers that where an opaque integer
// starts another query.
func (w EventWindow) Key() string {
	return w.Slug + "@" + w.Starts.UTC().Format(time.RFC3339)
}

// Perpetual reports whether this window never closes. A never-closing window is
// stored with Ends at the far future rather than as a NULL, so every query can
// use one range comparison and no caller needs a special case.
func (w EventWindow) Perpetual() bool { return IsPerpetual(w.Ends) }

// PerpetualAfter is the "obviously not a real end date" line: any window ending
// past it never closes.
//
// Exported and lives HERE, with the type, because three places need the same
// answer — the generator that writes the sentinel, the window that reports
// itself perpetual, and the admin page that must print "never" instead of the
// year 9999. Three copies of one magic date is three chances for one of them to
// drift by a digit.
var PerpetualAfter = time.Date(9000, 1, 1, 0, 0, 0, 0, time.UTC)

// IsPerpetual reports whether an end instant means "never closes".
func IsPerpetual(end time.Time) bool { return end.After(PerpetualAfter) }

// ScheduledEvents is looked up off the extension registry under
// ScheduledEventsName. Every method is safe to call from any process.
type ScheduledEvents interface {
	// Events lists every definition, enabled or not, for admin dropdowns. A
	// disabled event is still worth showing — an operator picking one needs to
	// see that it will never open.
	Events(ctx context.Context) ([]ScheduledEvent, error)

	// Event resolves one definition by slug. Absent is not an error: a
	// consumer holding a slug for an event an operator has since deleted needs
	// to distinguish "gone" from "the lookup failed", and only one of those is
	// worth reporting.
	Event(ctx context.Context, slug string) (ScheduledEvent, bool, error)

	// OpenNow returns the slugs whose window contains now.
	//
	// A SET, deliberately, rather than an IsOpen(slug) per caller. The callers
	// are feeds and listings rendering many rows, and asking per row is an N+1
	// across a plugin boundary. The result is small — open windows only — and
	// cheap enough to call per request.
	OpenNow(ctx context.Context) (map[string]bool, error)

	// OpenWindows returns the currently-open window for each named slug, and
	// omits slugs with none. Callers that must record WHICH occurrence they
	// acted on need this rather than OpenNow: a recurring payout keyed on
	// "the event is open" would pay once ever, where one keyed on the window
	// pays once per occurrence.
	OpenWindows(ctx context.Context, slugs []string) (map[string]EventWindow, error)

	// NextOpen returns when an event next opens, for "starts in 3 days" copy.
	// Zero time means never — a disabled event, a past one-off, or a cron with
	// no further firings.
	NextOpen(ctx context.Context, slug string) (time.Time, error)
}
