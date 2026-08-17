package achievements

import (
	"context"
	"html/template"
	"sort"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// The achievements catalogue, published as a content block.
//
// Sites usually document achievements as a hand-written wiki table, which is
// wrong the first time somebody adds one and stays wrong because a wiki page
// has no reason to look stale. This lets the page keep its prose and defer
// the table: an editor writes `{{achievements}}` and the list is generated
// per render.
//
// Registered under pluginapi.ContentBlockKey("achievements"). The wiki
// resolves it without knowing this plugin exists, and this plugin does not
// import the wiki — a cross-plugin call through the registry, which is the
// only kind allowed.

// catalogueRow is one achievement as the public table shows it.
type catalogueRow struct {
	Name        string
	Description string
	// Metric+Threshold for a counted achievement; Trigger for an event one.
	Metric    string
	Threshold int64
	Trigger   string
	// Pays is the named reward's slug, empty for a pure badge. The old table
	// rendered the reward's payout lines as a phrase ("50 points"); that
	// required reading the rewards tables, which this plugin no longer does,
	// so the slug is what there honestly is to show.
	Pays string
}

func (p *Plugin) registerBlock(c *core.Core) error {
	return c.Register(pluginapi.ContentBlockKey("achievements"),
		pluginapi.ContentBlockFunc(func(ctx context.Context) (template.HTML, error) {
			return p.renderCatalogue(ctx)
		}))
}

func (p *Plugin) renderCatalogue(ctx context.Context) (template.HTML, error) {
	if p.store == nil || p.tmpl == nil {
		return "", nil
	}
	defs, err := p.store.ListAchievementDefs(ctx)
	if err != nil {
		return "", err
	}

	rows := make([]catalogueRow, 0, len(defs))
	for _, d := range defs {
		// Disabled achievements are not earnable, and HIDDEN ones are secret
		// — publishing either on a help page defeats the point of both. This
		// is the public catalogue, not the admin table.
		if !d.Enabled || d.Hidden {
			continue
		}
		row := catalogueRow{
			Name: d.Name, Description: d.Description,
			Metric: d.Metric, Threshold: d.Threshold, Trigger: d.Trigger,
			Pays: d.RewardSlug,
		}
		if row.Name == "" {
			row.Name = d.Slug
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
	// Trusted by contract: this is template-escaped output, and the wiki
	// expands it AFTER sanitising precisely so the table markup survives.
	// Every value above goes through html/template, so an admin-authored
	// name containing markup is escaped rather than injected — a
	// ContentBlock writes HTML that nothing downstream will clean up.
	return template.HTML(sb.String()), nil
}
