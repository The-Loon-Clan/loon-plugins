// Package requests is the community request board — filing,
// voting, points boosts, torrent scraping (nyaa / nekobt / tosho),
// Prowlarr search, and the fulfil/retry/unpark lifecycle.
//
// Lifted from the ameNZB host's in-repo plugin. The storage stays
// host-owned: the request tables are shared with the site's agent
// fulfilment pipeline (agents pick work off the request queue, and
// the upload flows mark requests fulfilled), so every store crosses
// as an adapted interface and every piece of site vocabulary as a
// function — see deps.go.
package requests

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/httpclient"
)

const requestsPageSize = 30

// Handlers serves the request board. Core seams: Points (boost spending)
// and Errors (500s); everything else comes through Deps.
type Handlers struct {
	deps   Deps
	points core.PointsService
	errs   core.ErrorReporter

	// torCache holds SearchTorrents answers for a day (operator's call,
	// 2026-08-25). The same missing episode gets clicked by every member
	// who wants it, and each click was one more identical question to the
	// upstream tracker; a swarm does not change fast enough to re-ask for
	// 24 hours. Errors are never cached (an upstream flake must not stick
	// for a day) and refresh=1 — the manual search button — bypasses and
	// rewrites the entry.
	torCacheMu sync.Mutex
	torCache   map[string]torCacheEntry
}

type torCacheEntry struct {
	at   time.Time
	body []byte
}

const torCacheTTL = 24 * time.Hour
const torCacheMax = 512

// torCacheGet returns the still-fresh cached body for key, if any.
func (h *Handlers) torCacheGet(key string) []byte {
	h.torCacheMu.Lock()
	defer h.torCacheMu.Unlock()
	e, ok := h.torCache[key]
	if !ok || time.Since(e.at) > torCacheTTL {
		return nil
	}
	return e.body
}

// torCachePut stores body under key, evicting expired entries — and, past
// the cap, the oldest — so an abusive query stream cannot grow the map
// without bound.
func (h *Handlers) torCachePut(key string, body []byte) {
	h.torCacheMu.Lock()
	defer h.torCacheMu.Unlock()
	if h.torCache == nil {
		h.torCache = map[string]torCacheEntry{}
	}
	if len(h.torCache) >= torCacheMax {
		var oldestKey string
		var oldestAt time.Time
		for k, e := range h.torCache {
			if time.Since(e.at) > torCacheTTL {
				delete(h.torCache, k)
				continue
			}
			if oldestKey == "" || e.at.Before(oldestAt) {
				oldestKey, oldestAt = k, e.at
			}
		}
		if len(h.torCache) >= torCacheMax && oldestKey != "" {
			delete(h.torCache, oldestKey)
		}
	}
	h.torCache[key] = torCacheEntry{at: time.Now(), body: body}
}

// viewerID returns the session user's id, 0 for anonymous.
func (h *Handlers) viewerID(c *gin.Context) int {
	if v := h.deps.Viewer(c); v != nil {
		return v.ID
	}
	return 0
}

// BoostCostBreakdown describes all multipliers that make up the boost cost.
type BoostCostBreakdown struct {
	BaseCost  int    // admin-configured base
	SeedMult  int    // seed multiplier percentage (0 = no penalty)
	SeedLabel string // e.g. "3 seeders"
	SizeMult  int    // size multiplier percentage (0 = no penalty)
	SizeLabel string // e.g. "100 GB"
	FinalCost int    // total cost per boost
}

// boostCostBreakdown computes the full cost breakdown for a boost.
func boostCostBreakdown(baseCost int, seedCount int, sizeBytes int64, perGB int) BoostCostBreakdown {
	b := BoostCostBreakdown{BaseCost: baseCost}
	cost := baseCost

	// Seed penalty.
	switch {
	case seedCount <= 0:
		b.SeedMult = 300
		b.SeedLabel = "no seeders"
	case seedCount <= 2:
		b.SeedMult = 200
		b.SeedLabel = fmt.Sprintf("%d seeder%s", seedCount, map[bool]string{true: "", false: "s"}[seedCount == 1])
	case seedCount <= 5:
		b.SeedMult = 100
		b.SeedLabel = fmt.Sprintf("%d seeders", seedCount)
	default:
		b.SeedMult = 0
		b.SeedLabel = fmt.Sprintf("%d seeders", seedCount)
	}
	cost += baseCost * b.SeedMult / 100

	// Size penalty (per-GB surcharge).
	if perGB > 0 && sizeBytes > 0 {
		gb := int(sizeBytes / (1024 * 1024 * 1024))
		if gb < 1 {
			gb = 1
		}
		sizeCost := perGB * gb
		if baseCost > 0 {
			b.SizeMult = sizeCost * 100 / baseCost
		}
		b.SizeLabel = fmt.Sprintf("%d GB", gb)
		cost += sizeCost
	}

	if cost < 1 {
		cost = 1
	}
	b.FinalCost = cost
	return b
}

// boostCostForRequest returns the boost cost adjusted by seeder count (legacy wrapper).
func boostCostForRequest(baseCost int, seedCount int) int {
	return boostCostBreakdown(baseCost, seedCount, 0, 0).FinalCost
}

// attachRequestPriorities batch-loads priorities and attaches them to each request.
func (h *Handlers) attachRequestPriorities(ctx context.Context, requests []*Request) {
	if len(requests) == 0 {
		return
	}
	ids := make([]int64, len(requests))
	for i, r := range requests {
		ids[i] = r.ID
	}
	pmap, err := h.deps.Requests.GetRequestPrioritiesBatch(ctx, ids)
	if err != nil || pmap == nil {
		return
	}
	for _, r := range requests {
		r.Priorities = pmap[r.ID]
	}
}

