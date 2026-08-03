package rewards

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Cross-table validation.
//
// Every table here enforces its own shape with CHECKs, and every one of them
// can be satisfied by a configuration that still pays nobody. An event with no
// windows is a valid event. A reward pointing at it is a valid reward. Together
// they are a reward that can never be earned, and nothing anywhere says so —
// the failure is silence, which is the worst kind, because the first report is
// a member asking why they never got their bonus.
//
// So these checks are deliberately about RELATIONSHIPS between rows, and about
// what will break SOON rather than only what is broken now: a daily reward
// whose windows run out in three days is working perfectly today.

type Severity string

const (
	// SeverityError — this cannot pay. Something is broken right now.
	SeverityError Severity = "error"
	// SeverityWarn — this works today and will stop, or is drifting.
	SeverityWarn Severity = "warn"
	// SeverityInfo — legal, probably not what was meant.
	SeverityInfo Severity = "info"
)

// Finding is one problem, in the form an operator can act on: what is wrong,
// and what to do. A finding with no Fix is a complaint rather than a report.
type Finding struct {
	Severity Severity
	Subject  string
	Problem  string
	Fix      string
}

// Coverage is one event's window health.
type Coverage struct {
	EventID int64     `db:"event_id"`
	Windows int       `db:"windows"`
	LastEnd time.Time `db:"last_end"`
	// Gaps counts consecutive windows where one ends before the next begins.
	// Meaningless for a season (gaps are the point); for a contiguous reset it
	// counts periods during which the reward simply did not exist.
	Gaps int `db:"gaps"`
}

// windowRunway is how little lookahead is worth warning about. The generator
// keeps 45 days; below a week means it has not run for over a month, and the
// day it hits zero a daily reward silently stops existing.
const windowRunway = 7 * 24 * time.Hour

// Validate cross-checks the whole configuration. Read-only.
func (p *Plugin) Validate(ctx context.Context) ([]Finding, error) {
	now := time.Now()
	events, err := p.admin.ListEventStats(ctx, now)
	if err != nil {
		return nil, err
	}
	rewards, err := p.admin.ListRewards(ctx)
	if err != nil {
		return nil, err
	}
	coverage, err := p.admin.EventCoverage(ctx)
	if err != nil {
		return nil, err
	}
	stale, err := p.admin.CountStalePending(ctx, now)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]EventStats, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}

	var out []Finding
	out = append(out, validateEvents(events, coverage, now)...)
	out = append(out, validateRewards(rewards, byID, p.handlerKinds())...)

	if stale > 0 {
		out = append(out, Finding{
			Severity: SeverityWarn,
			Subject:  "grants",
			Problem:  fmt.Sprintf("%d pending grant(s) are past their expiry but still pending", stale),
			Fix:      "the Reward Windows job expires them; check it is running in /admin/jobs",
		})
	}

	// Errors first, then warnings — an operator scanning this needs the thing
	// that is broken now above the thing that will break next month.
	sort.SliceStable(out, func(i, j int) bool {
		return severityRank(out[i].Severity) < severityRank(out[j].Severity)
	})
	return out, nil
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarn:
		return 1
	}
	return 2
}

// handlerKinds is which payout kinds this process can actually execute.
func (p *Plugin) handlerKinds() map[PayoutKind]bool {
	out := make(map[PayoutKind]bool, len(p.engine.handlers))
	for k := range p.engine.handlers {
		out[k] = true
	}
	return out
}

