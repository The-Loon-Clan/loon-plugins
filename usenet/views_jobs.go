package usenet

import (
	"context"
	"html/template"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The Jobs tab: one pill per job (Crawler, Backfill, Builder, Tag Fill,
// Prune, Health Check), each pane carrying the job's live status, a Run-now
// button, the log tail, and the job-specific panels that used to crowd the
// crawler dashboard (the Builder card, the NZB-health card). The dashboard
// keeps the cross-job story (providers, coverage, the live pass); this page
// answers "what is THIS job doing".

// jobPaneVM is one job's pane. Embeds the telemetry VM so the template reads
// Status/Next/Running/Logs directly.
type jobPaneVM struct {
	crawlerJobVM
	Slug   string // pane id fragment (job-pane-<slug>)
	Short  string // pill label — the name without the "Usenet " prefix
	Action string // Run-now POST path under /admin/p/usenet/
}

// jobPaneOrder fixes the pill order to the pipeline's flow, not registration
// order: crawl → backfill → build → tag → prune → health.
var jobPaneOrder = []struct{ name, slug, action string }{
	{"Usenet Crawler", "crawler", "run-crawl"},
	{"Usenet Backfill", "backfill", "run-backfill"},
	{"Usenet Builder", "builder", "run-build"},
	{"Usenet Tag Fill", "tagfill", "run-tagfill"},
	{"Usenet Prune", "prune", "run-prune"},
	{"Usenet Health Check", "health", "run-health"},
}

func (p *Plugin) renderJobs(ctx context.Context) (template.HTML, error) {
	tv := p.telemetryView(ctx)
	jobs, _ := p.jobVMs()
	if !p.runsJobs {
		// Same rule as the dashboard: on the web process the local registry
		// only holds host jobs — the worker's published snapshots are the
		// truth here.
		jobs = tv.Jobs
	}
	byName := make(map[string]crawlerJobVM, len(jobs))
	for _, j := range jobs {
		byName[j.Name] = j
	}
	panes := make([]jobPaneVM, 0, len(jobPaneOrder))
	for _, o := range jobPaneOrder {
		j, ok := byName[o.name]
		if !ok {
			// Worker hasn't published yet (or isn't running): show the pane
			// with an honest placeholder rather than dropping it.
			j = crawlerJobVM{Name: o.name, Status: "unknown", Activity: "no telemetry from the worker yet"}
		}
		panes = append(panes, jobPaneVM{
			crawlerJobVM: j, Slug: o.slug,
			Short: strings.TrimPrefix(o.name, "Usenet "), Action: o.action,
		})
	}

	// Builder pane: the dashboard's old Builder card, mode-truthful. PG mode
	// reads builderInfo (bounded install by design); redis mode reads the
	// O(1) queue depth + the telemetry sample.
	var builder BuilderInfo
	pgStaging := p.cfg.Staging != StagingRedis
	if pgStaging {
		var err error
		if builder, err = p.st.builderInfo(ctx, 15); err != nil {
			return "", err
		}
	}
	var ready int64
	if !pgStaging {
		if si, err := p.staging.stagingInfo(ctx); err == nil {
			ready = si.ReadyGroups
		}
	}

	// Health pane: the dashboard's old NZB-health card (host-cache-backed in
	// sink=host mode).
	var cs *pluginapi.CatalogStats
	if p.cfg.Sink == SinkHost {
		if prov, ok := pluginapi.LookupCatalogStats(p.core); ok {
			if v, err := prov.CatalogStats(ctx); err == nil {
				cs = &v
			} else {
				p.core.Errors.Report(ctx, "usenet/catalog-stats", err)
			}
		}
	}

	return p.frag("jobs.html", map[string]any{
		"Jobs":    panes,
		"Builder": builder, "PGStaging": pgStaging,
		"Pending": tv.Pending, "Evicted": tv.Evicted, "ReadyGroups": ready,
		"Health":   p.healthVM(ctx, cs),
		"Pass":     statsVM(pickPass(tv.CrawlCur, tv.CrawlLast)),
		"Backfill": statsVM(pickPass(tv.BackfillCur, tv.BackfillLast)),
	})
}
