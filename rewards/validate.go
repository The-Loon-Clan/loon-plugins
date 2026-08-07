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
	achievements, err := p.admin.ListAchievementDefs(ctx)
	if err != nil {
		return nil, err
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
	var out []Finding
	out = append(out, validateRewards(rewards, knownEvents, p.handlerKinds())...)

	rewardsByID := make(map[int64]Reward, len(rewards))
	for _, r := range rewards {
		rewardsByID[r.ID] = r
	}
	out = append(out, validateAchievements(achievements, rewardsByID, p.metricNames())...)
	out = append(out, validateCatalogue(catalogue, rewards, achievements, p.metricNames())...)

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

// validateAchievements is the guard that keeps repeatability from having two
// sources of truth.
//
// An achievement declares no repeatability of its own — deliberately, because
// rewards.kind already does and the engine enforces it through the reference
// it computes per grant. What remains is making sure the reward an achievement
// points at is one whose repeatability MEANS anything for a criterion that
// latches:
//
//   - one_off   — earn once, ever. The only kind allowed today.
//   - recurring — once per event window. Coherent (a seasonal achievement),
//     but not enabled yet; enabling it is a change here, not a migration.
//   - per_unit  — incoherent. per_unit pays per delta forever, while a
//     criterion latches the moment it is met. An achievement on one would
//     complete once and then keep paying on every subsequent unit, which is
//     not an achievement at all.
//
// The other silent failure this catches is a criterion nothing can ever score:
// a metric with no registered source is inert, exactly as a payout kind with
// no handler is, and an operator has no way to see that from the admin page.
func validateAchievements(defs []AchievementDef, rewards map[int64]Reward, metrics map[string]bool) []Finding {
	var out []Finding
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		r, ok := rewards[d.RewardID]
		switch {
		case !ok:
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + d.Slug,
				Problem:  "points at a reward that does not exist",
				Fix:      "pick an existing reward, or disable the achievement",
			})
		case !r.Enabled:
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + d.Slug,
				Problem:  fmt.Sprintf("pays reward %q, which is disabled — it can be earned but not paid", r.Slug),
				Fix:      "enable the reward, or disable the achievement so it stops offering",
			})
		case r.Kind == KindPerUnit:
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + d.Slug,
				Problem: fmt.Sprintf("pays reward %q, which is per_unit — a criterion latches once, "+
					"but per_unit keeps paying on every later unit", r.Slug),
				Fix: "point it at a one_off reward",
			})
		case r.Kind != KindOneOff:
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + d.Slug,
				Problem:  fmt.Sprintf("pays reward %q of kind %s; achievements are one_off today", r.Slug, r.Kind),
				Fix:      "point it at a one_off reward",
			})
		}

		if d.Metric == "" {
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + d.Slug,
				Problem:  "has no metric, so nothing can ever score it",
				Fix:      "set the metric to a counter the site registers",
			})
			continue
		}
		if !metrics[d.Metric] {
			// Warn, not error: a host that has not booted its source yet is a
			// deployment ordering problem, and refusing would make the admin
			// page unusable during one.
			out = append(out, Finding{
				Severity: SeverityWarn,
				Subject:  "achievement " + d.Slug,
				Problem:  fmt.Sprintf("scored by metric %q, which no source is registered for — progress will never move", d.Metric),
				Fix:      "register a MetricSource under " + MetricSourcePrefix + d.Metric + ", or retire the achievement",
			})
		}
	}
	return out
}

// validateCatalogue cross-checks the declared vocabulary against what is
// actually configured and actually registered.
//
// Three ways a catalogue and a configuration drift apart, all of them silent:
// a reward or achievement pointing at a key the catalogue does not contain
// (usually a rename, and the row simply stops working); a source that says it
// Counts but has no MetricSource behind it, so every achievement on it sits at
// zero forever; and a catalogue that is empty, which is not an error but is
// worth saying, because it means every picker is still free text.
func validateCatalogue(cat SourceCatalog, rewards []Reward, achievements []AchievementDef, metrics map[string]bool) []Finding {
	if len(cat) == 0 {
		if len(rewards) == 0 && len(achievements) == 0 {
			return nil // nothing configured yet; nothing to say
		}
		return []Finding{{
			Severity: SeverityInfo,
			Subject:  "catalogue",
			Problem:  "no source catalogue is registered, so the trigger and metric pickers are free text",
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

	for _, a := range achievements {
		if !a.Enabled || a.Metric == "" {
			continue
		}
		d, ok := known[a.Metric]
		if !ok {
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + a.Slug,
				Problem:  fmt.Sprintf("scored by %q, which the catalogue does not declare", a.Metric),
				Fix:      "pick a declared metric, or add it to the catalogue",
			})
			continue
		}
		if !d.Counts {
			out = append(out, Finding{
				Severity: SeverityError,
				Subject:  "achievement " + a.Slug,
				Problem:  fmt.Sprintf("scored by %q, which is an event rather than a counter — a threshold needs something to count", d.Key),
				Fix:      "pick a source that counts",
			})
		}
	}

	// A declared counter with nothing behind it. Warn rather than error: this
	// is what a half-deployed host looks like during a rollout.
	for _, d := range cat {
		if d.Counts && !metrics[d.Key] {
			out = append(out, Finding{
				Severity: SeverityWarn,
				Subject:  "source " + d.Key,
				Problem: "is declared as a counter but no MetricSource is registered, so an " +
					"achievement on it counts only what happens from now on and can never reflect history",
				Fix: "register one under " + MetricSourcePrefix + d.Key + ", or clear its Counts flag",
			})
		}
	}
	return out
}
