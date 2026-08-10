package seedlock

import (
	"fmt"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The seedlock widget: which torrents this member currently has claimed, and
// from where.
//
// This one exists because of what seedlock DOES when it refuses: a client that
// is turned away gets a bencoded failure reason, which most clients show once
// and then bury in a log. A member whose second machine stopped seeding needs
// somewhere on the site that says why, and a widget they can put in front of
// themselves is the cheapest version of that.
//
// Registered whatever the enabled flag says. A claim outlives the switch — the
// lock window is a Redis TTL, so turning the rule off does not release what is
// already held, and a member has to be able to see and clear it.

func (p *Plugin) registerWidgets(c *core.Core) {
	_ = c.RegisterWidget(core.Widget{
		Slug:        "seedlock-claims",
		Title:       "Seeding locks",
		Description: "Torrents claimed by one of your hosts, and when each lock lapses.",
		Weight:      17,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if c.Auth == nil || p.st == nil {
				return "", nil
			}
			u, ok := c.Auth.CurrentUser(gc)
			if !ok || u == nil {
				return "", nil
			}
			// Redis being unreachable is not something to render an error box
			// about on somebody else's page — seedlock fails OPEN everywhere
			// else for the same reason, and a widget is the least important
			// place to break that rule.
			claims, err := p.st.HeldBy(gc.Request.Context(), u.ID)
			if err != nil || len(claims) == 0 {
				return "", nil
			}
			now := time.Now()
			out := `<dl class="key-value">`
			out += fmt.Sprintf(`<div class="key-value__group"><dt>Locked torrents</dt><dd>%d</dd></div>`, len(claims))
			// The soonest lapse is the useful single figure: it is when a
			// member could next legitimately move to another machine.
			//
			// Derived, not stored. A Claim carries the claiming host and its
			// LastSeen; the lock is a Redis TTL of LockMinutes PAST that
			// announce, so expiry is LastSeen + the window. Reading it off the
			// policy means the figure shown and the rule enforced cannot drift
			// apart when an operator changes the window.
			window := time.Duration(p.cfg.normalise().LockMinutes) * time.Minute
			var soonest time.Time
			for _, cl := range claims {
				if cl.LastSeen.IsZero() {
					continue
				}
				exp := cl.LastSeen.Add(window)
				if soonest.IsZero() || exp.Before(soonest) {
					soonest = exp
				}
			}
			if !soonest.IsZero() && soonest.After(now) {
				mins := int(soonest.Sub(now).Minutes()) + 1
				out += fmt.Sprintf(`<div class="key-value__group"><dt>Next lapses</dt><dd>in %d min</dd></div>`, mins)
			}
			out += `</dl>`
			out += `<a class="button button--outlined button--sm" href="/seedlock">Manage locks</a>`
			return template.HTML(out), nil
		},
	})
}
