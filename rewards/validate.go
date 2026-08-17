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
	rewards, err := p.admin.ListRewards(ctx)
	if err != nil {
		return nil, err
	}
	// Which scheduled events exist, asked of the plugin that owns them.
	//
	// nil when there is no events plugin wired, and that is NOT the same as an
	// empty set: nil means "cannot tell", empty means "asked, and there are
	// none". validateRewards reports a dangling slug only in the second case,
	// because complaining that an event is missing when nobody can see the list
	// is noise an operator cannot act on.
	var knownEvents map[string]bool
	if p.events != nil {
		evs, err := p.events.Events(ctx)
		if err != nil {
			return nil, err
		}
		knownEvents = make(map[string]bool, len(evs))
		for _, ev := range evs {
			knownEvents[ev.Slug] = ev.Enabled
		}
	}
	catalogue, err := p.Catalogue(ctx)
	if err != nil {
		return nil, err
	}
	stale, err := p.admin.CountStalePending(ctx, now)
	if err != nil {
		return nil, err
	}

	// Event window coverage is no longer checked here. The events plugin owns
	// its own generator and its admin page reports coverage per event, so a
	// second opinion computed from this side could only ever disagree.
	// Achievement validation moved out with the achievements plugin, for the
	// same ownership reason: the criterion, the metric sources and the
	// payability question all live on that side of the seam now.
	var out []Finding
	out = append(out, validateRewards(rewards, knownEvents, p.handlerKinds())...)
	out = append(out, validateCatalogue(catalogue, rewards)...)

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

// knownEvents is the set of scheduled-event slugs the events plugin reports, or
// nil when there is no events plugin wired. Nil and empty mean different things
// here and the distinction matters: nil means "cannot tell", where an empty set
// means "asked, and there are none".
func validateRewards(rewards []Reward, knownEvents map[string]bool, handled map[PayoutKind]bool) []Finding {
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

		// The scheduled event, checked against what the events plugin reports.
		//
		// knownEvents nil means there is no events plugin to ask, and silence is
		// right then: telling an operator their event does not exist when nobody
		// can see the list is a finding they cannot act on. Window coverage is no
		// longer checked from this side either -- the events plugin owns the
		// generator and reports coverage on its own page, so a second opinion
		// computed here could only ever disagree with the authority.
		if r.EventSlug != "" && knownEvents != nil {
			enabled, known := knownEvents[r.EventSlug]
			if !known {
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("gated by scheduled event %q, which does not exist", r.EventSlug),
					"point it at a real event in /admin/p/events, or clear the event"})
			} else if !enabled {
				out = append(out, Finding{SeverityError, subject,
					fmt.Sprintf("gated by scheduled event %q, which is disabled — it can never be earned", r.EventSlug),
					"enable the event, or the reward is dead weight"})
			}
		}

		if r.ExpiresAfter != nil && r.Delivery == DeliveryAuto {
			out = append(out, Finding{SeverityInfo, subject,
				"has an expiry but delivery=auto, so it settles immediately and the expiry never applies",
				"expiry is for delivery=claim, where a grant waits to be collected"})
		}
		// The high-water mark counts every grant, expired ones included — that
		// is what stops an expired grant being re-paid. The corollary for
		// per_unit is that lapsing PERMANENTLY burns the units the grant
		// covered: the mark has moved past them and nothing can move it back.
		// Legal, and possibly even wanted, but never an accident anyone meant.
		if r.Kind == KindPerUnit && r.Delivery == DeliveryClaim && r.ExpiresAfter != nil {
			out = append(out, Finding{SeverityWarn, subject,
				"per_unit with delivery=claim and an expiry — a member who misses the window loses those units forever, because the mark advances whether or not the grant was collected",
				"drop the expiry (a claim that waits costs nothing), or make delivery=auto"})
		}
		if r.Kind == KindPerUnit && r.EventSlug != "" {
			out = append(out, Finding{SeverityInfo, subject,
				"is per_unit AND gated by a scheduled event — the event still gates earning, but the reference names the high-water mark, not the occurrence",
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

// validateAchievements lived here until the achievements plugin moved out.
// Its concerns split with the code: the criterion checks went with the
// plugin, and the payability half (an achievement pointing at a disabled,
// deleted, payout-less or wrong-kind reward) is now reported LAZILY by that
// plugin's scoring job — eager reporting would require reading this schema's
// tables from over there, which is the coupling the split removed.

// validateCatalogue cross-checks the declared vocabulary against what is
// actually configured.
//
// Two ways a catalogue and a configuration drift apart, both silent: a reward
// pointing at a key the catalogue does not contain (usually a rename, and the
// row simply stops working), and a catalogue that is empty, which is not an
// error but is worth saying, because it means the trigger picker is still
// free text. (The achievement-metric checks, and the counter-with-no-
// MetricSource check, moved out with the achievements plugin — its pickers no
// longer read this catalogue.)
func validateCatalogue(cat SourceCatalog, rewards []Reward) []Finding {
	if len(cat) == 0 {
		if len(rewards) == 0 {
			return nil // nothing configured yet; nothing to say
		}
		return []Finding{{
			Severity: SeverityInfo,
			Subject:  "catalogue",
			Problem:  "no source catalogue is registered, so the trigger picker is free text",
			Fix:      "register a rewards.SourceCatalog under " + SourceCatalogExtension + " (see StockSources)",
		}}
	}

	known := map[string]SourceDef{}
	for _, d := range cat {
		known[d.Key] = d
	}

	var out []Finding
	for _, r := range rewards {
		if !r.Enabled || r.Trigger == "" {
			continue
		}
		d, ok := known[r.Trigger]
		if !ok {
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "reward " + r.Slug,
				Problem:  fmt.Sprintf("fires on %q, which the catalogue does not declare — nothing will ever fire it", r.Trigger),
				Fix:      "pick a declared trigger, or add it to the catalogue",
			})
			continue
		}
		if !d.Fires {
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "reward " + r.Slug,
				Problem:  fmt.Sprintf("fires on %q, which is a counter rather than an event", d.Key),
				Fix:      "pick a source that fires, or make this a per_unit reward scored by the counter",
			})
		}
	}
	return out
}