// backlogTotal is the tab badge: how many requests are shelved right now.
//
// Best-effort on purpose. It renders on every tab, so a failure here would
// take down the open queue to hide a number — the badge simply does not
// appear, which reads as "nothing shelved" rather than as a broken page.
func (h *Handlers) backlogTotal(ctx context.Context) int {
	counts, err := h.deps.Requests.BacklogCounts(ctx)
	if err != nil {
		h.errs.Report(ctx, "requests/backlog-counts", err)
		return 0
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// tabCounts is the badge on each open-queue tab, in template-friendly form.
//
// Best-effort for the same reason backlogTotal is: these render on every tab,
// so a failed count must cost a badge, not the listing under it. The map is
// keyed by the ?tab= word rather than the scope, because the template renders
// one link per tab and looking a badge up by the same word it links to is one
// less thing to keep in step.
//
// The three numbers do NOT sum to the open queue and are not shown as if they
// did — needs-sourcing is a lens over the other two plus the backlog. See
// ScopeNeedsSourcing.
func (h *Handlers) tabCounts(ctx context.Context) map[string]int {
	counts, err := h.deps.Requests.OpenRequestCounts(ctx)
	if err != nil {
		h.errs.Report(ctx, "requests/open-counts", err)
		return map[string]int{}
	}
	out := make(map[string]int, len(tabScopes))
	for tab, scope := range tabScopes {
		out[tab] = counts[scope]
	}
	return out
}

// listPageChrome is every key the page's shared furniture reads — the tab
// strip, the create form, the per-row gates — regardless of which tab is
// showing.
//
// It exists because the alternative kept failing in one direction: the create
// form lives ABOVE the tab switch and renders on every branch, so a branch
// that forgot "Prefill" or "PriorityTypes" produced a page that looked fine
// and had dead JavaScript. The feed tab shipped that way once and the backlog
// tab nearly did. A map-driven template answers a missing key with silence,
// so the only real fix is to stop having per-branch key lists.
func (h *Handlers) listPageChrome(ctx context.Context, c *gin.Context, tab string, upscaleOpts []UpscaleOption) gin.H {
	return gin.H{
		"Tab":            tab,
		"PriorityTypes":  h.deps.PriorityTypes(),
		"UpscaleOptions": upscaleOpts,
		"Prefill": map[string]string{
			"title": "", "anime_id": "", "season": "",
			"episodes": "", "resolution": "", "source": "",
		},
		"OpenForm":     false,
		"AllowedHosts": allowedRequestHosts,
		"ViewerID":     h.viewerID(c),
		"BacklogTotal": h.backlogTotal(ctx),
		"TabCounts":    h.tabCounts(ctx),
	}
}

func (h *Handlers) RequestsPage(c *gin.Context) {
	ctx := c.Request.Context()

	// AI upscale model dropdown options — union of advertised models
	// across every opted-in agent. Best-effort: rendering without the
	// dropdown is preferable to a 500 if the query hiccups. Empty slice
	// means "no capable agent online" and the template hides the
	// dropdown entirely.
	upscaleKeys, _ := h.deps.AgentTokens.ListAvailableUpscaleModels(ctx)
	upscaleOpts := h.deps.UpscaleOptions(upscaleKeys)

	// If filtering by anime, show that anime's requests instead. The
	// "Open a request" link from /community/broken's alternatives panel
	// also lands here (it sets anime_id in the query) — same prefill keys
	// thread through so the form is pre-filled either way.
	if aidStr := c.Query("anime_id"); aidStr != "" {
		aid, err := strconv.Atoi(aidStr)
		if err == nil && aid > 0 {
			requests, _ := h.deps.Requests.GetRequestsForAnime(ctx, aid)
			anime, _ := h.deps.Anime.GetAnimeMetadata(ctx, aid)
			h.attachRequestPriorities(ctx, requests)
			pg := h.deps.RenderPagination(1, 1, fmt.Sprintf("/community/requests?anime_id=%d&", aid))
			prefill := map[string]string{
				"title":      c.Query("title"),
				"anime_id":   aidStr,
				"season":     c.Query("season"),
				"episodes":   c.Query("episodes"),
				"resolution": c.Query("resolution"),
				"source":     c.Query("source"),
			}
			openForm := c.Query("open") == "1"
			// Deliberately NOT origin-split. "Who else wants this anime" is
			// a different question from "what did members ask for": if the
			// importer already filed this episode, that is the single most
			// useful thing to show somebody about to request it again.
			data := h.listPageChrome(ctx, c, "open", upscaleOpts)
			data["Requests"] = requests
			data["Total"] = len(requests)
			data["Page"] = 1
			data["TotalPages"] = 1
			data["Anime"] = anime
			data["AnimeID"] = aid
			data["Pagination"] = pg
			data["Prefill"] = prefill
			data["OpenForm"] = openForm
			h.render(c, http.StatusOK, "Community Requests", "community_requests.html", data)
			return
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := pageOffset(page, requestsPageSize)

	tab := c.DefaultQuery("tab", "open")

	// Feed tab: lists every torrent the bot's seen across nyaa /
	// tokyotosho / nekobt, including ones it skipped. Lets users
	// spot-request something the bot's heuristics rejected. The form +
	// vote controls don't apply here — template gates on Tab.
	if tab == "feed" {
		source := strings.TrimSpace(c.Query("source"))
		statusFilter := strings.TrimSpace(c.Query("status"))
		q := strings.TrimSpace(c.Query("q"))
		items, total, err := h.deps.FeedItems.ListFeedItems(ctx, FeedItemFilter{
			Source: source,
			Status: statusFilter,
			Q:      q,
			Limit:  requestsPageSize,
			Offset: offset,
		})
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to load feed items")
			return
		}
		totalPages := (total + requestsPageSize - 1) / requestsPageSize
		if totalPages < 1 {
			totalPages = 1
		}
		// Carry filters through pagination links so page 2 keeps the
		// active source/status/q filters.
		baseURL := "/community/requests?tab=feed"
		if source != "" {
			baseURL += "&source=" + url.QueryEscape(source)
		}
		if statusFilter != "" {
			baseURL += "&status=" + url.QueryEscape(statusFilter)
		}
		if q != "" {
			baseURL += "&q=" + url.QueryEscape(q)
		}
		baseURL += "&"
		pg := h.deps.RenderPagination(page, totalPages, baseURL)
		// listPageChrome carries PriorityTypes + Prefill, which the New
		// Request form reads — it lives ABOVE the tab switch and renders on
		// every tab, and this branch is where forgetting them was first
		// found: the template's {{index .Prefill "title"}} silently yielded
		// nothing and took the page's JS down with it.
		data := h.listPageChrome(ctx, c, "feed", upscaleOpts)
		data["FeedItems"] = items
		data["Total"] = total
		data["Page"] = page
		data["TotalPages"] = totalPages
		data["Pagination"] = pg
		data["FeedSource"] = source
		data["FeedStatus"] = statusFilter
		data["Query"] = q
		h.render(c, http.StatusOK, "Community Requests", "community_requests.html", data)
		return
	}

	// Backlog tab: requests that stopped moving and left the open queue.
	//
	// They are still open requests — nothing was deleted and nothing was
	// refused. What changed is that the open queue stopped describing them
	// honestly: 7,151 of 13,912 were over thirty days old, sitting in the
	// same list as this morning's, so a member could not tell a live
	// request from one that stalled in April.
	//
	// Anyone signed in can pull one back, because the person who knows
	// whether a two-month-old request still matters is the person who wants
	// it, not us. The row records who asked and how many times.
	if tab == "backlog" {
		requests, total, err := h.deps.Requests.GetBacklogRequests(ctx, requestsPageSize, offset)
		if err != nil {
			h.errs.Report(ctx, "requests/backlog-list", err)
			c.String(http.StatusInternalServerError, "failed to load the backlog")
			return
		}
		totalPages := (total + requestsPageSize - 1) / requestsPageSize
		if totalPages < 1 {
			totalPages = 1
		}
		h.attachRequestPriorities(ctx, requests)
		counts, err := h.deps.Requests.BacklogCounts(ctx)
		if err != nil {
			// The listing already rendered above; a missing breakdown drops
			// the summary strip, not the page.
			h.errs.Report(ctx, "requests/backlog-counts", err)
			counts = map[string]int{}
		}
		// Reasons in display order, dropping the ones nothing landed in —
		// a strip of three zeroes says less than a strip of one number.
		var summary []backlogSummaryRow
		for _, r := range BacklogReasons {
			if n := counts[r.Slug]; n > 0 {
				summary = append(summary, backlogSummaryRow{BacklogReason: r, Count: n})
			}
		}
		// ?requeued=<id> / ?requeue=already drive the post-action banner.
		requeuedID, _ := strconv.ParseInt(c.Query("requeued"), 10, 64)
		data := h.listPageChrome(ctx, c, "backlog", upscaleOpts)
		data["Requests"] = requests
		data["Total"] = total
		data["Page"] = page
		data["TotalPages"] = totalPages
		data["Pagination"] = h.deps.RenderPagination(page, totalPages, "/community/requests?tab=backlog&")
		// The badge on this tab counts what this tab LISTS — every origin,
		// because the backlog holds every origin. It is not scoped to
		// members the way the queue tabs are: a badge that disagreed with
		// the list under it would be the exact failure this change is
		// about, in a new place.
		data["BacklogTotal"] = total
		data["BacklogSummary"] = summary
		data["RequeuedID"] = requeuedID
		data["RequeueState"] = c.Query("requeue")
		h.render(c, http.StatusOK, "Community Requests", "community_requests.html", data)
		return
	}

	// Needs-sourcing tab: a member asked for something and there is nothing
	// to click.
	//
	// This is the operator's job, and it had no home. The queue could not
	// show it (6,413 of 6,420 open rows were the importer's), the backlog
	// sweep shelved these along with everything else that stopped moving,
	// and no counter anywhere named them. On the day it was built production
	// held nine — two with no link of any kind, both already swept off the
	// board months earlier.
	//
	// Oldest first and shelved rows included; see ScopeNeedsSourcing.
	if tab == "sourcing" {
		requests, total, err := h.deps.Requests.ListOpenRequests(ctx, ScopeNeedsSourcing, requestsPageSize, offset)
		if err != nil {
			h.errs.Report(ctx, "requests/needs-sourcing", err)
			c.String(http.StatusInternalServerError, "failed to load the sourcing queue")
			return
		}
		totalPages := (total + requestsPageSize - 1) / requestsPageSize
		if totalPages < 1 {
			totalPages = 1
		}
		h.attachRequestPriorities(ctx, requests)
		data := h.listPageChrome(ctx, c, "sourcing", upscaleOpts)
		data["Requests"] = requests
		data["Total"] = total
		data["Page"] = page
		data["TotalPages"] = totalPages
		data["Pagination"] = h.deps.RenderPagination(page, totalPages, "/community/requests?tab=sourcing&")
		h.render(c, http.StatusOK, "Community Requests", "community_requests.html", data)
		return
	}

	// Default: the open queue. Fulfilled / In Progress tabs were retired —
	// fulfilled releases are reachable via /release/{id}, in-progress agent
	// state belongs to the dashboard.
	//
	// Split by origin since 2026-08-18. "open" (the historical default, and
	// what every existing link points at) now means MEMBER-filed; the
	// importer's rows moved to ?tab=automated. Anything unrecognised also
	// lands here, so a stale or mistyped tab shows a member the members'
	// board rather than an error.
	scope := ScopeMember
	if s, ok := tabScopes[tab]; ok {
		scope = s
	} else {
		tab = "open"
	}
	requests, total, err := h.deps.Requests.ListOpenRequests(ctx, scope, requestsPageSize, offset)
	if err != nil {
		h.errs.Report(ctx, "requests/open-list", err)
		c.String(http.StatusInternalServerError, "failed to load requests")
		return
	}
	totalPages := (total + requestsPageSize - 1) / requestsPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	h.attachRequestPriorities(ctx, requests)
	// Pagination has to stay on the tab it started on, or page 2 of the
	// automated list silently becomes page 2 of the members' list.
	pagerBase := "/community/requests?"
	if tab != "open" {
		pagerBase = "/community/requests?tab=" + url.QueryEscape(tab) + "&"
	}
	pg := h.deps.RenderPagination(page, totalPages, pagerBase)
	// ?created=<id> drives the post-submit success banner. Parse here (int
	// via ParseInt, 0 on missing/bad input) so the template can just
	// `{{if .JustCreatedID}}…{{end}}` instead of inventing its own coercion.
	justCreated, _ := strconv.ParseInt(c.Query("created"), 10, 64)

	// Prefill query params — used by the "Open a request" link from
	// /community/broken's alternatives panel. open=1 also flips the
	// New Request collapsible to expanded so the user lands on the form.
	prefill := map[string]string{
		"title":      c.Query("title"),
		"anime_id":   c.Query("anime_id"),
		"season":     c.Query("season"),
		"episodes":   c.Query("episodes"),
		"resolution": c.Query("resolution"),
		"source":     c.Query("source"),
	}
	openForm := c.Query("open") == "1"

	// ?err=<code> drives the validation flash banner. Mapped to a
	// human-readable string here so the template stays dumb.
	var errMsg string
	switch c.Query("err") {
	case "missing_title":
		errMsg = "Title is required."
		openForm = true
	case "missing_url":
		errMsg = "A torrent link is required. Paste a URL from one of the supported sites below."
		openForm = true
	case "invalid_url":
		errMsg = "That doesn't look like a torrent link. Paste a URL or magnet:?xt=urn:btih: link from one of the supported sites below."
		openForm = true
	case "host_not_allowed":
		errMsg = "That site isn't supported. Use one of the listed sources, or paste a magnet link."
		openForm = true
	}

	// listPageChrome supplies ViewerID (the per-row Fulfill/Delete gates need
	// to know who is looking), the tab badges, and the create form's keys.
	data := h.listPageChrome(ctx, c, tab, upscaleOpts)
	data["Requests"] = requests
	data["Total"] = total
	data["Page"] = page
	data["TotalPages"] = totalPages
	data["Pagination"] = pg
	data["JustCreatedID"] = justCreated
	data["Prefill"] = prefill
	data["OpenForm"] = openForm
	data["ErrorMessage"] = errMsg
	h.render(c, http.StatusOK, "Community Requests", "community_requests.html", data)
}

// backlogSummaryRow is one reason and its count, for the summary strip.
type backlogSummaryRow struct {
	BacklogReason
	Count int
}

// RequeueRequest puts one backlogged request back in the open queue.
//
// Any signed-in member, one click, no approval. The alternative — a mod
// queue — would rebuild the bottleneck this tab exists to remove: these
// requests stalled because nobody was looking at them, and requiring
// somebody to look before they can move is the same wait with more steps.
// The cost of a wrong click is one request back in a queue it was already
// in; requeued_by_id and requeue_count record who and how often.
func (h *Handlers) RequeueRequest(c *gin.Context) {
	ctx := c.Request.Context()
	back := "/community/requests?tab=backlog"
	// Keep the reader where they were. Re-parsed rather than echoed: this
	// value ends up in a Location header.
	if n, err := strconv.Atoi(c.PostForm("page")); err == nil && n > 1 {
		back += fmt.Sprintf("&page=%d", n)
	}

	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, back)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, back)
		return
	}

	moved, err := h.deps.Requests.RequeueBacklogRequest(ctx, id, user.ID)
	switch {
	case err != nil:
		// A silent failure here is indistinguishable from the button doing
		// nothing, and the member has no other way to tell.
		h.errs.Report(ctx, "requests/requeue", err)
		back += "&requeue=error"
	case moved:
		back += fmt.Sprintf("&requeued=%d", id)
	default:
		// Already back — someone else clicked first, or a double submit.
		back += "&requeue=already"
	}
	c.Redirect(http.StatusFound, back)
}

