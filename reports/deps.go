// Package reports is the member-report queue: the admin surface over releases
// members have flagged as broken, mislabeled, or malware.
//
// The reports TABLE stays host-owned, and must — it is written from the
// release page and the API when a member files a report, and the host's daily
// digest reads it to raise "N unresolved malware reports". This plugin owns
// the triage SURFACE over it.
package reports

import (
	"context"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"
)

// Report is one member report as the queue displays it.
//
// Reason is a free string rather than a typed enum on purpose. The plugin
// renders whatever the host stores and colours the three it knows; a site with
// different reasons gets them listed rather than dropped, which is the
// behaviour a shared plugin wants.
type Report struct {
	ID        int64
	NzbID     int64
	NzbTitle  string
	Username  string
	Reason    string
	Detail    string
	CreatedAt time.Time
}

// Deps carries what the plugin cannot do for itself.
type Deps struct {
	// RenderPagination returns the site's pager as ready HTML. The page is a
	// slot fragment rendered by this plugin's own template set, which cannot
	// reach the host's partials — so the host renders its own and this drops
	// the result in, rather than a second copy of shared chrome living here.
	RenderPagination func(page, pageSize, totalItems int, baseURL string) template.HTML

	// List returns one page of reports plus the unpaged total. resolved
	// selects which side of the queue: open work, or the audit trail.
	List func(ctx context.Context, resolved bool, limit, offset int) ([]Report, int, error)

	// Resolve marks one report handled, attributing it to the acting admin so
	// the audit trail says who cleared it.
	Resolve func(ctx context.Context, reportID int64, adminID int) error

	// ActingAdmin identifies who is clearing a report. The host owns the
	// session, and the attribution is the point — a queue that records only
	// that something was resolved is not an audit trail.
	ActingAdmin func(c *gin.Context) int
}

var deps *Deps

// SetDeps hands the plugin its host adapters, before core.Boot.
func SetDeps(d Deps) { deps = &d }

func (d *Deps) ok() bool {
	return d != nil && d.RenderPagination != nil && d.List != nil &&
		d.Resolve != nil && d.ActingAdmin != nil
}
