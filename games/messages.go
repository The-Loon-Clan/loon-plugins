package games

import (
	"bytes"
	"context"
)

// The words a member reads, rendered from this plugin's own templates.
//
// WHY A GO FILE EXISTS FOR THIS AT ALL. Every other plugin here maps its codes
// inside the page template and needs nothing more, because the page IS the
// only surface. games has two: its own pages, and the STORE, which sells
// charity as an item and shows the buyer whatever text `Grant` returns.
//
// The store renders through the STORE's templates. games cannot put a sentence
// there, and the store cannot map games' codes -- it would have to know every
// selling plugin's vocabulary. So the plugin that owns the words renders them
// itself, here, and hands over finished text.
//
// That keeps ONE copy of each sentence. pot.html and charity.html call the
// same {{define "refusal"}} this does, so a translator has one place to work
// and the two surfaces cannot drift apart.

// refusalVM is a refusal code plus every number any refusal quotes.
//
// All of them, always, rather than a variant per message: a template that
// interpolates {{.DailyMax}} needs the field to exist whichever branch runs,
// and a handful of unused int64s is cheaper than four view models.
type refusalVM struct {
	Code                string
	DailyMax, LeftToday int64
	Min, Max            int64
}

// grantedVM is the store's success line for a charity purchase.
type grantedVM struct {
	Pts     int64
	Members int
}

// refusalText renders a refusal code for a surface that takes TEXT, not a
// code -- which today means the store.
//
// A template failure returns the code itself rather than "". The code is not a
// sentence, but it names what happened, and a member who reads "nomatch" can
// at least quote it; an empty refusal reads as success, which is the one
// outcome worth ruling out.
func (p *Plugin) refusalText(ctx context.Context, code string) string {
	vm := refusalVM{Code: code}
	if p.st != nil {
		if cfg, err := p.st.Settings(ctx); err == nil {
			vm.DailyMax = cfg.PotDailyMax
			vm.Min, vm.Max = cfg.CharityMin, cfg.CharityMax
		}
	}
	return p.renderMessage("refusal", vm, code)
}

// grantedText renders the store's success line.
func (p *Plugin) grantedText(pts int64, members int) string {
	return p.renderMessage("granted", grantedVM{Pts: pts, Members: members}, "")
}

// renderMessage executes one of the message defines, falling back to fallback
// when the templates are not loaded -- which is the case in unit tests that
// build a bare Plugin, and would otherwise panic on a nil template set.
func (p *Plugin) renderMessage(name string, data any, fallback string) string {
	if p.tmpl == nil {
		return fallback
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return fallback
	}
	return buf.String()
}
