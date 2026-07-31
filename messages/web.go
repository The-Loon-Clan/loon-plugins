package messages

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The handful of host helpers this surface used, lifted so the plugin depends
// on no site package.
//
// They are copies rather than a shared import on purpose: they are six lines
// each, and a portable plugin that pulls in a web-helpers package to get
// `{"ok": true}` has traded a real dependency for a trivial saving. The JSON
// shapes match the origin site byte for byte, because the same JavaScript on
// the inbox page reads them.

// jsonOK is `{"ok": true, ...extras}` — extras merge at the top level, which
// is what the existing inbox JS reads (`data.unread`, `data.thread_id`).
func jsonOK(c *gin.Context, extras gin.H) {
	out := gin.H{"ok": true}
	for k, v := range extras {
		out[k] = v
	}
	c.JSON(http.StatusOK, out)
}

// jsonError returns a message the client may see: validation, or a
// business rule like "that user has blocked you".
func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"ok": false, "error": msg})
}

// jsonInternalError logs the real error under a stable op label and tells the
// client nothing beyond "internal server error".
//
// Never hand err.Error() to a client: on this path it would leak SQL and
// schema detail to anyone who can post a malformed DM.
func jsonInternalError(c *gin.Context, errs core.ErrorReporter, op string, err error) {
	if errs != nil {
		errs.Report(c.Request.Context(), op, err)
	}
	c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "internal server error"})
}

// pageOffset converts a 1-based page into a SQL offset, clamping a missing or
// hostile page number to the first page.
func pageOffset(page, pageSize int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * pageSize
}

// Pagination is the shape the host's shared pagination partial expects.
//
// Lifted field-for-field, including the ellipsis sentinel and MidPage, because
// the TEMPLATE is the host's: a portable plugin that invented its own shape
// here would render nothing through the partial it is handed.
type Pagination struct {
	Page       int
	TotalPages int
	BaseURL    string // everything except the page param, ending in "?" or "&"
	Pages      []int  // page numbers to display; 0 = ellipsis sentinel
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
	MidPage    int    // halfway point, for the quick jump
	ParamName  string // page-number query key; "page" when empty
}

// newPagination builds the template struct: first, last, midpoint, and a
// window of ±2 around the current page, with 0 marking a gap.
func newPagination(page, totalPages int, baseURL string) Pagination {
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	mid := totalPages / 2
	if mid < 1 {
		mid = 1
	}
	pageSet := map[int]bool{1: true, totalPages: true}
	if totalPages > 10 {
		pageSet[mid] = true
	}
	for p := page - 2; p <= page+2; p++ {
		if p >= 1 && p <= totalPages {
			pageSet[p] = true
		}
	}

	var pages []int
	prev := 0
	for p := 1; p <= totalPages; p++ {
		if pageSet[p] {
			if prev > 0 && p-prev > 1 {
				pages = append(pages, 0) // ellipsis
			}
			pages = append(pages, p)
			prev = p
		}
	}
	return Pagination{
		Page: page, TotalPages: totalPages, BaseURL: baseURL, Pages: pages,
		HasPrev: page > 1, HasNext: page < totalPages,
		PrevPage: page - 1, NextPage: page + 1, MidPage: mid,
	}
}