// RequestDetail shows full details for a single request.
func (h *Handlers) RequestDetail(c *gin.Context) {
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid id")
		return
	}
	req, err := h.deps.Requests.GetNzbRequestByID(ctx, id)
	if err != nil {
		c.String(http.StatusNotFound, "request not found")
		return
	}
	var anime *Anime
	if req.AnimeID != nil {
		anime, _ = h.deps.Anime.GetAnimeMetadata(ctx, *req.AnimeID)
	}
	user := h.deps.Viewer(c)
	userID := 0
	if user != nil {
		userID = user.ID
	}
	voted, _ := h.deps.Requests.HasVotedForRequest(ctx, id, userID)
	activeLock, _ := h.deps.Requests.GetActiveLockForRequest(ctx, id)
	lastFailed, _ := h.deps.Requests.GetLastFailedLockForRequest(ctx, id)
	queuePos, _ := h.deps.Requests.GetRequestQueuePosition(ctx, id)

	baseCost := h.deps.BoostCost(ctx)
	perGB := h.deps.BoostPerGB(ctx)
	userPoints := 0
	breakdown := boostCostBreakdown(baseCost, req.SeedCount, 0, perGB)
	if user != nil {
		userPoints = user.Points
	}

	canDelete := user != nil && (user.Contributor || user.ID == req.UserID)
	// Edit and delete share the same predicate. Kept as a separate
	// template flag so a future "moderators can edit but not delete"
	// (or vice-versa) policy is one-line away.
	canEdit := canDelete

	priorities, _ := h.deps.Requests.GetRequestPriorities(ctx, id)
	ptypes := h.deps.PriorityTypes()

	// Resolve a *live* NzbID for the "View release →" link.
	// GetNzbIDByInfoHash filters status='completed' AND deleted_at IS
	// NULL, so a soft-deleted NZB never surfaces. We deliberately do
	// NOT trust req.NzbID — when an NZB gets deleted (junk cleanup,
	// admin delete, vacuum), the request's NzbID stays pointing at the
	// gone row and the old "View release →" link 404s.
	//
	// liveNzbID > 0  → the file is in the catalog right now; render
	//                  the green Fulfilled card with a working link.
	// liveNzbID == 0 AND req.NzbID set → was once fulfilled but the
	//                  NZB has been removed; render an amber "release
	//                  removed" card instead so the user knows to vote
	//                  for a re-upload rather than chase a dead link.
	var liveNzbID int64
	if req.InfoHash != "" {
		liveNzbID, _ = h.deps.Nzbs.GetNzbIDByInfoHash(ctx, req.InfoHash)
	}
	nzbRemoved := liveNzbID == 0 && req.NzbID != nil && *req.NzbID > 0

	// Community-edit/delete proposals on this request. Always loaded so
	// the template can render pending ones for voting + a history of
	// resolved ones for audit trail.
	actions, _ := h.deps.Requests.GetRequestActionsForRequest(ctx, id)

	h.render(c, http.StatusOK, req.Title, "community_request_detail.html", gin.H{
		"Request":       req,
		"Anime":         anime,
		"Voted":         voted,
		"ActiveLock":    activeLock,
		"FailedLock":    lastFailed,
		"QueuePos":      queuePos,
		"BoostCost":     breakdown.FinalCost,
		"Breakdown":     breakdown,
		"UserPoints":    userPoints,
		"PriorityTotal": req.PriorityScore,
		"Priorities":    priorities,
		"PriorityTypes": ptypes,
		"CanDelete":     canDelete,
		"CanEdit":       canEdit,
		"CanUnpark":     user != nil && user.Contributor,
		"Duplicate":     c.Query("dup") == "1",
		"LiveNzbID":     liveNzbID,
		"NzbRemoved":    nzbRemoved,
		"Revived":       c.Query("revived") == "1",
		"Actions":       actions,
		"CanPropose":    user != nil,
		"IsMod":         user != nil && user.Mod,
		"InQueue": func() bool {
			if user == nil {
				return false
			}
			ids, _ := h.deps.Requests.GetAgentQueueRequestIDs(ctx, user.ID)
			return ids[id]
		}(),
	})
}

// allowedRequestHosts is the canonical list of torrent sources we accept
// in the request form. Mirrored verbatim in the template's "Supported
// sites" hint so users see the same list the validator enforces. Keep
// in sync if either side changes.
//
// Magnet URIs (`magnet:?xt=urn:btih:...`) are also accepted; they're
// not host-based but carry the info_hash directly which is what we
// actually need for dedup against the catalog.
var allowedRequestHosts = []string{
	"nyaa.si",
	"tokyotosho.info",
	"nekobt.to",
	"animetosho.org",
}

// validateRequestURL returns "" when the URL is acceptable, or a short
// machine-readable error code (rendered into a flash banner). Codes:
//
//	"missing_url"  -- empty / whitespace
//	"invalid_url"  -- can't parse as URL or magnet
//	"host_not_allowed" -- parsed but host isn't on the allowlist
//
// Sub-domains match (e.g. "old.nyaa.si") via host suffix check; query
// strings are ignored.
func validateRequestURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "missing_url"
	}
	// Magnet URIs are allowed unconditionally as long as they carry a
	// btih info hash. The CreateRequest flow extracts the hash a few
	// lines down so we don't need to repeat it here.
	if strings.HasPrefix(strings.ToLower(s), "magnet:") {
		if strings.Contains(strings.ToLower(s), "btih:") {
			return ""
		}
		return "invalid_url"
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "invalid_url"
	}
	host := strings.ToLower(u.Host)
	// Strip an optional port.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	for _, allowed := range allowedRequestHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return ""
		}
	}
	return "host_not_allowed"
}

