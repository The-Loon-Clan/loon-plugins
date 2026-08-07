package events

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// cronParser accepts standard 5-field expressions plus descriptors (@daily,
// @weekly). Seconds are deliberately not enabled: an event firing every few
// seconds is a misconfiguration that would materialise millions of windows, and
// refusing to parse it is a cheaper way to say so than the bound below.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// PerpetualEnd is the stored end of a window that never closes.
//
// A sentinel rather than NULL. Every "is this open" query is then one range
// comparison with no special case, and `ends_at > starts_at` keeps its meaning.
// A nullable end would push the case into every consumer, and one of them would
// forget — which is the same argument that keeps timezone out of the window rows.
var PerpetualEnd = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// GenerateWindows materialises the windows of ev that begin in [from, until].
//
// Ported from the rewards plugin with the two cases it could not express:
//
//   - a ONE-OFF, which rewards skipped entirely ("windows authored by hand"),
//     leaving no way to declare a plain announcement date;
//   - a one-off with a DURATION, which its schema forbade outright.
//
// The recurring behaviour is unchanged, and deliberately so — the contiguous
// case is what a daily reset depends on.
func GenerateWindows(ev pluginapi.ScheduledEvent, from, until time.Time) ([]pluginapi.EventWindow, error) {
	if !ev.Enabled {
		// Not an error. The generator runs across every event and a disabled one
		// is simply not its business — the same reason a one-off used to return
		// nil here rather than complaining.
		return nil, nil
	}
	if until.Before(from) {
		return nil, nil
	}

	if ev.OneOff() {
		return oneOffWindow(ev, from, until)
	}
	return recurringWindows(ev, from, until)
}

// oneOffWindow builds the single window of an event with no recurrence.
func oneOffWindow(ev pluginapi.ScheduledEvent, from, until time.Time) ([]pluginapi.EventWindow, error) {
	if ev.StartsAt == nil {
		return nil, fmt.Errorf("event %q: one-off with no start date", ev.Slug)
	}
	start := *ev.StartsAt
	// Generated once, when the horizon first reaches it. Outside the requested
	// range there is nothing to do — and crucially the UNIQUE (event_id,
	// starts_at) is what makes a second pass over the same range a no-op rather
	// than a duplicate, so this needs no "have I done this already" state.
	if start.After(until) {
		return nil, nil
	}

	end := PerpetualEnd
	if ev.Duration > 0 {
		end = start.Add(ev.Duration)
		// A one-off whose window has already closed still gets generated: the
		// row is the record that it happened, and a consumer asking "was this
		// open last Tuesday" needs it to exist. Only `from` filtering would
		// drop it, which is why `from` is not consulted here.
	}
	return []pluginapi.EventWindow{{Slug: ev.Slug, Starts: start, Ends: end}}, nil
}

// recurringWindows walks the cron expression.
func recurringWindows(ev pluginapi.ScheduledEvent, from, until time.Time) ([]pluginapi.EventWindow, error) {
	sched, err := cronParser.Parse(ev.Cron)
	if err != nil {
		return nil, fmt.Errorf("event %q: parse cron %q: %w", ev.Slug, ev.Cron, err)
	}
	tz := ev.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("event %q: load timezone %q: %w", ev.Slug, tz, err)
	}

	// Next() returns the first firing STRICTLY after its argument, so stepping
	// back one nanosecond makes a `from` that lands exactly on a firing produce
	// that firing rather than skipping it. Without this, generating from
	// midnight for a midnight-daily event loses the first day.
	cursor := from.In(loc).Add(-time.Nanosecond)

	// A bound, not a limit: a caller asking for a century of a per-minute cron
	// would otherwise build 50 million windows in memory and take the process
	// with it. Anything hitting this is a misconfiguration, and erroring names
	// it rather than letting the box die anonymously.
	const maxWindows = 10000

	var out []pluginapi.EventWindow
	for {
		start := sched.Next(cursor)
		if start.IsZero() || start.After(until) {
			break
		}
		if len(out) >= maxWindows {
			return nil, fmt.Errorf("event %q: cron %q yields more than %d windows between %s and %s",
				ev.Slug, ev.Cron, maxWindows, from.Format(time.RFC3339), until.Format(time.RFC3339))
		}

		end := time.Time{}
		if ev.Duration > 0 {
			end = start.Add(ev.Duration)
		} else {
			// Contiguous: this window runs until the next firing. A cron that
			// fires only once in range (a yearly reset generated a month ahead)
			// has no next firing to close against, so it is left for a later
			// run rather than closed at an arbitrary point.
			end = sched.Next(start)
			if end.IsZero() {
				break
			}
		}
		out = append(out, pluginapi.EventWindow{Slug: ev.Slug, Starts: start, Ends: end})
		cursor = start
	}
	return out, nil
}

// NextStart returns when ev next opens strictly after `after`, or the zero time
// if it never does.
//
// Computed rather than read from the window table on purpose: the table only
// reaches as far as the horizon, so "next" would silently become "never" for an
// event whose next firing is a year out. A cron evaluation is cheap and always
// right.
func NextStart(ev pluginapi.ScheduledEvent, after time.Time) (time.Time, error) {
	if !ev.Enabled {
		return time.Time{}, nil
	}
	if ev.OneOff() {
		if ev.StartsAt == nil || !ev.StartsAt.After(after) {
			return time.Time{}, nil
		}
		return *ev.StartsAt, nil
	}
	sched, err := cronParser.Parse(ev.Cron)
	if err != nil {
		return time.Time{}, fmt.Errorf("event %q: parse cron %q: %w", ev.Slug, ev.Cron, err)
	}
	tz := ev.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("event %q: load timezone %q: %w", ev.Slug, tz, err)
	}
	next := sched.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, nil
	}
	return next, nil
}
