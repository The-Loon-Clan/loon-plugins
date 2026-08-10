package ranks

import (
	"context"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The ranks catalog as a placeable widget.
//
// It answers the question a member actually asks — "what do I get, and what
// would I get one rank up" — which the admin catalog page cannot, because that
// page is for editing and is admin-only. Built from the SAME rows the admin
// edits, so raising a rank's allowance updates the card with no second edit;
// the alternative, a hand-written table in host markup, is the arrangement
// that goes stale the first time a number changes and nobody notices.
//
// Placeable rather than a fixed slot (core.Widget, not core.View): the natural
// homes for it are a profile sidebar, the API-key page and a "why upgrade"
// page, and which of those a site wants is not this plugin's business.
//
// Any active API boost is rendered on top, resolved through
// pluginapi.LimitBoostName so it is the very number the host enforces. A table
// that lists the base allowance during a week when everyone actually has ten
// times that is worse than no table, because a member reads it, believes it,
// and reports a bug against the API.

// rankRow is one line of the table, already formatted.
type rankRow struct {
	Name     string
	Color    string
	APIBase  int64
	APIGiven int64 // after the boost; equals APIBase when none is active
	Grabs    int64
}

// registerWidgets publishes the ranks widgets. Called from Provision on legs
// that have a router. Errors are logged rather than fatal: a widget that failed
// to register is a missing card, not a reason to refuse to run ranks.
func (p *Plugin) registerWidgets(c *core.Core) {
	if err := c.RegisterWidget(core.Widget{
		Slug:        "ranks-allowances",
		Title:       "Ranks & daily allowances",
		Description: "Every rank with its daily API and grab allowance, plus any active boost.",
		// Public: it describes what the site offers rather than anything about
		// a member, and it is the card a prospective member most wants to see.
		Public: true,
		Weight: 20,
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderAllowances(gc, c)
		},
	}); err != nil {
		p.log.Info("register allowances widget failed", "err", err)
	}
}

func (p *Plugin) renderAllowances(gc *gin.Context, c *core.Core) (template.HTML, error) {
	ctx := gc.Request.Context()
	// Resolving the boost is the only thing here that needs the Core, so it is
	// done at the edge and the rendering below takes a plain value — which is
	// what makes the table testable without standing up a registry.
	return p.allowancesTable(ctx, pluginapi.LookupAPIBoost(ctx, c))
}

func (p *Plugin) allowancesTable(ctx context.Context, boost pluginapi.APIBoost) (template.HTML, error) {
	groups, err := p.store.Groups(ctx)
	if err != nil {
		// Returning the error loses the card, which the host handles by
		// dropping it — the right outcome for a card nobody can act on.
		return "", err
	}

	rows := make([]rankRow, 0, len(groups))
	for _, g := range groups {
		// Hidden groups are staff or machinery — an operator marked them
		// not-for-display and a public card must honour that.
		if !g.Visible {
			continue
		}
		api := g.Grants[entAPIDaily]
		grabs := g.Grants[entDownloadDaily]
		// A rank that confers neither quota says nothing about allowances. It
		// may well exist for a badge colour, and listing it with two dashes
		// invites the reader to wonder what they are missing.
		if api <= 0 && grabs <= 0 {
			continue
		}
		rows = append(rows, rankRow{
			Name:     g.Name,
			Color:    g.TitleColor,
			APIBase:  api,
			APIGiven: int64(boost.Apply(int(api))),
			Grabs:    grabs,
		})
	}
	if len(rows) == 0 {
		// Nothing to show is not a failure: a site whose ranks grant no quotas
		// should see no card rather than an empty box.
		return "", nil
	}
	// Cheapest rank first — the table reads as a ladder, so the order has to be
	// the ladder. Grabs before API because grabs are the figure members compare.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Grabs != rows[j].Grabs {
			return rows[i].Grabs < rows[j].Grabs
		}
		return rows[i].APIBase < rows[j].APIBase
	})

	var b strings.Builder
	b.WriteString(`<div class="ranks-allowances">`)
	if boost.Active() {
		b.WriteString(boostBanner(boost))
	}
	b.WriteString(`<table class="table table-sm mb-0"><thead><tr>` +
		`<th>Rank</th><th class="text-end">API / day</th><th class="text-end">Grabs / day</th>` +
		`</tr></thead><tbody>`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td class="text-end">%s</td><td class="text-end">%s</td></tr>`,
			rankNameHTML(r), apiCellHTML(r, boost.Active()), thousands(r.Grabs))
	}
	b.WriteString(`</tbody></table></div>`)
	return template.HTML(b.String()), nil
}

// boostBanner announces the multiplier above the table.
func boostBanner(b pluginapi.APIBoost) string {
	label := fmt.Sprintf("%s — API allowances ×%d", b.Name, b.Factor)
	if !b.Ends.IsZero() {
		// Date only: the hour a promotion lapses is not something a member can
		// act on, and printing a timestamp invites them to test it.
		label += ", until " + b.Ends.UTC().Format("2 Jan 2006")
	}
	return fmt.Sprintf(`<p class="ranks-allowances__boost"><strong>%s</strong></p>`,
		template.HTMLEscapeString(label))
}

// rankNameHTML renders the rank in its own colour when it has one. The colour
// is operator-supplied, so it is validated as a plain hex literal rather than
// interpolated into a style attribute on trust — an operator field reaching CSS
// unchecked is how a catalog row starts closing tags.
func rankNameHTML(r rankRow) string {
	name := template.HTMLEscapeString(r.Name)
	if !isHexColor(r.Color) {
		return name
	}
	return fmt.Sprintf(`<span style="color:%s">%s</span>`, r.Color, name)
}

// apiCellHTML shows the boosted figure, keeping the base visible so a member can
// see what it returns to. Without the base the card silently redefines the
// rank, and the day the window closes their allowance appears to have been cut.
func apiCellHTML(r rankRow, boosted bool) string {
	if !boosted || r.APIGiven == r.APIBase {
		return thousands(r.APIBase)
	}
	return fmt.Sprintf(`<s>%s</s> <strong>%s</strong>`, thousands(r.APIBase), thousands(r.APIGiven))
}

// isHexColor accepts #rgb and #rrggbb only.
func isHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, ch := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
			return false
		}
	}
	return true
}

// thousands groups digits so 100000 reads as 100,000. Quotas are the numbers
// this card exists to compare, and comparing them by counting zeroes is what
// makes a table of them useless.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