func (h *Handlers) CreateRequest(c *gin.Context) {
	ctx := c.Request.Context()
	// AJAX detection — when the caller sets Accept: application/json (the
	// feed-page in-place Request button does this via fetch), every
	// outcome returns JSON instead of redirecting. Same business logic
	// either way; only the response shape differs.
	wantsJSON := strings.Contains(c.GetHeader("Accept"), "application/json")
	respond := func(httpCode int, redirectURL string, payload gin.H) {
		if wantsJSON {
			c.JSON(httpCode, payload)
			return
		}
		c.Redirect(http.StatusFound, redirectURL)
	}
	user := h.deps.Viewer(c)
	if user == nil {
		respond(http.StatusUnauthorized, "/community/requests",
			gin.H{"ok": false, "error": "not signed in"})
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		respond(http.StatusBadRequest, "/community/requests?err=missing_title",
			gin.H{"ok": false, "error": "missing title"})
		return
	}
	nyaaURL := strings.TrimSpace(c.PostForm("nyaa_url"))
	// Until the points-based bounty flow lands, every request must
	// carry a torrent link from one of the allowed sources. Anything
	// else (random forums, raw filenames, "please upload X" prose) is
	// the noise we're trying to stop here.
	if errCode := validateRequestURL(nyaaURL); errCode != "" {
		respond(http.StatusBadRequest, "/community/requests?err="+errCode,
			gin.H{"ok": false, "error": errCode})
		return
	}

	// info_hash: prefer the hidden field (filled by scrape), fall back to
	// extracting from a magnet URI in the URL field (Prowlarr flow).
	infoHash := strings.ToLower(strings.TrimSpace(c.PostForm("info_hash")))
	if infoHash == "" && strings.Contains(nyaaURL, "btih:") {
		if idx := strings.Index(strings.ToLower(nyaaURL), "btih:"); idx >= 0 {
			h := nyaaURL[idx+5:]
			if amp := strings.IndexByte(h, '&'); amp >= 0 {
				h = h[:amp]
			}
			infoHash = strings.ToLower(strings.TrimSpace(h))
		}
	}

	seedCount, _ := strconv.Atoi(c.PostForm("seed_count"))

	// Dedup on info hash before creating. Order:
	//
	//   1. Live catalog NZB with this hash → route to /release unless the
	//      stored health verdict says dead (then the resubmission is the
	//      signal someone wants a fresh upload).
	//
	//   2. Open request (non-deleted) with this hash → vote/boost.
	//
	//   3. Soft-deleted request with this hash → REVIVE it (clear
	//      deleted_at, reset state) instead of creating a duplicate.
	//      Same id, same FK-children (priorities, locks). The user is
	//      asking again so we honour their intent.
	//
	// We never trust an existing request row's stale NzbID — a fulfilled
	// request can carry an NzbID pointing at an NZB that's since been
	// deleted (junk cleanup, admin delete, vacuum); GetNzbIDByInfoHash
	// is the only source of truth for "in the catalog right now".
	if infoHash != "" {
		if nzbID, err := h.deps.Nzbs.GetNzbIDByInfoHash(ctx, infoHash); err == nil && nzbID > 0 {
			deadConfirmed := false
			if _, status, err := h.deps.Nzbs.GetNzbHealthMeta(ctx, nzbID); err == nil && status == "dead" {
				deadConfirmed = true
			}
			if !deadConfirmed {
				respond(http.StatusOK, fmt.Sprintf("/release/%d?already=1", nzbID),
					gin.H{"ok": true, "status": "in_catalog", "nzb_id": nzbID})
				return
			}
			// Confirmed-dead: fall through to the request path. The
			// user's resubmission becomes the signal that someone wants
			// a fresh upload of this content.
		}
		if existing, err := h.deps.Requests.GetRequestByInfoHash(ctx, infoHash); err == nil && existing != nil {
			// Stamp the back-link onto any feed_items row carrying this
			// hash. Idempotent: WHERE request_id IS NULL means we don't
			// stomp an existing link. Without this, a hash-match dup hit
			// from the feed-page Request button never updates the row's
			// request_id, so the listing keeps rendering "Request"
			// after refresh.
			_ = h.deps.FeedItems.LinkFeedItemRequestByInfoHash(ctx, infoHash, existing.ID)
			respond(http.StatusOK, fmt.Sprintf("/community/request/%d?dup=1", existing.ID),
				gin.H{"ok": true, "status": "existing", "request_id": existing.ID, "fulfilled": existing.Fulfilled})
			return
		}
		// Revive path: a soft-deleted match means the user is asking
		// for the same thing they (or an admin) previously deleted.
		// Reactivate that row instead of creating a duplicate.
		if soft, err := h.deps.Requests.GetSoftDeletedRequestByInfoHash(ctx, infoHash); err == nil && soft != nil {
			revived, err := h.deps.Requests.ReviveNzbRequest(ctx, soft.ID)
			if err != nil {
				h.errs.Report(ctx, "community/revive-request", err)
				if wantsJSON {
					c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "failed to revive request"})
				} else {
					c.String(http.StatusInternalServerError, "failed to revive request")
				}
				return
			}
			// Vote on behalf of the resubmitter so the revived row
			// inherits at least one vote (matches the implicit "the
			// requester votes for their own request" UX).
			_, _, _ = h.deps.Requests.VoteForRequest(ctx, revived.ID, user.ID)
			_ = h.deps.FeedItems.LinkFeedItemRequestByInfoHash(ctx, infoHash, revived.ID)
			respond(http.StatusOK, fmt.Sprintf("/community/request/%d?revived=1", revived.ID),
				gin.H{"ok": true, "status": "revived", "request_id": revived.ID})
			return
		}
	}

	// Reject blocked file extensions BEFORE inserting the row. The agent
	// runs RemoveBlockedFiles post-download, which deletes the .iso/.exe/
	// etc., and then the prepare step fails with "expected X under temp
	// dir: not found." Letting the request through is wasted bandwidth +
	// a confusing user error; the host's assembler already enforces this
	// same list at NZB-build time.
	if h.deps.BlockedExtension(title) {
		if wantsJSON {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "Requests for this file type aren't accepted (executables, ISOs, scripts, and other potentially-harmful formats are blocked)."})
		} else {
			c.String(http.StatusBadRequest, "Requests for this file type aren't accepted.")
		}
		return
	}

	// Per-request remux option. Only honoured when the agent fulfilling
	// the job has its own remux_bluray setting enabled. Constrained to
	// none/remux/both at the DB level via CHECK.
	remuxOption := strings.TrimSpace(c.PostForm("remux_option"))
	switch remuxOption {
	case "remux", "both":
		// keep as-is
	default:
		remuxOption = "none"
	}

	// AI upscale model key. Free-form — the model set is data-driven
	// (advertised per-agent), so unlike remux there's no enum to switch
	// on. Accept only the upscale_ namespace + a sane length so a crafted
	// form can't push arbitrary text into dispatch; empty = no upscale
	// (the default). Only honoured by agents with ai_upscale on.
	upscaleOption := strings.TrimSpace(c.PostForm("upscale_option"))
	if upscaleOption != "" && (!strings.HasPrefix(upscaleOption, "upscale_") || len(upscaleOption) > 64) {
		upscaleOption = ""
	}

	req := &Request{
		UserID:        user.ID,
		Username:      user.Username,
		Title:         title,
		Category:      c.PostForm("category"),
		Resolution:    c.PostForm("resolution"),
		Source:        c.PostForm("source"),
		Season:        c.PostForm("season"),
		Episodes:      c.PostForm("episodes"),
		NyaaURL:       nyaaURL,
		InfoHash:      infoHash,
		SeedCount:     seedCount,
		Notes:         c.PostForm("notes"),
		RemuxOption:   remuxOption,
		UpscaleOption: upscaleOption,
	}
	if aid := c.PostForm("anime_id"); aid != "" {
		if n, err := strconv.Atoi(aid); err == nil {
			req.AnimeID = &n
		}
	}
	if mid := c.PostForm("manga_id"); mid != "" {
		if n, err := strconv.Atoi(mid); err == nil {
			req.MangaID = &n
		}
	}
	if mid := c.PostForm("music_id"); mid != "" {
		if n, err := strconv.ParseInt(mid, 10, 64); err == nil {
			req.MusicID = &n
		}
	}
	if mid := c.PostForm("mal_id"); mid != "" {
		if n, err := strconv.Atoi(mid); err == nil {
			req.MalID = &n
		}
	}
	if alid := c.PostForm("anilist_id"); alid != "" {
		if n, err := strconv.Atoi(alid); err == nil {
			req.AnilistID = &n
		}
	}
	created, err := h.deps.Requests.CreateNzbRequest(ctx, req)
	if err != nil {
		if wantsJSON {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "failed to create request"})
		} else {
			c.String(http.StatusInternalServerError, "failed to create request")
		}
		return
	}

	// Stamp this fresh request_id back onto any feed_items row
	// carrying the same info_hash so the listing's LEFT JOIN starts
	// surfacing it on the next refresh. Without this, a user clicks
	// the in-place Request button, the request gets created, but the
	// listing keeps showing "Request" — and the next click sees the
	// row as "existing" (vote-counted toast), confusing the user.
	if created.InfoHash != "" {
		_ = h.deps.FeedItems.LinkFeedItemRequestByInfoHash(ctx, created.InfoHash, created.ID)
	}

	// Queue anime metadata scrape if an anime ID was provided, so cover art
	// and details are available on the request page.
	if created.AnimeID != nil && *created.AnimeID > 0 && h.deps.RefreshAnime != nil {
		go func(aid int) {
			if err := h.deps.RefreshAnime(context.Background(), aid); err != nil {
				log.Printf("anime metadata refresh for aid=%d: %v", aid, err)
			}
		}(*created.AnimeID)
	}

	// ?created=<id> drives the post-submit success banner in the template
	// so the user gets a visible "it worked" signal on the page they land on.
	respond(http.StatusOK, fmt.Sprintf("/community/requests?created=%d", created.ID),
		gin.H{"ok": true, "status": "queued", "request_id": created.ID})
}

