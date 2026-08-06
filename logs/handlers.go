package logs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const logsPageSize = 50

// Handlers serves /admin/logs. It reads the host's error-log sink through the
// four search functions on Deps.
type Handlers struct{}

// build parses the request into a search + the derived page/offset and
// the histogram bucket. Shared by the page and the JSON API so both
// interpret the query box identically.
func (h *Handlers) build(c *gin.Context) (Search, int, string) {
	q := ParseQuery(c.Query("q"), time.Now().UTC())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	q.Limit = logsPageSize
	q.Offset = (page - 1) * logsPageSize

	// Bucket the histogram by hour for short windows, day otherwise,
	// so the chart stays readable at both zoom levels. Explicit
	// ?bucket= overrides.
	bucket := c.Query("bucket")
	if bucket != "hour" && bucket != "day" {
		bucket = "day"
		if q.From != nil && time.Since(*q.From) <= 72*time.Hour {
			bucket = "hour"
		}
	}
	return q, page, bucket
}

// searchResult is the JSON payload for the live-tail / async refresh.
type searchResult struct {
	Rows       []Row    `json:"rows"`
	Total      int      `json:"total"`
	Page       int      `json:"page"`
	TotalPages int      `json:"total_pages"`
	Ops        []Facet  `json:"ops"`
	Severities []Facet  `json:"severities"`
	Histogram  []Bucket `json:"histogram"`
}

// run executes the search + facets + histogram and returns the result
// alongside the histogram bucket it used (so the page render doesn't
// re-parse the query just to recover the bucket).
func (h *Handlers) run(c *gin.Context) (*searchResult, string, error) {
	q, page, bucket := h.build(c)
	ctx := c.Request.Context()
	rows, total, err := deps.Search(ctx, q)
	if err != nil {
		return nil, bucket, err
	}
	ops, sevs, err := deps.Facets(ctx, q, 12)
	if err != nil {
		return nil, bucket, err
	}
	hist, err := deps.Histogram(ctx, q, bucket)
	if err != nil {
		return nil, bucket, err
	}
	totalPages := (total + logsPageSize - 1) / logsPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	return &searchResult{rows, total, page, totalPages, ops, sevs, hist}, bucket, nil
}

// histoBar is one server-rendered histogram column: a label, the raw
// occurrence count, and a 0-100 height percent (Go templates can't do
// the division).
type histoBar struct {
	Label string
	Count int64
	Pct   int
}

func buildBars(h []Bucket, bucket string) []histoBar {
	max := histoMax(h)
	layout := "Jan 2"
	if bucket == "hour" {
		layout = "Jan 2 15h"
	}
	bars := make([]histoBar, len(h))
	for i, b := range h {
		pct := 0
		if max > 0 {
			pct = int(b.Count * 100 / max)
			if pct < 3 {
				pct = 3 // keep a non-empty bar visible
			}
		}
		bars[i] = histoBar{Label: b.Bucket.Format(layout), Count: b.Count, Pct: pct}
	}
	return bars
}

// LogsSearch is the JSON endpoint backing live-tail auto-refresh and
// async pagination — same query semantics as the page, no HTML.
func (h *Handlers) LogsSearch(c *gin.Context) {
	res, _, err := h.run(c)
	if err != nil {
		deps.JSONInternalError(c, "logs/search", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// ArchiveLog dismisses one row — the search-result row-level button
// POSTs here and removes the row client-side.
func (h *Handlers) ArchiveLog(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := deps.Archive(c.Request.Context(), id); err != nil {
		deps.JSONInternalError(c, "logs/archive", err)
		return
	}
	deps.JSONOK(c, nil)
}

// histoMax returns the largest bucket occurrence-count, for scaling
// the server-rendered bar chart. Zero-safe (the template guards /0).
func histoMax(h []Bucket) int64 {
	var max int64
	for _, b := range h {
		if b.Count > max {
			max = b.Count
		}
	}
	return max
}
