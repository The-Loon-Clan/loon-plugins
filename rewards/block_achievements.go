package rewards

import (
	"context"
	"html/template"
	"sort"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// The achievements catalogue, published as a content block.
//
// Sites usually document achievements as a hand-written wiki table, which is
// wrong the first time somebody adds one and stays wrong because a wiki page has
// no reason to look stale. This lets the page keep its prose and defer the table:
// an editor writes `{{achievements}}` and the list is generated per render.
//
// Registered under pluginapi.ContentBlockKey("achievements"). The wiki resolves
// it without knowing this plugin exists, and this plugin does not import the wiki
// — a cross-plugin call through the registry, which is the only kind allowed.

// catalogueRow is one achievement as the public table shows it.
type catalogueRow struct {
	Name        string
	Description string
	Metric      string
	Threshold   int64
	// Pays is a human summary of the reward's payout lines ("50 points"), empty
	// when the reward cannot be resolved. The catalogue is a member-facing page,
	// so it says what you get rather than which reward id it points at.
	Pays string
}

func (p *Plugin) registerAchievementBlock(c *core.Core) error {
	return c.Register(pluginapi.ContentBlockKey("achievements"),
		pluginapi.ContentBlockFunc(func(ctx context.Context) (template.HTML, error) {
			return p.renderAchievementCatalogue(ctx)
		}))
}

func (p *Plugin) renderAchievementCatalogue(ctx context.Context) (template.HTML, error) {
	if p.store == nil || p.tmpl == nil {
		return "", nil
	}
	defs, err := p.store.ListAchievementDefs(ctx)
	if err != nil {
		return "", err
	}

	rows := make([]catalogueRow, 0, len(defs))
	for _, d := range defs {
		// Disabled achievements are not earnable, and HIDDEN ones are secret —
		// publishing either on a help page defeats the point of both. This is the
		// public catalogue, not the admin table.
		if !d.Enabled || d.Hidden {
			continue
		}
		row := catalogueRow{
			Name: d.Name, Description: d.Description,
			Metric: d.Metric, Threshold: d.Threshold,
		}
		if row.Name == "" {
			row.Name = d.Slug
		}
		// One lookup per row. An N+1, and deliberate: RewardsByTrigger("")
		// returns only rewards whose trigger is empty rather than all of them, so
		// the batch shortcut would silently blank this column for most rows —
		// which is worse than a handful of indexed reads on a page with a dozen
		// achievements. A failure just omits the prize, keeping the criteria.
		if r, err := p.store.RewardByID(ctx, d.RewardID); err == nil && r != nil {
			row.Pays = payoutSummary(r.Payouts)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Metric != rows[j].Metric {
			return rows[i].Metric < rows[j].Metric
		}
		return rows[i].Threshold < rows[j].Threshold
	})

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "block_achievements.html", rows); err != nil {
		return "", err
	}
	// Trusted by contract: this is template-escaped output, and the wiki expands
	// it AFTER sanitising precisely so the table markup survives. Every value
	// above goes through html/template, so an admin-authored name containing
	// markup is escaped rather than injected — a ContentBlock writes HTML that
	// nothing downstream will clean up.
	return template.HTML(sb.String()), nil
}

// payoutSummary renders payout lines as a phrase for a member-facing table.
func payoutSummary(ps []Payout) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		switch {
		case p.Amount > 0:
			parts = append(parts, plural(p.Amount, string(p.Kind)))
		default:
			parts = append(parts, string(p.Kind))
		}
	}
	return strings.Join(parts, " + ")
}

func plural(n int, unit string) string {
	s := strconv.Itoa(n) + " " + unit
	if n != 1 && !strings.HasSuffix(unit, "s") {
		s += "s"
	}
	return s
}