// EditRequest updates the user-facing fields on an existing request.
// Owner OR contributor-and-up can edit; everyone else gets a redirect. Same
// fields the create form accepts (title, category, resolution, source,
// season, episodes, nyaa_url, info_hash, seed_count, notes, plus the
// three external IDs). Boost/vote/priority counters are NOT editable
// here — those are derived from user actions, not metadata.
func (h *Handlers) EditRequest(c *gin.Context) {
	ctx := c.Request.Context()
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	req, err := h.deps.Requests.GetNzbRequestByID(ctx, id)
	if err != nil || req == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	// Owner or contributor+. Contributors are trusted with direct
	// content edits (same gate as the host's canEditRelease and the
	// auto-resolve path on /community/request/:id/actions). The owner
	// branch covers plain users fixing their own typos.
	if user.ID != req.UserID && !user.Contributor {
		c.Redirect(http.StatusFound, "/community/request/"+strconv.FormatInt(id, 10))
		return
	}

	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		// Empty title would orphan the row from the list view; reject.
		c.Redirect(http.StatusFound, "/community/request/"+strconv.FormatInt(id, 10))
		return
	}
	req.Title = title
	req.Category = c.PostForm("category")
	req.Resolution = c.PostForm("resolution")
	req.Source = c.PostForm("source")
	req.Season = c.PostForm("season")
	req.Episodes = c.PostForm("episodes")
	req.NyaaURL = c.PostForm("nyaa_url")
	req.Notes = c.PostForm("notes")

	infoHash := strings.ToLower(strings.TrimSpace(c.PostForm("info_hash")))
	if infoHash == "" && strings.Contains(req.NyaaURL, "btih:") {
		if idx := strings.Index(strings.ToLower(req.NyaaURL), "btih:"); idx >= 0 {
			h := req.NyaaURL[idx+5:]
			if amp := strings.IndexByte(h, '&'); amp >= 0 {
				h = h[:amp]
			}
			infoHash = strings.ToLower(strings.TrimSpace(h))
		}
	}
	req.InfoHash = infoHash

	if seedStr := c.PostForm("seed_count"); seedStr != "" {
		if n, err := strconv.Atoi(seedStr); err == nil {
			req.SeedCount = n
		}
	}

	// External IDs: empty string means "clear it", non-empty means
	// "set to this int (or leave alone if invalid)". Mirroring the
	// create form's lenient parsing so a typo doesn't reject the edit.
	req.AnimeID = parseOptionalInt(c.PostForm("anime_id"))
	req.MalID = parseOptionalInt(c.PostForm("mal_id"))
	req.AnilistID = parseOptionalInt(c.PostForm("anilist_id"))

	if err := h.deps.Requests.UpdateNzbRequest(ctx, req); err != nil {
		log.Printf("UpdateNzbRequest(%d): %v", id, err)
		c.String(http.StatusInternalServerError, "failed to update request")
		return
	}

	// If the anime_id changed and we have a refresher, queue a metadata
	// scrape so the cover art / details on the request page line up.
	if req.AnimeID != nil && *req.AnimeID > 0 && h.deps.RefreshAnime != nil {
		go func(aid int) {
			if err := h.deps.RefreshAnime(context.Background(), aid); err != nil {
				log.Printf("anime metadata refresh for aid=%d: %v", aid, err)
			}
		}(*req.AnimeID)
	}

	c.Redirect(http.StatusFound, "/community/request/"+strconv.FormatInt(id, 10))
}