func validateEvents(events []EventStats, coverage map[int64]Coverage, now time.Time) []Finding {
	var out []Finding
	for _, e := range events {
		if !e.Enabled {
			continue
		}
		cov := coverage[e.ID]
		subject := "event " + e.Slug

		if cov.Windows == 0 {
			if e.Cron == nil {
				out = append(out, Finding{SeverityError, subject,
					"one-off event with no windows — nothing generates windows for an event with no cron",
					"author its windows by hand, or give it a cron"})
			} else {
				out = append(out, Finding{SeverityError, subject,
					"has a cron but no windows at all",
					"trigger the Reward Windows job in /admin/jobs"})
			}
			continue
		}

		// A contiguous reset must have no gaps: every instant belongs to a
		// window, so a gap is a period during which the reward did not exist.
		if e.Duration == nil && cov.Gaps > 0 {
			out = append(out, Finding{SeverityWarn, subject,
				fmt.Sprintf("%d gap(s) between windows, but this event has no duration so its windows should be contiguous", cov.Gaps),
				"the generator was interrupted; the gaps are past periods nobody could earn and cannot be back-filled safely once grants exist"})
		}

		if e.Cron != nil {
			runway := cov.LastEnd.Sub(now)
			switch {
			case runway <= 0:
				out = append(out, Finding{SeverityError, subject,
					"every window is in the past — nothing is earnable on this event now",
					"trigger the Reward Windows job in /admin/jobs"})
			case runway < windowRunway:
				out = append(out, Finding{SeverityWarn, subject,
					fmt.Sprintf("windows run out in %s", runway.Round(time.Hour)),
					"the generator keeps 45 days ahead; this short means it has not run in a while"})
			}
		}
	}
	return out
}

func validateRewards(rewards []Reward, events map[int64]EventStats, handled map[PayoutKind]bool) []Finding {
	var out []Finding
	for _, r := range rewards {
		if !r.Enabled {
			// A disabled reward that is misconfigured is not a problem yet,
			// and reporting it would bury the live ones.
			continue
		}
		subject := "reward " + r.Slug

		if len(r.Payouts) == 0 {
			out = append(out, Finding{SeverityError, subject,
				"enabled but has no payout lines — every grant will be refused",
				"add a payout line, or disable it"})
		}
		for _, pay := range r.Payouts {
			if !handled[pay.Kind] {
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("pays %q, but no handler is registered for that kind — every grant will be refused", pay.Kind),
					"register a rewards.payout." + string(pay.Kind) + " extension, or change the payout line"})
			}
		}

		if r.EventID != nil {
			ev, known := events[*r.EventID]
			if !known {
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("references event %d, which does not exist", *r.EventID),
					"point it at a real event"})
			} else if !ev.Enabled {
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("gated by event %q, which is disabled — it can never be earned", ev.Slug),
					"enable the event, or the reward is dead weight"})
			} else if ev.Current == nil && ev.Duration == nil {
				// A season being closed is normal. A contiguous reset having
				// no open window is not: some window should always contain now.
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("event %q has no window open right now, and it is a reset, so one always should be", ev.Slug),
					"trigger the Reward Windows job in /admin/jobs"})
			}
		}

		if r.ExpiresAfter != nil && r.Delivery == DeliveryAuto {
			out = append(out, Finding{SeverityInfo, subject,
				"has an expiry but delivery=auto, so it settles immediately and the expiry never applies",
				"expiry is for delivery=claim, where a grant waits to be collected"})
		}
		if r.Kind == KindPerUnit && r.EventID != nil {
			out = append(out, Finding{SeverityInfo, subject,
				"is per_unit AND gated by an event — the event still gates earning, but the reference is the high-water mark, not the window",
				"usually per_unit rewards want no event at all"})
		}
		// A per_unit reward is granted by a job reading its UnitSource, never
		// offered by a surface, so it has no use for a trigger. Warning about
		// one was this check's first false positive in production -- on the
		// tenure reward, whose whole design is job-driven.
		if r.Trigger == "" && r.Delivery == DeliveryClaim && r.Kind != KindPerUnit {
			out = append(out, Finding{SeverityWarn, subject,
				"delivery=claim with no trigger — no surface asks for it, so nobody will ever be offered it",
				"set a trigger (e.g. login), or make it per_unit and grant it from a job"})
		}
	}
	return out
}
