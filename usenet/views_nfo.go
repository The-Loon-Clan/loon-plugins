package usenet

import (
	"context"
	"html/template"
)

// The NFO tab on /admin/p/usenet.
//
// It belongs here rather than on a host admin page for the same reason the
// Crawlers and Filters tabs do: this is the plugin's job, its config, and its
// yield behaviour, so the place that reports on it is the plugin's page. The
// host owns the catalogue the text lands in, which is why the numbers arrive
// through a capability rather than a query — but owning the storage is not the
// same as owning the feature.

type nfoRowVM struct {
	ID      int64
	Title   string
	Group   string
	Bytes   int
	Lines   int
	Preview string
}

type nfoTabVM struct {
	Enabled     bool
	BudgetMB    int
	BatchSize   int
	Stored      int
	Pending     int
	Unavailable int
	Rows        []nfoRowVM
	// Unavailable via capability: a host that registered no NFO store gets a
	// tab explaining that rather than an empty table implying zero work.
	NoStore bool
}

func (p *Plugin) renderNFO(ctx context.Context) (template.HTML, error) {
	cfg := p.effective(ctx)
	vm := nfoTabVM{
		Enabled:   cfg.NFOEnabled,
		BudgetMB:  cfg.NFOBudgetMB,
		BatchSize: cfg.NFOBatchSize,
	}
	backend, err := p.resolveNFOBackend()
	if err != nil {
		vm.NoStore = true
		return p.frag("nfo.html", vm)
	}
	// The two reads are independent and neither is worth failing the tab
	// over; the settings renderer already degrades a failing tab to a card,
	// but a missing count is better shown as zero than as a broken tab.
	if st, ok := backend.(hostNFO); ok {
		if pr, err := st.st.NFOProgress(ctx); err == nil {
			vm.Stored, vm.Pending, vm.Unavailable = pr.Stored, pr.Pending, pr.Unavailable
		}
		if rows, err := st.st.RecentNFOs(ctx, nfoTabRows); err == nil {
			for _, r := range rows {
				vm.Rows = append(vm.Rows, nfoRowVM{
					ID: r.ID, Title: r.Title, Group: r.Group,
					Bytes: r.Bytes, Lines: r.Lines, Preview: r.Preview,
				})
			}
		}
	}
	return p.frag("nfo.html", vm)
}

// nfoTabRows is how many extractions the tab lists. Enough to see the job
// working and short enough that the tab stays a tab rather than becoming a
// browsing surface — the release pages are where an NFO is actually read.
const nfoTabRows = 50