// parseOptionalInt is the lenient form-int parser used by both
// CreateRequest and EditRequest. Empty string → nil (clears the column),
// invalid number → nil (don't reject the whole edit), valid number → &n.
func parseOptionalInt(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

func (h *Handlers) DeleteRequest(c *gin.Context) {
	ctx := c.Request.Context()
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	// Contributors+ may delete any request (the deleteAny flag just
	// means "skip the owner-only WHERE clause" — the gate matches the
	// host's canEditRelease and the auto-resolve path on
	// /community/request/:id/actions). Plain users still fall through
	// to the owner-scoped UPDATE so they can delete their own row.
	canDeleteAny := user.Contributor
	if err := h.deps.Requests.DeleteNzbRequest(ctx, id, user.ID, canDeleteAny); err != nil {
		h.errs.Report(ctx, "requests/delete", err)
	}
	c.Redirect(http.StatusFound, "/community/requests")
}

// BulkDeleteRequests is the mod-only multi-select counterpart to
// DeleteRequest. Accepts either a repeated "ids" form field or a single
// comma-separated "ids" value so the JS can send it either way. Returns JSON
// with the count of rows deleted.
func (h *Handlers) BulkDeleteRequests(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		jsonError(c, http.StatusUnauthorized, "login required")
		return
	}
	if !user.Mod {
		jsonError(c, http.StatusForbidden, "admin only")
		return
	}

	raw := c.Request.PostForm["ids"]
	if len(raw) == 0 {
		_ = c.Request.ParseForm()
		raw = c.Request.PostForm["ids"]
	}
	var tokens []string
	for _, v := range raw {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tokens = append(tokens, t)
			}
		}
	}
	ids := make([]int64, 0, len(tokens))
	for _, t := range tokens {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil && n > 0 {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		jsonError(c, http.StatusBadRequest, "no ids")
		return
	}

	n, err := h.deps.Requests.BulkDeleteNzbRequests(c.Request.Context(), ids)
	if err != nil {
		h.errs.HandlerError(c, "community", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// BoostRequest lets a user spend points to increase a request's priority.
func (h *Handlers) BoostRequest(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		jsonError(c, http.StatusUnauthorized, "login required")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()

	baseCost := h.deps.BoostCost(ctx)
	// Look up the request to get seed count for cost calculation.
	reqInfo, err := h.deps.Requests.GetNzbRequestByID(ctx, id)
	if err != nil {
		jsonError(c, http.StatusNotFound, "request not found")
		return
	}
	boostPerGB := h.deps.BoostPerGB(ctx)
	cost := boostCostBreakdown(baseCost, reqInfo.SeedCount, 0, boostPerGB).FinalCost

	// Points flow through the core facade: typed spend_boost ledger
	// entry, reference_id = the request, ledger-write failures land
	// in the host's error log, and the returned balance saves the old
	// re-read of the user row.
	remaining, err := h.points.Deduct(ctx, int64(user.ID), cost, "spend_boost",
		fmt.Sprintf("Boosted request #%d", id), id)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "not enough points")
		return
	}
	// The deduction has landed, so from here every failure is a paid-for
	// boost that did not happen. A failed boost write refunds and errors
	// out; a failed priority bump keeps the boost (refunding a landed
	// boost would double-state) but must reach the error log — nothing
	// recomputes priority later, so silence here is a paid bump that
	// never moves the queue.
	if err := h.deps.Requests.BoostRequest(ctx, id); err != nil {
		h.errs.Report(ctx, "requests/boost-write", err)
		if _, rerr := h.points.Refund(ctx, int64(user.ID), cost, "refund_boost",
			fmt.Sprintf("Boost of request #%d failed", id), id); rerr != nil {
			h.errs.Report(ctx, "requests/boost-refund", rerr)
		}
		jsonError(c, http.StatusInternalServerError, "boost failed — points refunded")
		return
	}
	if err := h.deps.Requests.IncrementRequestPriority(ctx, id, "boost"); err != nil {
		h.errs.Report(ctx, "requests/boost-priority", err)
	}

	// Return updated boost count, priority score, and new queue position.
	req, _ := h.deps.Requests.GetNzbRequestByID(ctx, id)
	newBoost := 0
	priorityScore := 0
	if req != nil {
		newBoost = req.BoostCount
		priorityScore = req.PriorityScore
	}
	newPos, _ := h.deps.Requests.GetRequestQueuePosition(ctx, id)
	c.JSON(http.StatusOK, gin.H{
		"boost_count":      newBoost,
		"priority_score":   priorityScore,
		"points_spent":     cost,
		"queue_pos":        newPos,
		"points_remaining": remaining,
	})
}

// RetryRequest clears failed/expired locks so the request can be picked up again.
func (h *Handlers) RetryRequest(c *gin.Context) {
	ctx := c.Request.Context()
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	// Dispatch state: a silent failure here looks exactly like the retry
	// button doing nothing, which is what it looked like while this was
	// swallowed.
	if err := h.deps.AgentLocks.ClearFailedLocks(ctx, id); err != nil {
		h.errs.Report(ctx, "requests/retry-clear-locks", err)
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/community/request/%d", id))
}

// UnparkRequest clears the parked_until/parked_reason on a request so the
// dispatcher will hand it out again. Contributors+ — parking is the kill
// switch when many agents are rejecting a request, so unparking should
// only happen after a trusted human looked at it. Per-(request, agent)
// cooldowns (the 30-minute aborted-row check) are unaffected, so a
// reckless unpark won't immediately re-trigger the same agents.
func (h *Handlers) UnparkRequest(c *gin.Context) {
	ctx := c.Request.Context()
	user := h.deps.Viewer(c)
	if user == nil || !user.Contributor {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	// Unparking is the un-flip of the dispatch kill switch — a silent
	// failure leaves the request parked while the page implies otherwise.
	if err := h.deps.Requests.UnparkRequest(ctx, id); err != nil {
		h.errs.Report(ctx, "requests/unpark", err)
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/community/request/%d", id))
}

func (h *Handlers) FulfillRequest(c *gin.Context) {
	ctx := c.Request.Context()
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	// Mark-fulfilled is a closing action: only the requester or a mod
	// should get to use it. Previously any logged-in user could close any
	// request, which let randoms close someone else's open request out
	// from under them.
	req, err := h.deps.Requests.GetNzbRequestByID(ctx, id)
	if err != nil || req == nil {
		c.Redirect(http.StatusFound, "/community/requests")
		return
	}
	if req.UserID != user.ID && !user.Mod {
		c.Redirect(http.StatusFound, fmt.Sprintf("/community/request/%d", id))
		return
	}
	var nzbID *int64
	if s := c.PostForm("nzb_id"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			nzbID = &n
		}
	}
	// A closing action: if the write fails the request is still open, and
	// the redirect must not pretend otherwise — send the user back to the
	// request they tried to close, with the failure in the error log.
	if err := h.deps.Requests.FulfillNzbRequest(ctx, id, user.ID, nzbID); err != nil {
		h.errs.Report(ctx, "requests/fulfill", err)
		c.Redirect(http.StatusFound, fmt.Sprintf("/community/request/%d", id))
		return
	}
	c.Redirect(http.StatusFound, "/community/requests")
}

// VoteRequest toggles a vote on a request (AJAX).
func (h *Handlers) VoteRequest(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		jsonError(c, http.StatusUnauthorized, "login required")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}
	count, voted, err := h.deps.Requests.VoteForRequest(c.Request.Context(), id, user.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "vote failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count, "voted": voted})
}

// ScrapeNyaa fetches a nyaa.si page and extracts title, file list,
// seeders, info hash. URL is user-supplied so we use NewWhitelisted
// to enforce the allowlist at dial time — string-matching the URL
// before fetching is bypassable (e.g. http://attacker.com/?x=nyaa.si/),
// the dial-level host check is the only correct boundary.
func (h *Handlers) ScrapeNyaa(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		jsonError(c, http.StatusBadRequest, "url required")
		return
	}

	client := httpclient.NewWhitelisted(httpclient.DefaultAPITimeout,
		"nyaa.si", "tokyotosho.info", "nekobt.to")
	httpReq, _ := http.NewRequest("GET", rawURL, nil)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonError(c, http.StatusBadGateway, "failed to fetch page")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2 MB limit
	if err != nil {
		jsonError(c, http.StatusBadGateway, "failed to read page")
		return
	}
	html := string(body)

	// Pick the parser by hostname rather than substring-match. Substring
	// matches were the original implementation and are unsafe (an
	// attacker URL like http://x/?fake=nyaa.si/ would have routed to
	// scrapeNyaaHTML, but the dial-level whitelist now rejects it
	// outright; keep the parser dispatch tight regardless).
	lower := strings.ToLower(rawURL)
	result := gin.H{}
	switch {
	case strings.Contains(lower, "nyaa.si/"):
		result = scrapeNyaaHTML(html, h.deps.SanitizeHTML)
	case strings.Contains(lower, "nekobt.to/"):
		result = scrapeNekoBTHTML(html)
	default:
		result = scrapeToshoHTML(html)
	}

	// If the scraped info hash matches an existing request — or already
	// resolves to a catalog NZB — annotate the response so the form
	// preview can warn the user and offer a direct link to the file
	// instead of a doomed duplicate request. nzb_id is set whenever we
	// can hand the user a /release/<id> URL; the JS uses its presence
	// to pick the right link target.
	//
	// GetNzbIDByInfoHash is the single source of truth for "is this in
	// the catalog?" — it filters status='completed' AND deleted_at IS
	// NULL, so a soft-deleted NZB never surfaces here. We deliberately
	// do NOT fall back to the request row's stale NzbID: a request
	// flagged Fulfilled can carry an NzbID pointing at an NZB that's
	// since been deleted (junk cleanup, admin delete, vacuum), and
	// linking the user to /release/<gone> 404s. If GetNzbIDByInfoHash
	// returns 0, the NZB is gone — link to the request instead so the
	// user can vote / boost / re-submit.
	if hash, ok := result["info_hash"].(string); ok && hash != "" {
		ctx := c.Request.Context()
		liveNzbID, _ := h.deps.Nzbs.GetNzbIDByInfoHash(ctx, hash)
		if liveNzbID > 0 {
			dup := gin.H{
				"nzb_id":    liveNzbID,
				"fulfilled": true,
			}
			if nzb, err := h.deps.Nzbs.GetNzbByID(ctx, liveNzbID); err == nil && nzb != nil {
				if nzb.Title != "" {
					dup["title"] = nzb.Title
				} else if nzb.Filename != "" {
					dup["title"] = nzb.Filename
				}
			}
			result["duplicate_of"] = dup
		} else if existing, err := h.deps.Requests.GetRequestByInfoHash(ctx, hash); err == nil && existing != nil {
			// Request exists but no live NZB. Don't expose the request's
			// NzbID even if Fulfilled=true; that NzbID is the very pointer
			// that's gone stale. Send the user to the request itself.
			dup := gin.H{
				"id":        existing.ID,
				"title":     existing.Title,
				"fulfilled": existing.Fulfilled,
			}
			result["duplicate_of"] = dup
		}
	}
	c.JSON(http.StatusOK, result)
}

var (
	reNyaaTitle = regexp.MustCompile(`<h3 class="panel-title">\s*(.+?)\s*</h3>`)
	reNyaaSeed  = regexp.MustCompile(`(?i)<span[^>]*>Seeders:</span>\s*<span[^>]*>(\d+)</span>`)
	reNyaaHash  = regexp.MustCompile(`(?i)<kbd>([0-9a-fA-F]{40})</kbd>`)
	reNyaaFile  = regexp.MustCompile(`<li[^>]*>\s*<a[^>]*>([^<]+)</a>\s*<span[^>]*class="file-size"[^>]*>\(([^)]+)\)</span>`)
	// Sidebar metadata rows. Nyaa lays them out as
	//   <div class="col-md-1">LABEL:</div><div class="col-md-5">VALUE</div>
	// Capturing the col-md-5 inner HTML lets us pull each link/text out
	// in a follow-up pass, since the Category row carries two anchors
	// (primary + sub-category) and the Information row may carry text or
	// an arbitrary URL.
	reNyaaCatRow  = regexp.MustCompile(`(?s)<div class="col-md-1">\s*Category:\s*</div>\s*<div class="col-md-5">(.*?)</div>`)
	reNyaaInfoRow = regexp.MustCompile(`(?s)<div class="col-md-1">\s*Information:\s*</div>\s*<div class="col-md-5">(.*?)</div>`)
	reNyaaAnchor  = regexp.MustCompile(`(?s)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	// Description body. Nyaa renders the uploader's markdown into a
	// flat HTML run inside id="torrent-description" — the rendered
	// output is heading/list/inline tags, no nested divs in practice,
	// so a non-greedy match to the next </div> is reliable here. We
	// still pass the result through the host's sanitiser before
	// returning so any pathological content is neutered.
	reNyaaDesc      = regexp.MustCompile(`(?s)<div[^>]*id="torrent-description"[^>]*>(.*?)</div>`)
	reToshoTitle    = regexp.MustCompile(`<td class="desc-top">\s*<a[^>]*>([^<]+)</a>`)
	reScrapeSeaEp   = regexp.MustCompile(`(?i)S(\d{1,2})\s*[-_]?\s*(?:E|EP?)(\d{1,4})`)
	reScrapeSeaOnly = regexp.MustCompile(`(?i)(?:Season|S)\s*(\d{1,2})`)
	reScrapeEpOnly  = regexp.MustCompile(`(?i)(?:\b(?:E|EP|Episode)\s*(\d{1,4})|[-_ ](\d{2,3})(?:\s*[\[\(]|\s*$))`)
	// reVolumeRange matches "v01-05", "Vol.1-5", "Vol 01-05" as season ranges.
	reVolumeRange = regexp.MustCompile(`(?i)\b(?:v|vol\.?\s*)0*(\d+)\s*[-–]\s*0*(\d+)`)
)

// parseTitleMeta extracts resolution, source, and category from a torrent title.
func parseTitleMeta(title string) (resolution, source, category string) {
	upper := strings.ToUpper(title)
	// Resolution.
	for _, r := range []string{"2160p", "1080p", "720p", "480p"} {
		if strings.Contains(upper, strings.ToUpper(r)) {
			resolution = r
			break
		}
	}
	// Source — order matters (BluRay Remux before BluRay).
	sources := []struct{ pattern, label string }{
		{"BLURAY REMUX", "BluRay Remux"},
		{"BDREMUX", "BluRay Remux"},
		{"BD REMUX", "BluRay Remux"},
		{"BDRIP", "BDRip"},
		{"BLURAY", "BluRay"},
		{"BD ", "BluRay"},
		{"WEB-DL", "WEB-DL"},
		{"WEBDL", "WEB-DL"},
		{"WEBRIP", "WEBRip"},
		{"WEB ", "WEB-DL"},
		{"HDTV", "HDTV"},
		{"SDTV", "SDTV"},
		{"DVDRIP", "DVDRip"},
		{"DVD", "DVD"},
		{"LASERDISC", "LaserDisc"},
		{"LD ", "LaserDisc"},
		{"VHS", "VHS"},
	}
	for _, s := range sources {
		if strings.Contains(upper, s.pattern) {
			source = s.label
			break
		}
	}
	// Category — default to Anime for anime-related sites.
	category = "Anime"
	if strings.Contains(upper, "MOVIE") || strings.Contains(upper, "GEKIJOUBAN") {
		category = "Movie"
	}
	return
}

// scrapeNyaaHTML parses a nyaa.si torrent page. sanitizeHTML is the host's
// wiki sanitiser, applied to the uploader description before it reaches the
// form preview — it crosses as a parameter so the pure parser stays testable
// without a host.
func scrapeNyaaHTML(html string, sanitizeHTML func(string) string) gin.H {
	result := gin.H{}
	if m := reNyaaTitle.FindStringSubmatch(html); m != nil {
		result["title"] = strings.TrimSpace(m[1])
	}
	if m := reNyaaSeed.FindStringSubmatch(html); m != nil {
		n, _ := strconv.Atoi(m[1])
		result["seed_count"] = n
	}
	if m := reNyaaHash.FindStringSubmatch(html); m != nil {
		result["info_hash"] = strings.ToLower(m[1])
	}

	// Extract file list.
	var files []gin.H
	for _, m := range reNyaaFile.FindAllStringSubmatch(html, -1) {
		files = append(files, gin.H{"name": m[1], "size": m[2]})
	}
	result["files"] = files

	// Try to parse season/episode from title or first file name.
	parseSrc := ""
	if t, ok := result["title"].(string); ok {
		parseSrc = t
	}
	if parseSrc == "" && len(files) > 0 {
		parseSrc = files[0]["name"].(string)
	}
	if parseSrc != "" {
		enrichScrapeResult(result, parseSrc)
	}

	// Page-truth Category overrides the title-parse guess from
	// enrichScrapeResult. The sidebar row carries two anchors —
	// primary ("Anime", "Live Action", …) and sub ("English-translated",
	// "Non-English-translated", "Raw", …) — both of which map cleanly
	// to a tag/group on our side. We surface them as separate fields
	// so the form can offer both as suggestions; we also overwrite
	// result["category"] with the primary so the existing select-box
	// auto-fill picks up the page truth rather than a title heuristic.
	if m := reNyaaCatRow.FindStringSubmatch(html); m != nil {
		var cats []string
		for _, am := range reNyaaAnchor.FindAllStringSubmatch(m[1], -1) {
			label := strings.TrimSpace(stripHTMLTags(am[2]))
			if label != "" {
				cats = append(cats, label)
			}
		}
		if len(cats) > 0 {
			result["category"] = cats[0]
		}
		if len(cats) > 1 {
			result["subcategory"] = cats[1]
		}
		if len(cats) > 0 {
			result["categories"] = cats
		}
	}

	// Information row — typically a Discord/announce URL or a free-text
	// note. We return it as a string for display + a separate first_url
	// helper so the JS can render it as a clickable link without having
	// to parse anchor markup itself.
	if m := reNyaaInfoRow.FindStringSubmatch(html); m != nil {
		raw := strings.TrimSpace(m[1])
		text := strings.TrimSpace(stripHTMLTags(raw))
		if text != "" {
			result["information"] = text
		}
		if am := reNyaaAnchor.FindStringSubmatch(raw); am != nil {
			result["information_url"] = strings.TrimSpace(am[1])
		}
	}

	// Uploader description — runs through the host sanitiser so any inline
	// scripts/event handlers are stripped before the JS preview drops
	// the HTML into the page. Length-cap the input to keep the policy
	// runner from chewing on a runaway payload (the 2 MB body limit
	// upstream already bounds this, but a tighter cap is cheap).
	if m := reNyaaDesc.FindStringSubmatch(html); m != nil {
		raw := m[1]
		if len(raw) > 64*1024 {
			raw = raw[:64*1024]
		}
		clean := strings.TrimSpace(sanitizeHTML(raw))
		if clean != "" {
			result["description_html"] = clean
			result["description_text"] = strings.TrimSpace(stripHTMLTags(clean))
		}
	}
	return result
}

// stripHTMLTags is a tiny helper for pulling the visible text out of a
// fragment we already trust (the sanitiser's output, or sidebar anchor
// labels). It is NOT a sanitizer — never feed untrusted HTML to it
// expecting safety.
var reStripTags = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	return reStripTags.ReplaceAllString(s, "")
}

// enrichScrapeResult parses season/episode and title metadata from a source string.
func enrichScrapeResult(result gin.H, source string) {
	if m := reScrapeSeaEp.FindStringSubmatch(source); m != nil {
		result["season"] = m[1]
		result["episodes"] = m[2]
	} else if m := reVolumeRange.FindStringSubmatch(source); m != nil {
		result["season"] = m[1] + "-" + m[2]
	} else {
		if m := reScrapeSeaOnly.FindStringSubmatch(source); m != nil {
			result["season"] = m[1]
		}
		if m := reScrapeEpOnly.FindStringSubmatch(source); m != nil {
			ep := m[1]
			if ep == "" {
				ep = m[2]
			}
			result["episodes"] = ep
		}
	}
	res, src, cat := parseTitleMeta(source)
	if res != "" {
		result["resolution"] = res
	}
	if src != "" {
		result["source"] = src
	}
	if cat != "" {
		result["category"] = cat
	}
}

var (
	// <title>Torrent Name - nekoBT</title>
	reNekoBTTitle = regexp.MustCompile(`<title>(.+?)\s*-\s*nekoBT</title>`)
	// <span class="rounded px-1 text-xs">hash</span>
	reNekoBTHash = regexp.MustCompile(`(?i)class="[^"]*text-xs[^"]*">([0-9a-fA-F]{40})</span>`)
	// data-tip="Seeders">...N</span>
	reNekoBTSeed = regexp.MustCompile(`data-tip="Seeders"[^>]*>(?:[^<]*<[^>]*>)*[^<]*?(\d+)</span>`)
	// <img ... src="..." alt="Banner for ..."> (src before alt)
	reNekoBTBanner = regexp.MustCompile(`(?i)<img[^>]*src="([^"]+)"[^>]*alt="Banner for [^"]*"`)
	// <img ... alt="Banner for ..." ... src="..."> (alt before src)
	reNekoBTBanner2 = regexp.MustCompile(`(?i)<img[^>]*alt="Banner for [^"]*"[^>]*src="([^"]+)"`)
	// href="http://thetvdb.com/?tab=series&amp;id=453319"
	reNekoBTTVDB   = regexp.MustCompile(`(?i)href="(https?://(?:www\.)?thetvdb\.com/[^"]+)"`)
	reNekoBTTVDBID = regexp.MustCompile(`(?i)thetvdb\.com/?\?[^"]*id=(\d+)`)
	// href="https://www.themoviedb.org/tv/274622"
	reNekoBTTMDB = regexp.MustCompile(`(?i)href="https?://(?:www\.)?themoviedb\.org/(?:tv|movie)/(\d+)`)
	// href="https://www.imdb.com/title/tt37182080"
	reNekoBTIMDB = regexp.MustCompile(`(?i)href="https?://(?:www\.)?imdb\.com/title/(tt\d+)`)
	// File list: <span style="overflow-wrap: anywhere">filename</span></span> <span>size</span>
	reNekoBTFile = regexp.MustCompile(`overflow-wrap:\s*anywhere[^>]*>([^<]+)</span></span>\s*<span>([^<]+)</span>`)
)

func scrapeNekoBTHTML(html string) gin.H {
	result := gin.H{}
	if m := reNekoBTTitle.FindStringSubmatch(html); m != nil {
		result["title"] = strings.TrimSpace(m[1])
	}
	if m := reNekoBTSeed.FindStringSubmatch(html); m != nil {
		n, _ := strconv.Atoi(m[1])
		result["seed_count"] = n
	}
	if m := reNekoBTHash.FindStringSubmatch(html); m != nil {
		result["info_hash"] = strings.ToLower(m[1])
	}

	// Banner image (alt="Banner for ...").
	if m := reNekoBTBanner.FindStringSubmatch(html); m != nil {
		result["banner_url"] = m[1]
	} else if m := reNekoBTBanner2.FindStringSubmatch(html); m != nil {
		result["banner_url"] = m[1]
	}

	// External reference IDs.
	extIDs := gin.H{}
	if m := reNekoBTTVDB.FindStringSubmatch(html); m != nil {
		extIDs["tvdb_url"] = m[1]
		if idm := reNekoBTTVDBID.FindStringSubmatch(m[1]); idm != nil {
			n, _ := strconv.Atoi(idm[1])
			extIDs["tvdb_id"] = n
		}
	}
	if m := reNekoBTTMDB.FindStringSubmatch(html); m != nil {
		n, _ := strconv.Atoi(m[1])
		extIDs["tmdb_id"] = n
	}
	if m := reNekoBTIMDB.FindStringSubmatch(html); m != nil {
		extIDs["imdb_id"] = m[1]
	}
	if len(extIDs) > 0 {
		result["external_ids"] = extIDs
	}

	// File list.
	var files []gin.H
	for _, m := range reNekoBTFile.FindAllStringSubmatch(html, -1) {
		f := gin.H{"name": strings.TrimSpace(m[1])}
		if len(m) > 2 && m[2] != "" {
			f["size"] = m[2]
		}
		files = append(files, f)
	}
	result["files"] = files

	// Season/episode + resolution/source/category from title.
	if t, ok := result["title"].(string); ok && t != "" {
		enrichScrapeResult(result, t)
	}
	return result
}

func scrapeToshoHTML(html string) gin.H {
	result := gin.H{}
	if m := reToshoTitle.FindStringSubmatch(html); m != nil {
		result["title"] = strings.TrimSpace(m[1])
	}
	return result
}

// SearchTorrentResult is the normalized shape returned by SearchTorrents.
// Both upstream backends (nekoBT Torznab + Prowlarr v1 search) project
// into this struct so the request-form JS doesn't have to handle two
// flavors. Source tags which backend served the result so the UI can
// surface that to power users.
type SearchTorrentResult struct {
	Title    string `json:"title"`
	InfoHash string `json:"info_hash"`
	Link     string `json:"link"` // magnet or .torrent download URL
	// info_url has NO omitempty on purpose: prod always emitted the key
	// ("" for nekoBT results) and the wire shape is the contract.
	InfoURL string `json:"info_url"`
	Size    int64  `json:"size"`
	Seeders int    `json:"seeders"`
	Indexer string `json:"indexer"` // upstream indexer name (Prowlarr) or backend ("nekoBT")
	Source  string `json:"source"`  // "nekobt" or "prowlarr"
}

// SearchTorrents serves the request-form's search button. Prefers
// nekoBT (Torznab API) when the API key is configured, falls back to
// Prowlarr otherwise. Returns 503 only when neither is configured.
//
// Why nekoBT first: it's the user's own provisioned key going to a
// site that already feeds the resurrector / calendar paths -- one
// less external service in the request loop, and the result quality
// for anime queries is higher than the average Prowlarr indexer mix.
func (h *Handlers) SearchTorrents(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		jsonError(c, http.StatusBadRequest, "query required")
		return
	}

	// One upstream question per query per day: the anime page's missing-
	// episode click and the request form both land here, and every member
	// wanting the same episode asked the same tracker the same thing.
	// refresh=1 (the manual search button) bypasses and rewrites.
	cacheKey := strings.ToLower(query) + "|" + c.DefaultQuery("cat", "5070")
	if c.Query("refresh") == "" {
		if body := h.torCacheGet(cacheKey); body != nil {
			c.Data(http.StatusOK, "application/json; charset=utf-8", body)
			return
		}
	}
	serve := func(payload gin.H) {
		body, err := json.Marshal(payload)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "internal error")
			return
		}
		h.torCachePut(cacheKey, body)
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
	}

	if h.deps.Torznab != nil && h.deps.Torznab.Available() {
		raw, err := h.deps.Torznab.Search(c.Request.Context(), query, 0, 0)
		if err != nil {
			jsonError(c, http.StatusBadGateway, "upstream service unavailable")
			return
		}
		out := make([]SearchTorrentResult, 0, len(raw))
		for _, r := range raw {
			out = append(out, SearchTorrentResult{
				Title:    r.Title,
				InfoHash: r.InfoHash,
				Link:     r.Link,
				Size:     r.Size,
				Seeders:  r.Seeders,
				Indexer:  "nekoBT",
				Source:   "nekobt",
			})
		}
		serve(gin.H{"source": "nekobt", "results": out, "count": len(out)})
		return
	}

	if h.deps.Prowlarr != nil && h.deps.Prowlarr.Available() {
		cat := c.DefaultQuery("cat", "5070") // default to anime
		raw, err := h.deps.Prowlarr.Search(c.Request.Context(), query, cat)
		if err != nil {
			jsonError(c, http.StatusBadGateway, "upstream service unavailable")
			return
		}
		out := make([]SearchTorrentResult, 0, len(raw))
		for _, r := range raw {
			link := r.MagnetURL
			if link == "" {
				link = r.DownloadURL
			}
			out = append(out, SearchTorrentResult{
				Title:    r.Title,
				InfoHash: strings.ToLower(r.InfoHash),
				Link:     link,
				InfoURL:  r.InfoURL,
				Size:     r.Size,
				Seeders:  r.Seeders,
				Indexer:  r.Indexer,
				Source:   "prowlarr",
			})
		}
		serve(gin.H{"source": "prowlarr", "results": out, "count": len(out)})
		return
	}

	jsonError(c, http.StatusServiceUnavailable, "no torrent search indexer configured")
}

