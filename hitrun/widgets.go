package hitrun

import (
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The hit-and-run widget.
//
// This is the one a member most needs in front of them, because it is the rule
// that STOPS them downloading. A warning they never saw is a warning that reads
// as the site breaking — see hitrun_web.go on the host, where the block is gin
// middleware over /tracker/download and its whole job is to be explicable.
//
// AT RISK leads, deliberately. A list of warnings is a bill; a list of what to
// reseed is something a member can still act on. Standing() returns both and
// the ordering here is the difference between a page that scolds and a page
// that helps.
//
// Registered only when the plugin is switched on, so a site without hit-and-run
// rules is never offered a widget that could not fire.

func (p *Plugin) registerWidgets(c *core.Core) {
	_ = c.RegisterWidget(core.Widget{
		Slug:        "hitrun-standing",
		Title:       "Hit and run",
		Description: "Your warnings, and the torrents that would become warnings.",
		Weight:      15,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if c.Auth == nil || p.st == nil {
				return "", nil
			}
			u, ok := c.Auth.CurrentUser(gc)
			if !ok || u == nil {
				return "", nil
			}
			st, err := p.st.Standing(gc.Request.Context(), u.ID)
			if err != nil {
				return "", nil
			}
			// The at-risk count is COMPUTED here, the same way /hitrun computes
			// it: the member's snatches judged by the live policy. It used to
			// read Standing.AtRisk — a field the store has never populated,
			// because at-risk is a POLICY judgment and the store deliberately
			// does not know the policy — so the widget's second figure was
			// permanently zero and the whole card vanished for a member with
			// no warnings but plenty about to become one, which is exactly the
			// member the card exists to warn.
			atRisk := 0
			if snatches, err := p.st.UserSnatches(gc.Request.Context(), u.ID); err == nil {
				pol := p.Policy()
				for _, s := range snatches {
					if AtRisk(pol, s.Snatch) {
						atRisk++
					}
				}
			}
			// Nothing owed and nothing at risk: render NOTHING. A permanent
			// "0 warnings" card is a rule shouting at somebody who is keeping
			// it, and the host drops an empty fragment rather than framing it.
			if st.ActiveWarnings == 0 && atRisk == 0 {
				return "", nil
			}

			out := `<dl class="key-value">`
			if st.ActiveWarnings > 0 {
				out += fmt.Sprintf(
					`<div class="key-value__group"><dt>Warnings</dt><dd>%d</dd></div>`,
					st.ActiveWarnings)
			}
			if atRisk > 0 {
				out += fmt.Sprintf(
					`<div class="key-value__group"><dt>At risk</dt><dd>%d</dd></div>`,
					atRisk)
			}
			out += `</dl>`
			// A way to act, not just a number. The member page lists which
			// torrents and what each still owes.
			out += `<a class="button button--outlined button--sm" href="/hitrun">What do I owe?</a>`
			return template.HTML(out), nil
		},
	})
}
