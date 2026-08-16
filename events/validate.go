package events

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Window-health validation.
//
// Every table here enforces its own shape with CHECKs, and every one of them can
// be satisfied by a configuration that opens nothing. An event with no windows is
// a valid event. A reward or a news post gated on it is valid too. Together they
// are a thing that can never happen, and nothing says so — the failure is
// SILENCE, which is the worst kind, because the first report is a member asking
// why they never got their bonus.
//
// These checks moved here with the tables. Rewards used to run them, and when the
// events plugin took the generator it took the responsibility: a second opinion
// computed from the rewards side could only ever disagree with the authority.
// They were briefly checked NOWHERE, which is the gap this file closes.
//
// Deliberately about what will break SOON as well as what is broken now. A daily
// reset whose windows run out in three days is working perfectly today.

type Severity string

const (
	// SeverityError — this cannot open. Something is broken right now.
	SeverityError Severity = "error"
	// SeverityWarn — this works today and will stop, or is drifting.
	SeverityWarn Severity = "warn"
	// SeverityInfo — legal, probably not what was meant.
	SeverityInfo Severity = "info"
)

// Finding is one problem, in the form an operator can act on: what is wrong, and
// what to do. A finding with no Fix is a complaint rather than a report.
type Finding struct {
	Severity Severity
	Subject  string
	Problem  string
	Fix      string
}

// Coverage is one event's window health, computed in a single query.
type Coverage struct {
	Slug    string    `db:"slug"`
	Windows int       `db:"windows"`
	LastEnd time.Time `db:"last_end"`
	// Gaps counts consecutive windows where one ends before the next begins.
	// Meaningless for a bounded event (gaps are the point); for a contiguous one
	// it counts periods during which the event simply did not exist.
	Gaps int `db:"gaps"`
}

// windowRunway is how little lookahead is worth warning about.
//
// The generator keeps windowHorizon (45 days) and runs every 30 minutes, so
// below a week means it has not completed a pass in well over a month. The day
// it reaches zero, anything gated on the event silently stops happening.
const windowRunway = 7 * 24 * time.Hour

// Validate reports every event's window health. Read-only.
func (p *Plugin) Validate(ctx context.Context) ([]Finding, error) {
	evs, err := p.store.ListEvents(ctx)
	if err != nil {
		return nil, err
	}
	cov, err := p.store.Coverage(ctx)
	if err != nil {
		return nil, err
	}
	out := validateEvents(evs, cov, time.Now())
	// Errors first. An operator scanning this needs the things that are broken
	// above the things that are merely untidy.
	sort.SliceStable(out, func(i, j int) bool { return severityRank(out[i].Severity) < severityRank(out[j].Severity) })
	return out, nil
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarn:
		return 1
	default:
		return 2
	}
}

// validateEvents is the pure half, so the cases can be tested without a database.
//
// Ported from the rewards plugin with three adaptations the new schema forces.
// All three are cases the old checks would now report as broken when they are
// not, and a validator that cries wolf is one an operator learns to ignore —
// which is how it stops working the day it matters.
func validateEvents(evs []pluginapi.ScheduledEvent, cov map[string]Coverage, now time.Time) []Finding {
	var out []Finding
	for _, ev := range evs {
		if !ev.Enabled {
			// A disabled event opening nothing is not a fault, it is the point.
			continue
		}
		c := cov[ev.Slug]
		subject := "event " + ev.Slug

		if c.Windows == 0 && ev.Triggered() {
			// Normal. A triggered event has no windows until something opens
			// one, and most of the time nothing has — a site-freeleech event on
			// a site that has not met its goal this month is correctly idle,
			// not broken. Reporting it as an error would train an operator to
			// ignore this page.
			continue
		}
		if c.Windows == 0 {
			// ADAPTATION 1: a one-off whose start is beyond the generation
			// horizon legitimately has no windows yet. Rewards never generated
			// for cron-less events at all, so "no windows" was unambiguous
			// there; here it is only a fault if the generator SHOULD have made
			// one by now.
			if ev.OneOff() {
				if ev.StartsAt != nil && ev.StartsAt.After(now) {
					out = append(out, Finding{SeverityInfo, subject,
						fmt.Sprintf("no window yet — it starts %s, beyond the %d-day generation horizon",
							ev.StartsAt.UTC().Format("2006-01-02"), int(windowHorizon.Hours()/24)),
						"nothing to do; the generator will create it as the date approaches"})
					continue
				}
				out = append(out, Finding{SeverityError, subject,
					"one-off whose start has passed but has no window — the generator has not created it",
					"trigger the Event Windows job in /admin/jobs"})
				continue
			}
			out = append(out, Finding{SeverityError, subject,
				"has a cron but no windows at all — nothing gated on this can ever open",
				"trigger the Event Windows job in /admin/jobs"})
			continue
		}

		// A contiguous event must have no gaps: every instant belongs to a
		// window, so a gap is a period during which the event did not exist.
		if !ev.OneOff() && ev.Duration == 0 && c.Gaps > 0 {
			out = append(out, Finding{SeverityWarn, subject,
				fmt.Sprintf("%d gap(s) between windows, but this event has no duration so its windows should be contiguous", c.Gaps),
				"the generator was interrupted; the gaps are past periods nobody could act in, and back-filling them once consumers have keyed work on the windows is not safe"})
		}

		// ADAPTATION 2: the runway check applies to RECURRING events only, and it
		// covers two cases at once.
		//
		// A bounded one-off whose window has closed has not "run out" — it is
		// over, which is what a one-off does. And a PERPETUAL one-off never runs
		// out at all: its stored end is the year-9999 sentinel, so a runway
		// measured against it would report eight thousand years of headroom,
		// which is true and useless.
		//
		// This started as two guards, one per case. A mutation check deleting the
		// perpetual one changed nothing, which is how it became clear the second
		// was unreachable: only a one-off can be perpetual (the generator writes
		// the sentinel nowhere else), so this exemption had already caught it.
		// The test for the perpetual case stays, because the property has to hold
		// whichever branch enforces it.
		if ev.OneOff() {
			continue
		}
		switch runway := c.LastEnd.Sub(now); {
		case runway <= 0:
			out = append(out, Finding{SeverityError, subject,
				"every window is in the past — nothing gated on this event can open now",
				"trigger the Event Windows job in /admin/jobs"})
		case runway < windowRunway:
			out = append(out, Finding{SeverityWarn, subject,
				fmt.Sprintf("windows run out in %s", runway.Round(time.Hour)),
				fmt.Sprintf("the generator keeps %d days ahead; this short means it has not completed a pass in over a month",
					int(windowHorizon.Hours()/24))})
		}
	}
	return out
}