// SearchNekoBT proxies a Torznab search to nekoBT, scoped to an episode when
// season and episode query params are supplied. Returns JSON results the
// calendar page uses to populate the "find this episode" modal. Falls through
// to Prowlarr if Prowlarr is configured but nekoBT isn't, so the endpoint is
// useful on dev instances that only have one of the two wired up.
func (h *Handlers) SearchNekoBT(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		jsonError(c, http.StatusBadRequest, "query required")
		return
	}
	season, _ := strconv.Atoi(c.Query("season"))
	episode, _ := strconv.Atoi(c.Query("episode"))

	if h.deps.Torznab != nil && h.deps.Torznab.Available() {
		results, err := h.deps.Torznab.Search(c.Request.Context(), query, season, episode)
		if err != nil {
			jsonError(c, http.StatusBadGateway, "upstream service unavailable")
			return
		}
		c.JSON(http.StatusOK, gin.H{"source": "nekobt", "results": results, "count": len(results)})
		return
	}

	if h.deps.Prowlarr != nil && h.deps.Prowlarr.Available() {
		results, err := h.deps.Prowlarr.Search(c.Request.Context(), query, "5070")
		if err != nil {
			jsonError(c, http.StatusBadGateway, "upstream service unavailable")
			return
		}
		c.JSON(http.StatusOK, gin.H{"source": "prowlarr", "results": results, "count": len(results)})
		return
	}

	jsonError(c, http.StatusServiceUnavailable, "no torrent search indexer configured")
}

