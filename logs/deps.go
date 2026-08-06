// Package logs is the error-log search + retention plugin. It serves
// /admin/logs — an Elasticsearch-style search over the host's error-log sink
// (query DSL, op/severity facets, a last_at histogram, and a live-tail
// refresh) — and owns the daily Error Log Cleanup job.
//
// The error-log TABLE stays host-owned: every internal-error and
// service-error call across the site writes to it, and the host reads it on
// its own admin pages. This plugin is a READER over that sink plus the prune
// loop; it owns no schema and runs no migrations.
package logs

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// Search is the parsed query. It is the plugin's type rather than the host's
// because ParseQuery is what produces it — the DSL is this plugin's whole
// reason to exist, and a filter it cannot name is a filter it cannot offer.
// The host translates at the seam.
type Search struct {
	// Terms AND-match as case-insensitive substrings of message.
	Terms []string
	// NotTerms exclude rows whose message contains the substring.
	NotTerms []string
	// Ops OR-match against op. A trailing '*' makes it a prefix
	// match ("usenet/*"); otherwise exact.
	Ops []string
	// NotOps exclude rows whose op matches (same exact/prefix rule as
	// Ops). AND-combined: a row is dropped if it matches ANY NotOp.
	NotOps []string
	// Severities OR-match (warning | error | fatal). Empty = all.
	Severities []string
	// NotSeverities exclude rows of these severities (AND-combined).
	NotSeverities []string
	// UserID filters to one user's rows when non-nil.
	UserID *int
	// Paths AND-match as case-insensitive substrings of request_path.
	Paths []string
	// NotPaths exclude rows whose request_path contains the substring.
	// A NULL request_path is kept (it contains nothing to exclude).
	NotPaths []string
	// From / To bound last_at (half-open: last_at >= From, < To).
	From *time.Time
	To   *time.Time
	// IncludeArchived widens the search to dismissed rows.
	IncludeArchived bool
	// MinCount keeps only rows with count >= MinCount (find flappers).
	MinCount int
	// Sort: "recent" (last_at DESC, default), "count" (count DESC),
	// "first" (first_at DESC). Anything else falls back to recent.
	Sort string

	Limit  int
	Offset int
}

// Row is one error-log entry as the page and the JSON API see it.
//
// Deliberately flat and snake_case-tagged: the host's record carries db tags
// only, so marshalling it directly would emit PascalCase keys (breaking the
// live-tail JS) and leave pointers that Go templates cannot print. Absent
// values arrive as the zero value, which both renderers handle.
type Row struct {
	ID          int64     `json:"id"`
	Severity    string    `json:"severity"`
	Op          string    `json:"op"`
	Message     string    `json:"message"`
	RequestPath string    `json:"request_path"`
	UserID      int       `json:"user_id"`
	Count       int       `json:"count"`
	LastAt      time.Time `json:"last_at"`
}

// Facet is one bar of the op / severity rail.
type Facet struct {
	Key   string `json:"key"`
	Rows  int    `json:"rows"`
	Count int64  `json:"count"`
}

// Bucket is one column of the last_at histogram.
type Bucket struct {
	Bucket time.Time `json:"bucket"`
	Rows   int       `json:"rows"`
	Count  int64     `json:"count"`
}

// Deps is the web/search side, set from the composition root before core.Boot.
//
// The four data functions are the whole read surface. They are function-typed
// because each translates the plugin's query and row types to the host's at
// the seam; the host's error-log repository is much wider than this (it also
// writes, and serves the host's own pages), and the plugin has no business
// holding the write half.
type Deps struct {
	// BaseData merges the host's page chrome into a template data map.
	BaseData func(c *gin.Context, extra gin.H) gin.H
	// Pagination is the host's paging helper. Typed `any` across the seam:
	// the value is consumed by the host's pagination partial, so the plugin
	// hands it straight to the template and never reads it.
	Pagination func(page, pageSize, totalItems int, baseURL string) any

	// The host's JSON response helpers. Passed rather than reimplemented
	// because JSONInternalError also records to the very sink this plugin
	// displays — a search failure should show up in the search.
	JSONOK            func(c *gin.Context, extras gin.H)
	JSONError         func(c *gin.Context, code int, msg string)
	JSONInternalError func(c *gin.Context, op string, err error)

	Search    func(ctx context.Context, q Search) (rows []Row, total int, err error)
	Facets    func(ctx context.Context, q Search, topN int) (ops, severities []Facet, err error)
	Histogram func(ctx context.Context, q Search, bucket string) ([]Bucket, error)
	Archive   func(ctx context.Context, id int64) error
}

// JobDeps is the worker side — the daily prune loop.
type JobDeps struct {
	// Prune deletes rows older than olderThan and reports how many went.
	Prune func(ctx context.Context, olderThan time.Duration) (int64, error)
	// ReportError records a prune failure to the host's error sink. The job
	// also surfaces it via SetError; this is the durable half.
	ReportError func(ctx context.Context, op string, err error)
}

var (
	deps    *Deps
	jobDeps *JobDeps
)

// SetDeps hands the plugin its web-side dependencies.
func SetDeps(d Deps) { deps = &d }

// SetJobDeps hands the plugin its worker-side dependencies.
func SetJobDeps(d JobDeps) { jobDeps = &d }

// ok reports whether every web-side dependency was supplied.
func (d *Deps) ok() bool {
	return d != nil && d.BaseData != nil && d.Pagination != nil &&
		d.JSONOK != nil && d.JSONError != nil && d.JSONInternalError != nil &&
		d.Search != nil && d.Facets != nil && d.Histogram != nil && d.Archive != nil
}

// ok reports whether every worker-side dependency was supplied.
func (d *JobDeps) ok() bool {
	return d != nil && d.Prune != nil && d.ReportError != nil
}
