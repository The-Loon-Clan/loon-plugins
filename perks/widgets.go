package perks

import (
	"fmt"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The perks widget: what a member currently holds.
//
// Read from the in-memory Table rather than the store, because the Table is
// what the ANNOUNCE path consults — so a member sees the perks that are
// actually being applied to their traffic, not a row that says they should be.
// If the two ever disagree, the number a member is shown should be the one
// affecting them.
//
// A perk is spent on ONE torrent, which is the grain the whole plugin works at,
// so the widget counts holdings rather than claiming a site-wide state: "2
// freeleech" is true and "freeleech: on" would not be.

func (p *Plugin) registerWidgets(c *core.Core) {
	_ = c.RegisterWidget(core.Widget{
		Slug:        "perks-active",
		Title:       "Your perks",
		Description: "Freeleech and double-upload tokens currently in force.",
		Weight:      16,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if c.Auth == nil || p.table == nil {
				return "", nil
			}
			u, ok := c.Auth.CurrentUser(gc)
			if !ok || u == nil {
				return "", nil
			}
			active := p.table.ActiveFor(u.ID, time.Now())
			// Holding nothing renders nothing. A permanent "0 tokens" card
			// tells a member about a shop they did not ask about; the store
			// page is where that belongs.
			if len(active) == 0 {
				return "", nil
			}
			var free, dbl int
			for _, a := range active {
				switch a.Kind {
				case Freeleech:
					free++
				case UploadDouble:
					dbl++
				}
			}
			out := `<dl class="key-value">`
			if free > 0 {
				out += fmt.Sprintf(`<div class="key-value__group"><dt>Freeleech</dt><dd>%d</dd></div>`, free)
			}
			if dbl > 0 {
				out += fmt.Sprintf(`<div class="key-value__group"><dt>Double upload</dt><dd>%d</dd></div>`, dbl)
			}
			out += `</dl>`
			return template.HTML(out), nil
		},
	})
}