// LookupAnime returns anime metadata for a given AniDB/MAL/AniList ID (JSON).
func (h *Handlers) LookupAnime(c *gin.Context) {
	ctx := c.Request.Context()
	idType := c.Query("type") // "anidb", "mal", "anilist"
	idVal := c.Query("id")
	if idType == "" || idVal == "" {
		jsonError(c, http.StatusBadRequest, "type and id required")
		return
	}
	n, err := strconv.Atoi(idVal)
	if err != nil || n < 1 {
		jsonError(c, http.StatusBadRequest, "invalid id")
		return
	}

	var meta *Anime
	switch idType {
	case "anidb":
		meta, _ = h.deps.Anime.GetAnimeMetadata(ctx, n)
	case "mal":
		meta, _ = h.deps.Anime.GetAnimeByMalID(ctx, n)
	case "anilist":
		meta, _ = h.deps.Anime.GetAnimeByAnilistID(ctx, n)
	case "tvdb":
		meta, _ = h.deps.Anime.GetAnimeByTvdbID(ctx, n)
	case "tmdb":
		meta, _ = h.deps.Anime.GetAnimeByTmdbID(ctx, n)
	default:
		jsonError(c, http.StatusBadRequest, "type must be anidb, mal, anilist, tvdb, or tmdb")
		return
	}
	if meta == nil {
		jsonError(c, http.StatusNotFound, "anime not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"aid":        meta.AID,
		"mal_id":     meta.MalID,
		"anilist_id": meta.AnilistID,
		"tvdb_id":    meta.TvdbID,
		"tmdb_id":    meta.TmdbID,
		"title":      meta.Title,
		"episodes":   meta.Episodes,
		"status":     meta.Status,
		"format":     meta.Format,
		"genres":     meta.Genres,
		"start_date": meta.StartDate,
		"end_date":   meta.EndDate,
	})
}

// ── "This may already exist" card ───────────────────────────────────────────

// existingReleasesLimit caps the card at a screenful: the point is "check
// before requesting", not a full listing — the link rows carry the user to
// the release pages for that.
const existingReleasesLimit = 8

type existingReleaseJSON struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Season     *int   `json:"season"`
	Episode    *int   `json:"episode"`
	Resolution string `json:"resolution"`
	Source     string `json:"source"`
	Size       int64  `json:"size"`
	Date       string `json:"date"`
	Dead       bool   `json:"dead"`
}

type existingReleasesResponse struct {
	OK    bool `json:"ok"`
	Found bool `json:"found"`
	// Exact reports whether BOTH the season and episode filters were
	// applied, so the card can say "this episode already has N releases"
	// rather than the weaker series-level hint.
	Exact    bool                  `json:"exact"`
	Releases []existingReleaseJSON `json:"releases"`
	// Total is the number of rows RETURNED (the card is capped at a
	// screenful); HasMore reports that the cap truncated the real
	// count, so the UI can say "8+" instead of claiming a total it
	// doesn't know.
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

// parseEpisodeFilter reads the form's free-text season/episodes value
// best-effort: a lone non-negative integer filters that column; ranges
// ("1-12"), lists ("1,3"), "*", or anything else unparseable means no
// filter (nil) — a loose card beats a wrong one.
func parseEpisodeFilter(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// ExistingReleases answers the create form's "this may already exist" card
// (JSON). Resolution order: an explicit anime_id wins; otherwise the title
// param through the optional ResolveAnimeTitle seam; no resolution — or a
// host that wired neither seam — answers {ok:true, found:false}, so the
// card silently stays hidden and never breaks the form.
func (h *Handlers) ExistingReleases(c *gin.Context) {
	ctx := c.Request.Context()
	resp := existingReleasesResponse{OK: true, Releases: []existingReleaseJSON{}}
	if h.deps.ExistingReleases == nil {
		c.JSON(http.StatusOK, resp)
		return
	}

	animeID := 0
	if s := strings.TrimSpace(c.Query("anime_id")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			animeID = n
		}
	}
	if animeID == 0 && h.deps.ResolveAnimeTitle != nil {
		if title := strings.TrimSpace(c.Query("title")); title != "" {
			if id, ok := h.deps.ResolveAnimeTitle(ctx, title); ok && id > 0 {
				animeID = id
			}
		}
	}
	if animeID == 0 {
		c.JSON(http.StatusOK, resp)
		return
	}

	season := parseEpisodeFilter(c.Query("season"))
	episode := parseEpisodeFilter(c.Query("episodes"))
	resp.Exact = season != nil && episode != nil

	// One over the cap: the extra row only proves the cap truncated,
	// so the card can honestly say "8+" rather than presenting the
	// page length as the episode's total.
	rows, err := h.deps.ExistingReleases(ctx, animeID, season, episode, existingReleasesLimit+1)
	if err != nil {
		h.errs.Report(ctx, "requests/existing-releases", err)
		jsonError(c, http.StatusInternalServerError, "failed to check existing releases")
		return
	}
	if len(rows) > existingReleasesLimit {
		rows = rows[:existingReleasesLimit]
		resp.HasMore = true
	}
	for _, r := range rows {
		resp.Releases = append(resp.Releases, existingReleaseJSON{
			ID:         r.ID,
			Title:      r.Title,
			URL:        fmt.Sprintf("/release/%d", r.ID),
			Season:     r.Season,
			Episode:    r.Episode,
			Resolution: r.Resolution,
			Source:     r.Source,
			Size:       r.Size,
			Date:       r.CreatedAt.Format("2006-01-02"),
			Dead:       r.Dead,
		})
	}
	resp.Found = len(resp.Releases) > 0
	resp.Total = len(resp.Releases)
	c.JSON(http.StatusOK, resp)
}
