package offers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// AdminOffersPage is the read-only observability surface at
// /admin/offers — recent requests, status histogram, leaderboard,
// tracker stats. Lets admins see stuck claims and per-user activity
// without poking the DB by hand. Mutations live elsewhere; this page
// only renders state.
func (h *Handlers) AdminOffersPage(c *gin.Context) {
	ctx := c.Request.Context()
	recent, err := deps.AdminRequests(ctx, 100)
	if err != nil {
		deps.LogError(ctx, "admin/offers-recent", err)
	}
	counts, err := deps.AdminStatusCounts(ctx)
	if err != nil {
		deps.LogError(ctx, "admin/offers-counts", err)
	}
	leaders, err := deps.Leaderboard(ctx, 25)
	if err != nil {
		deps.LogError(ctx, "admin/offers-leaderboard", err)
	}
	trackers, err := deps.TrackerStats(ctx)
	if err != nil {
		deps.LogError(ctx, "admin/offers-trackers", err)
	}
	page(c, "Offers — admin", "admin_offers.html", gin.H{
		"PageTitle":    "Offers",
		"ActiveNav":    "admin",
		"Recent":       recent,
		"StatusCounts": counts,
		"Leaders":      leaders,
		"Trackers":     trackers,
	})
}

func (h *Handlers) AdminTrackersPage(c *gin.Context) {
	ctx := c.Request.Context()
	trackers, err := deps.ListTrackers(ctx, true) // include banned
	if err != nil {
		deps.LogError(ctx, "admin/trackers-list", err)
	}
	page(c, "Trackers", "admin_trackers.html", gin.H{
		"PageTitle": "Private Trackers",
		"ActiveNav": "admin",
		"Trackers":  trackers,
		"Saved":     c.Query("saved") == "1",
		"Error":     c.Query("error"),
	})
}

func (h *Handlers) AdminTrackerSave(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil || !user.IsMod {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	ctx := c.Request.Context()
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	name := strings.TrimSpace(c.PostForm("name"))
	shortName := strings.TrimSpace(c.PostForm("short_name"))
	if name == "" || shortName == "" {
		c.Redirect(http.StatusFound, "/admin/trackers?error=Name+and+short+name+are+required")
		return
	}
	visibility := c.PostForm("visibility")
	switch visibility {
	case VisibilityPrivate, VisibilityPublic, VisibilityPersonal:
		// ok
	default:
		visibility = VisibilityPrivate
	}
	status := c.PostForm("status")
	switch status {
	case StatusUnvetted, StatusActive, StatusBanned:
		// ok
	default:
		status = StatusUnvetted
	}
	scrapeMin, _ := strconv.Atoi(c.PostForm("scrape_min_seconds"))
	if scrapeMin <= 0 {
		scrapeMin = 180
	}

	t := &TrackerInput{
		ID:               int(id),
		Name:             name,
		ShortName:        shortName,
		Visibility:       visibility,
		Status:           status,
		RulesMarkdown:    c.PostForm("rules_md"),
		ScrapeMinSeconds: scrapeMin,
	}
	op := "admin/tracker-update"
	if id == 0 {
		op = "admin/tracker-create"
	}
	if err := deps.SaveTracker(ctx, *t); err != nil {
		deps.ReportError(c, op, err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/trackers?saved=1")
}

func (h *Handlers) AdminTrackerDelete(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil || !user.IsAdmin {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, "bad id")
		return
	}
	if err := deps.DeleteTracker(c.Request.Context(), id); err != nil {
		deps.ReportError(c, "admin/tracker-delete", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/trackers?saved=1")
}
