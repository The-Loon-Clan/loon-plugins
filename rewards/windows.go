package rewards

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser matches the standard five-field expression ("0 0 1 7 *") plus the
// @daily / @weekly descriptors. Deliberately NOT robfig's seconds-optional
// parser: a five-field expression fed to a six-field parser silently shifts
// every field by one, so "0 0 1 7 *" would mean something entirely different
// and windows would appear on the wrong days with no error anywhere.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ValidateCron reports whether an expression can generate windows. Called at
// the write boundary — an admin or an MCP client saving an event — so a bad
// expression fails where someone can read the message, not silently at 3am
// inside the generator.
func ValidateCron(expr string) error {
	_, err := cronParser.Parse(expr)
	return err
}

// GenerateWindows returns the windows an event should have between from and
// until, in order. It does not touch the database: the caller inserts them and
// lets UNIQUE (event_id, starts_at) absorb any overlap with what already
// exists, which is what makes running the generator twice harmless.
//
// The two shapes differ only in how a window ENDS:
//
//   - duration set — [fire, fire+duration). A season. There are gaps: outside
//     it no window contains "now" at all, so the reward is not earnable.
//   - duration nil — [fire, next fire). A reset. Contiguous, so some window
//     always contains "now" and the reward is always earnable, once per window.
//
// Windows are half-open. A member acting exactly at ends_at falls in the next
// one — with a contiguous reset the alternative is an instant belonging to two
// windows, which is a second free claim every midnight.
func GenerateWindows(ev Event, from, until time.Time) ([]Window, error) {
	if ev.Cron == nil || *ev.Cron == "" {
		// A one-off event's windows are authored by hand. Returning none is
		// correct rather than an error: the generator runs across every event
		// and must not care that some are not its business.
		return nil, nil
	}
	sched, err := cronParser.Parse(*ev.Cron)
	if err != nil {
		return nil, fmt.Errorf("event %q: parse cron %q: %w", ev.Slug, *ev.Cron, err)
	}
	loc, err := time.LoadLocation(ev.Timezone)
	if err != nil {
		return nil, fmt.Errorf("event %q: load timezone %q: %w", ev.Slug, ev.Timezone, err)
	}
	if until.Before(from) {
		return nil, nil
	}

	// Next() returns the first firing STRICTLY after its argument, so stepping
	// back one nanosecond makes a `from` that lands exactly on a firing
	// produce that firing rather than skipping it. Without this, generating
	// from midnight for a midnight-daily event loses the first day.
	cursor := from.In(loc).Add(-time.Nanosecond)

	var out []Window
	// A bound, not a limit: a caller asking for a century of a per-minute cron
	// would otherwise build 50 million windows in memory and take the process
	// with it. Anything hitting this is a misconfiguration, and erroring names
	// it rather than letting the box die anonymously.
	const maxWindows = 10000
	for {
		start := sched.Next(cursor)
		if start.IsZero() || start.After(until) {
			break
		}
		if len(out) >= maxWindows {
			return nil, fmt.Errorf("event %q: cron %q yields more than %d windows between %s and %s",
				ev.Slug, *ev.Cron, maxWindows, from.Format(time.RFC3339), until.Format(time.RFC3339))
		}

		var end time.Time
		if ev.Duration != nil {
			end = start.Add(*ev.Duration)
		} else {
			// Contiguous: this window runs until the next firing. A cron that
			// fires only once in range (a yearly reset generated a month
			// ahead) has no next firing to close against, so it is left for a
			// later run rather than closed at an arbitrary point.
			end = sched.Next(start)
			if end.IsZero() {
				break
			}
		}
		out = append(out, Window{EventID: ev.ID, StartsAt: start, EndsAt: end})
		cursor = start
	}
	return out, nil
}
