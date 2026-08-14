package usenet

import (
	"context"
	"fmt"
)

// The junk-drop debug list, on the Filters tab.
//
// The hits table above it counts drops by RULE, which answers "which rule is
// doing the work". This answers the question that one cannot: for the postings
// we threw away, what were they actually? The rules judge a subject, and an
// obfuscated post's subject is the part the poster scrambled — so a drop can be
// correct about the subject and wrong about the posting.
//
// It is a debug surface, not a feature: nothing here is indexed, and until the
// probe is enabled the rows show "not asked". The recovery rate is the number
// that decides whether the crawler's drop is costing us releases.

// junkDropsRows is how many drops the card lists. Enough to see the shapes,
// short enough that the card stays a card — the counters above it are what
// carry the finding, and this is the evidence under them.
const junkDropsRows = 25

type junkDropRowVM struct {
	Group     string
	Subject   string
	Rule      string
	Recovered string
	// Verdict is the one-word reading of the row: what the body said, judged by
	// the same rules that dropped the subject.
	Verdict string
	Seen    string
}

type junkDropsVM struct {
	Enabled bool
	Rows    []junkDropRowVM
	Sampled int
	Probed  int
	Real    int
	Junk    int
	// Rate is the finding: of the drops we asked about, how many named a real
	// file. Rendered in Go because these fragments parse without a FuncMap —
	// arithmetic in the template fails the whole render at runtime.
	Rate string
	// Note explains an empty or partial card, which otherwise reads as "we
	// checked and there is nothing", the opposite of the truth.
	Note string
}

func (p *Plugin) renderJunkDrops(ctx context.Context) (junkDropsVM, error) {
	cfg := p.effective(ctx)
	vm := junkDropsVM{Enabled: cfg.JunkProbeEnabled}

	rep, err := p.st.junkDropsReport(ctx, junkDropsRows)
	if err != nil {
		return vm, err
	}
	vm.Sampled, vm.Probed = rep.Sampled, rep.Probed
	vm.Real, vm.Junk = rep.Real, rep.Junk

	for _, r := range rep.Rows {
		row := junkDropRowVM{
			Group: r.Group, Subject: r.Subject, Rule: r.Rule,
			Recovered: r.Recovered, Seen: fmtTime(r.Seen),
		}
		switch {
		case !r.Probed:
			row.Verdict = "not asked"
		case r.Recovered == "":
			// Either the article is gone or it carries no yEnc header. Both
			// mean the body cannot settle it, which is different from the body
			// agreeing with the drop.
			row.Verdict = "no answer"
		case whichJunkRule(r.Recovered) != "":
			row.Verdict = "junk too"
		default:
			row.Verdict = "real file"
		}
		vm.Rows = append(vm.Rows, row)
	}

	switch {
	case rep.Sampled == 0:
		vm.Note = fmt.Sprintf("No drops sampled yet. The corpus samples 1 subject in %d, so this fills at crawl speed rather than at drop speed.", 1<<corpusSampleShift)
	case !cfg.JunkProbeEnabled:
		vm.Note = fmt.Sprintf("%s drops recorded, none asked about. Enable the junk probe in settings to read their bodies — it spends provider bytes, roughly one segment per drop.", fmtComma(int64(rep.Sampled)))
	case rep.Probed == 0:
		vm.Note = "Probe enabled; the first pass has not run yet."
	}
	if answered := rep.Real + rep.Junk; answered > 0 {
		vm.Rate = fmt.Sprintf("%d%%", rep.Real*100/answered)
	}
	return vm, nil
}
