// Package releasegroups is the release-group directory — group
// pages, ownership claims with token-scrape verification, news
// posts with follower fan-out, bios, and the external-archive
// mirror.
//
// Lifted from the ameNZB host's in-repo plugin, markup included. The
// group tables stay host-owned (review-vote claim resolution, release
// pages, and the tagging service read the same rows), so the store and
// the claim/scrape machinery cross as seams — see deps.go.
package releasegroups

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// pageSize mirrors the host's release-list page size.
const pageSize = 50

const releaseGroupPageSize = 50

// Handlers serves the /release-groups* surface.
type Handlers struct {
	deps Deps
	errs core.ErrorReporter
	// bg returns the root context for post-response work (news fan-out, the
	// on-demand archive scrape). Set by Start; falls back to Background if a
	// request somehow lands first.
	bg func() context.Context
}

func (h *Handlers) rootCtx() context.Context {
	if h.bg != nil {
		return h.bg()
	}
	return context.Background()
}

// ReleaseGroupsList renders the public release-groups page with two tabs:
// confirmed (vetted) and unknown (auto-created from titles, awaiting an
// admin or scraper to fill in details).
func (h *Handlers) ReleaseGroupsList(c *gin.Context) {
	ctx := c.Request.Context()

	tab := c.DefaultQuery("tab", "confirmed")
	if tab != "confirmed" && tab != "unknown" {
		tab = "confirmed"
	}
	query := strings.TrimSpace(c.Query("q"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := pageOffset(page, releaseGroupPageSize)

	groups, total, err := h.deps.Groups.ListReleaseGroups(ctx, tab, query, releaseGroupPageSize, offset)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "loadfailed")
		return
	}

	// Tab counts come from the stats cache (refreshed hourly by the
	// Site Stats Cache job). Falls through to a live count if the
	// cache hasn't been populated yet — that path was the original
	// implementation and it's still correct, just slower.
	confirmedCount := int(0)
	unknownCount := int(0)
	if v, ok := h.deps.CachedStat(ctx, "release_groups_confirmed_count"); ok && v > 0 {
		confirmedCount = int(v)
	} else {
		confirmedCount, _ = h.deps.Groups.CountReleaseGroupsByStatus(ctx, "confirmed")
	}
	if v, ok := h.deps.CachedStat(ctx, "release_groups_unknown_count"); ok && v > 0 {
		unknownCount = int(v)
	} else {
		unknownCount, _ = h.deps.Groups.CountReleaseGroupsByStatus(ctx, "unknown")
	}

	totalPages := (total + releaseGroupPageSize - 1) / releaseGroupPageSize
	if totalPages < 1 {
		totalPages = 1
	}

	baseURL := "/release-groups?tab=" + tab
	if query != "" {
		baseURL += "&q=" + query
	}
	baseURL += "&"
	pg := h.deps.RenderPagination(page, totalPages, baseURL)

	h.render(c, http.StatusOK, "Release Groups", "release_groups_list.html", gin.H{
		"Tab":            tab,
		"Query":          query,
		"Groups":         groups,
		"Total":          total,
		"ConfirmedCount": confirmedCount,
		"UnknownCount":   unknownCount,
		"Page":           page,
		"TotalPages":     totalPages,
		"Pagination":     pg,
		"Suggested":      c.Query("suggested"),
	})
}

// ReleaseGroupDetail shows a header card for one group plus a paginated
// list of its NZBs. The NZB cards arrive pre-rendered from the host so the
// visual treatment matches /browse exactly.
func (h *Handlers) ReleaseGroupDetail(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := pageOffset(page, pageSize)

	user := h.deps.Viewer(c)
	showNSFW := user != nil && user.ShowNSFW
	nzbs, _, err := h.deps.GroupNzbCards(ctx, group.ID, showNSFW, pageSize, offset)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, "releasesfailed")
		return
	}

	// Use the cached count from release_groups (refreshed by the NZB Tag
	// Fill job and the scraper) instead of running a live COUNT(*) on
	// nzbs every page load — that count was the dominant cost on this
	// page for popular groups with thousands of rows.
	total := group.NzbCountCached
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	pg := h.deps.RenderPagination(page, totalPages, "/release-groups/"+slug+"?")

	// Community surface. Loaded best-effort: any individual failure
	// surfaces an empty section, not a 500, since the Releases grid is the
	// page's primary content.
	owners, _ := h.deps.Groups.GetReleaseGroupOwners(ctx, group.ID)
	followerCount, _ := h.deps.Groups.CountReleaseGroupFollowers(ctx, group.ID)
	// Torrent tab paginated in-place. `tpage` is its own query param so it
	// doesn't collide with `page` (NZB tab). `archiveTotal` is the live
	// visible-row count driving the tab badge, so the count is correct
	// immediately after a cross-write from the 30-min feed; the cached
	// column still drives the header "X on nekoBT" stat, which is
	// intentionally the upstream-snapshot count.
	const archivePageSize = 30
	tpage, _ := strconv.Atoi(c.DefaultQuery("tpage", "1"))
	if tpage < 1 {
		tpage = 1
	}
	archiveOffset := pageOffset(tpage, archivePageSize)
	archiveTorrents, archiveTotal, archiveErr := h.deps.Groups.ListExternalReleaseGroupTorrents(ctx, group.ID, archivePageSize, archiveOffset)
	if archiveErr != nil {
		// Best-effort: the Torrents tab simply renders empty if the
		// query fails. Surface the error so a broken JOIN or column
		// rename doesn't silently empty the tab.
		h.errs.Report(ctx, "release-group/list-archive", archiveErr)
	}
	archivePg := h.deps.RenderPaginationParam(tpage, archivePageSize, archiveTotal, "/release-groups/"+slug+"?", "tpage")
	archiveTotalPages := (archiveTotal + archivePageSize - 1) / archivePageSize
	if archiveTotalPages < 1 {
		archiveTotalPages = 1
	}
	// News posts. Newest-first, top 20 — keeps the initial render small.
	newsPosts, newsTotal, _ := h.deps.Groups.ListReleaseGroupNews(ctx, group.ID, 20, 0)

	var viewerIsOwner bool
	var viewerIsFollowing bool
	var viewerPendingClaim *Claim
	if user != nil {
		viewerIsOwner, _ = h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		viewerIsFollowing, _ = h.deps.Groups.IsUserFollowingReleaseGroup(ctx, group.ID, user.ID)
		viewerPendingClaim, _ = h.deps.Groups.GetUserPendingClaimForGroup(ctx, group.ID, user.ID)
	}

	h.render(c, http.StatusOK, group.Name, "release_group_detail.html", gin.H{
		"Group":             group,
		"Nzbs":              nzbs,
		"Total":             total,
		"Page":              page,
		"TotalPages":        totalPages,
		"Pagination":        pg,
		"Owners":            owners,
		"FollowerCount":     followerCount,
		"ViewerIsOwner":     viewerIsOwner,
		"ViewerIsFollowing": viewerIsFollowing,
		"ViewerClaim":       viewerPendingClaim,
		"NewsPosts":         newsPosts,
		"NewsTotal":         newsTotal,
		"ArchiveTorrents":   archiveTorrents,
		"ArchiveTotal":      archiveTotal,
		"ArchivePage":       tpage,
		"ArchiveTotalPages": archiveTotalPages,
		"ArchivePagination": archivePg,
		// Owner-authored bio, rendered through the host's bio-policy
		// sanitiser at request time. Empty when unset; the template hides
		// the card.
		"BioHTML": h.deps.RenderBioMarkdown(group.BioMarkdown),
		// Flash strings sourced from the redirect targets the claim +
		// verify + news handlers set. Whitelisted to known values so a
		// hand-edited query string can't inject template content.
		"ClaimFlash":  c.Query("claim"),
		"VerifyFlash": c.Query("verify"),
		"NewsFlash":   c.Query("news"),
		"BioFlash":    c.Query("bio"),
	})
}

// ClaimReleaseGroup handles a user submitting a new ownership claim.
// Generates a unique verification token, persists the claim, and
// redirects back to the detail page where the verify UI takes over.
//
// POST /release-groups/:slug/claim
func (h *Handlers) ClaimReleaseGroup(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug

	// Already an owner — nothing to claim.
	if owner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID); owner {
		c.Redirect(http.StatusFound, target+"?claim=already_owner")
		return
	}
	// Existing pending claim — bounce back instead of duplicating.
	if existing, _ := h.deps.Groups.GetUserPendingClaimForGroup(ctx, group.ID, user.ID); existing != nil {
		c.Redirect(http.StatusFound, target+"?claim=already_pending")
		return
	}

	// Verification URL: user can override at claim time (e.g. their
	// pinned Twitter post URL); otherwise we use the group's own
	// website_url. Trim + bound for safety.
	verifyURL := strings.TrimSpace(c.PostForm("verification_url"))
	if verifyURL == "" {
		verifyURL = group.WebsiteURL
	}
	if verifyURL != "" && !strings.HasPrefix(verifyURL, "http://") && !strings.HasPrefix(verifyURL, "https://") {
		c.Redirect(http.StatusFound, target+"?claim=bad_url")
		return
	}
	if len(verifyURL) > 500 {
		verifyURL = verifyURL[:500]
	}

	message := strings.TrimSpace(c.PostForm("claim_message"))
	if len(message) > 1000 {
		message = message[:1000]
	}

	token, err := h.deps.NewClaimToken()
	if err != nil {
		h.errs.HandlerError(c, "release-group/claim-token", err)
		return
	}

	claim := &Claim{
		ReleaseGroupID:    group.ID,
		UserID:            user.ID,
		Role:              RoleOwner,
		ClaimMessage:      message,
		VerificationToken: token,
		VerificationURL:   verifyURL,
	}
	if _, err := h.deps.Groups.CreateReleaseGroupClaim(ctx, claim); err != nil {
		h.errs.HandlerError(c, "release-group/claim-create", err)
		return
	}
	c.Redirect(http.StatusFound, target+"?claim=created")
}

// PostReleaseGroupNews accepts a new news post from an owner of the
// group. Title + body come from the form; once the row is inserted
// we fan out one notification per follower in a background goroutine
// so a popular group's publish doesn't block the request goroutine.
//
// POST /release-groups/:slug/news
func (h *Handlers) PostReleaseGroupNews(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug

	// Gate: must be an owner (or mod) to post. Maintainers also
	// qualify since IsReleaseGroupOwner matches both roles today.
	if !user.Mod {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		if !isOwner {
			c.Redirect(http.StatusFound, target+"?news=forbidden")
			return
		}
	}

	title := strings.TrimSpace(c.PostForm("title"))
	body := strings.TrimSpace(c.PostForm("body"))
	if title == "" {
		c.Redirect(http.StatusFound, target+"?news=missing_title")
		return
	}
	if len(title) > 200 {
		title = title[:200]
	}
	if len(body) > 10_000 {
		body = body[:10_000]
	}

	postID, err := h.deps.Groups.CreateReleaseGroupNewsPost(ctx, &NewsPost{
		ReleaseGroupID: group.ID,
		AuthorUserID:   user.ID,
		Title:          title,
		Body:           body,
	})
	if err != nil {
		h.errs.HandlerError(c, "release-group/news-create", err)
		return
	}

	// Fan out to followers in the background, bound to the root context so
	// a request cancellation doesn't kill the dispatch — the post itself is
	// already committed and the user has been redirected.
	go h.fanOutReleaseGroupNews(h.rootCtx(), group, user, postID, title)

	c.Redirect(http.StatusFound, target+"?news=posted")
}

// fanOutReleaseGroupNews inserts one notification row per follower
// for a freshly-published news post. Best-effort per follower: a
// single insert failure logs + continues so a transient DB issue
// doesn't lose every follower's notification.
//
// Notification link is the release-group detail page with the post's
// anchor so the recipient lands on the right scroll position. Title
// is the post title prefixed with the group name; body is empty so
// the existing /notifications template renders just the title row.
func (h *Handlers) fanOutReleaseGroupNews(ctx context.Context, group *Group, author *Viewer, postID int64, title string) {
	followerIDs, err := h.deps.Groups.ListReleaseGroupFollowerIDs(ctx, group.ID)
	if err != nil {
		h.errs.Report(ctx, "release-group/news-fanout-followers", err)
		return
	}
	link := "/release-groups/" + group.Slug + "#news-" + strconv.FormatInt(postID, 10)
	notifyTitle := group.Name + ": " + title
	for _, uid := range followerIDs {
		// Skip self-notification — author following their own group
		// shouldn't get pinged for their own post.
		if uid == author.ID {
			continue
		}
		authorID := author.ID
		n := Notification{
			UserID:    uid,
			Type:      NotificationTypeNews,
			ActorID:   &authorID,
			ActorName: author.Username,
			Title:     notifyTitle,
			Link:      link,
		}
		if err := h.deps.Notify(ctx, n); err != nil {
			h.errs.Report(ctx, "release-group/news-fanout-insert", err)
			continue
		}
	}
}

// DeleteReleaseGroupNewsPost soft-deletes a news post. Allowed by
// the author OR any owner/maintainer of the group OR a mod.
//
// POST /release-groups/:slug/news/:id/delete
func (h *Handlers) DeleteReleaseGroupNewsPost(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusFound, target+"?news=bad_id")
		return
	}

	post, err := h.deps.Groups.GetReleaseGroupNewsPost(ctx, id)
	if err != nil || post == nil || post.ReleaseGroupID != group.ID {
		c.Redirect(http.StatusFound, target+"?news=not_found")
		return
	}

	allowed := pluginapi.VisibleTo(post.AuthorUserID, user.ID, user.Mod)
	if !allowed {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		allowed = isOwner
	}
	if !allowed {
		c.Redirect(http.StatusFound, target+"?news=forbidden")
		return
	}

	if err := h.deps.Groups.SoftDeleteReleaseGroupNewsPost(ctx, id); err != nil {
		h.errs.HandlerError(c, "release-group/news-delete", err)
		return
	}
	c.Redirect(http.StatusFound, target+"?news=deleted")
}

// ToggleReleaseGroupFollow is one handler covering both follow and
// unfollow so the detail-page button can be a single POST with a
// known target — the server inspects current state and flips it.
// JSON response so the button can update via fetch() without a full
// page reload (form-style fallback redirects to the detail page).
//
// POST /release-groups/:slug/follow
func (h *Handlers) ToggleReleaseGroupFollow(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		if strings.Contains(c.GetHeader("Accept"), "application/json") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
			return
		}
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}

	// Flip whichever state the user is in. Both storage methods are
	// idempotent (INSERT ON CONFLICT / DELETE WHERE) so a stale UI
	// double-click can't corrupt state.
	following, _ := h.deps.Groups.IsUserFollowingReleaseGroup(ctx, group.ID, user.ID)
	if following {
		if _, err := h.deps.Groups.UnfollowReleaseGroup(ctx, group.ID, user.ID); err != nil {
			h.errs.HandlerError(c, "release-group/unfollow", err)
			return
		}
	} else {
		if _, err := h.deps.Groups.FollowReleaseGroup(ctx, group.ID, user.ID); err != nil {
			h.errs.HandlerError(c, "release-group/follow", err)
			return
		}
	}

	count, _ := h.deps.Groups.CountReleaseGroupFollowers(ctx, group.ID)
	if strings.Contains(c.GetHeader("Accept"), "application/json") {
		jsonOK(c, gin.H{"following": !following, "follower_count": count})
		return
	}
	c.Redirect(http.StatusFound, "/release-groups/"+slug)
}

// VerifyReleaseGroupClaim runs the scrape-for-token check on a
// pending claim. On success the claim auto-approves + the owner row
// is created in the same transaction (host-side, inside the resolve).
//
// POST /release-groups/:slug/claim/verify
//
// Caller must own the claim. We look up the claim by (group, user)
// rather than taking an id in the URL so a leaked claim id can't
// be replayed across users.
func (h *Handlers) VerifyReleaseGroupClaim(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug

	claim, err := h.deps.Groups.GetUserPendingClaimForGroup(ctx, group.ID, user.ID)
	if err != nil {
		h.errs.HandlerError(c, "release-group/verify-lookup", err)
		return
	}
	if claim == nil {
		c.Redirect(http.StatusFound, target+"?verify=no_claim")
		return
	}

	// Backlink substring is built from the group's slug + the SESSION user
	// id so the scraper can confirm two things at once: that the published
	// page links back to THIS group, AND that the link names the user who's
	// filing the claim (?owner=<id>). Pulling the id from the session — not
	// the claim row — means a tampered claim_id query can't trick verify
	// into approving a claim owned by someone else.
	expectedBacklink := h.deps.Backlink(group.Slug, user.ID)
	err = h.deps.VerifyClaim(ctx, claim, expectedBacklink)
	switch {
	case err == nil:
		c.Redirect(http.StatusFound, target+"?verify=approved")
	case errors.Is(err, ErrVerifyTooSoon):
		c.Redirect(http.StatusFound, target+"?verify=cooldown")
	case errors.Is(err, ErrVerifyTokenNotFound):
		c.Redirect(http.StatusFound, target+"?verify=token_missing")
	default:
		h.errs.Report(ctx, "release-group/verify", err)
		c.Redirect(http.StatusFound, target+"?verify=error")
	}
}

// SuggestReleaseGroupForm shows the user-facing form for creating a new
// suggestion (either edit-existing or propose-new). Group is optional —
// if slug is empty, the form is in "propose new" mode.
func (h *Handlers) SuggestReleaseGroupForm(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")

	var group *Group
	if slug != "" {
		g, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
		if err != nil || g == nil {
			h.fail(c, http.StatusNotFound, "nogroup")
			return
		}
		group = g
	}

	// Mode-dependent title, matching the original page's wording: an
	// edit-mode tab/bookmark must name the group.
	title := "Suggest a Group"
	if group != nil {
		title = "Suggest Edit · " + group.Name
	}
	h.render(c, http.StatusOK, title, "release_group_suggest.html", gin.H{
		"Group": group,
		"IsNew": group == nil,
	})
}

// SubmitReleaseGroupSuggestion writes a pending suggestion. The same
// handler covers both "edit existing" (slug param present) and "propose
// new" (slug empty); the storage layer disambiguates by group_id NULL.
func (h *Handlers) SubmitReleaseGroupSuggestion(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")

	var groupID *int
	if slug != "" {
		g, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
		if err != nil || g == nil {
			h.fail(c, http.StatusNotFound, "nogroup")
			return
		}
		groupID = &g.ID
	}

	name := strings.TrimSpace(c.PostForm("name"))
	if groupID == nil && name == "" {
		h.fail(c, http.StatusBadRequest, "nameneeded")
		return
	}

	sug := &Suggestion{
		GroupID:     groupID,
		UserID:      user.ID,
		Username:    user.Username,
		Name:        name,
		WebsiteURL:  strings.TrimSpace(c.PostForm("website_url")),
		Description: strings.TrimSpace(c.PostForm("description")),
		LogoURL:     strings.TrimSpace(c.PostForm("logo_url")),
		Notes:       strings.TrimSpace(c.PostForm("notes")),
	}
	if _, err := h.deps.Groups.CreateReleaseGroupSuggestion(ctx, sug); err != nil {
		log.Printf("releasegroups/suggest: %v", err)
		h.fail(c, http.StatusInternalServerError, "suggestfailed")
		return
	}

	if slug != "" {
		c.Redirect(http.StatusFound, "/release-groups/"+slug+"?suggested=1")
	} else {
		c.Redirect(http.StatusFound, "/release-groups?suggested=1")
	}
}

func (h *Handlers) ReleaseGroupArchive(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const pageSize = 50
	offset := (page - 1) * pageSize

	torrents, total, err := h.deps.Groups.ListExternalReleaseGroupTorrents(ctx, group.ID, pageSize, offset)
	if err != nil {
		h.errs.Report(ctx, "release-group/archive-list", err)
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	user := h.deps.Viewer(c)
	var viewerIsOwner bool
	if user != nil {
		viewerIsOwner, _ = h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
	}

	h.render(c, http.StatusOK, group.Name+" — Archive", "release_group_archive.html", gin.H{
		"Group":         group,
		"Torrents":      torrents,
		"Total":         total,
		"Page":          page,
		"TotalPages":    totalPages,
		"ViewerIsOwner": viewerIsOwner,
		"RefreshFlash":  c.Query("refresh"),
		// Bulk-request flash + per-category counts (filed/dup/already/skipped).
		// Counters arrive as query params from BulkRequestMissingFromArchive's
		// redirect so the template can render "Filed N, M already pending, …".
		"BulkFlash":   c.Query("bulk"),
		"BulkFiled":   c.Query("filed"),
		"BulkDup":     c.Query("dup"),
		"BulkAlready": c.Query("already"),
		"BulkSkipped": c.Query("skipped"),
		// Hide-torrent action flash (?hide=ok / forbidden / bad_id).
		"HideFlash": c.Query("hide"),
	})
}

// RefreshReleaseGroupArchive triggers an on-demand pull from the group's
// configured upstream. Rate-limited via archive_last_refresh_at so a
// click-happy owner can't hammer the upstream API. The actual scrape runs
// in a background goroutine bound to the root context — the request
// goroutine just kicks it off and redirects with a flash.
//
// POST /release-groups/:slug/archive/refresh
func (h *Handlers) RefreshReleaseGroupArchive(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug + "/archive"

	// Refresh is open to any logged-in user — archive freshness isn't a
	// privileged action, just a request to backfill from upstream.
	// Ownership is reserved for news posts + the hide-torrent curation
	// surface.
	//
	// Per-group rate limit: 15 min between any user's triggered refreshes.
	// Mods bypass (force-resync after fixing a misconfigured group id).
	// The scheduled daily sweep uses its own off-peak gate and isn't
	// constrained by this.
	const archiveRefreshCooldown = 15 * time.Minute
	if !user.Mod && group.ArchiveLastRefreshAt != nil {
		if since := time.Since(*group.ArchiveLastRefreshAt); since < archiveRefreshCooldown {
			c.Redirect(http.StatusFound, target+"?refresh=cooldown")
			return
		}
	}
	// At least one source must be configured — nekoBT id OR a
	// (scrape_source=nyaa + scrape_url) pair. The dispatcher runs
	// whichever (or both) are set; the gate only blocks the truly
	// unconfigured case so an owner hitting Refresh on a never-
	// linked group gets a clear flash instead of a generic error.
	hasNekobt := strings.TrimSpace(group.NekobtGroupID) != ""
	hasNyaa := group.ScrapeSource == ArchiveSourceNyaa &&
		strings.TrimSpace(group.ScrapeURL) != ""
	if !hasNekobt && !hasNyaa {
		// Differentiate "never tried" from "confirmed absent" so the
		// flash banner can be honest. SubsPlease etc. are genuinely
		// not on nekoBT — telling the user "an admin needs to link it"
		// is misleading when the scanner has already confirmed there's
		// nothing to link AND no nyaa fallback was set.
		flash := "unlinked"
		if group.NekobtStatus == "not_found" {
			flash = "not_found"
		}
		c.Redirect(http.StatusFound, target+"?refresh="+flash)
		return
	}

	groupID := group.ID
	groupSlug := group.Slug
	go func() {
		bg := h.rootCtx()
		n, err := h.deps.ScrapeArchive(bg, groupID)
		if err != nil {
			h.errs.Report(bg, "release-group/archive-scrape", err)
			return
		}
		log.Printf("release-group %s archive: refreshed (%d torrents)", groupSlug, n)
	}()
	c.Redirect(http.StatusFound, target+"?refresh=started")
}

// HideArchiveTorrent soft-deletes one cached torrent row from the group's
// archive surface. Owner- or mod-only — owners are the curation layer for
// their own group's archive, hiding mis-attributed entries the upstream
// scraper grouped under this tag incorrectly.
//
// The row stays in the table so the daily scraper's UPSERT doesn't recreate
// it on every refresh; the read path filters hidden rows.
//
// POST /release-groups/:slug/archive/torrents/:torrentID/hide
func (h *Handlers) HideArchiveTorrent(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug
	// Gate: owner or mod. Same pattern as news post + bio.
	if !user.Mod {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		if !isOwner {
			c.Redirect(http.StatusFound, target+"?hide=forbidden")
			return
		}
	}
	torrentID := strings.TrimSpace(c.Param("torrentID"))
	if torrentID == "" {
		c.Redirect(http.StatusFound, target+"?hide=bad_id")
		return
	}
	reason := strings.TrimSpace(c.PostForm("reason"))
	n, err := h.deps.Groups.HideExternalReleaseGroupTorrent(ctx, group.ID, torrentID, user.ID, reason)
	if err != nil {
		h.errs.HandlerError(c, "release-group/archive-hide", err)
		return
	}
	// Zero rows = the torrent id isn't in this group's archive (the
	// IDOR guard fired). Surface bad_id so a crafted URL hitting
	// someone else's torrent looks the same as a typo.
	if n == 0 {
		c.Redirect(http.StatusFound, target+"?hide=bad_id")
		return
	}
	c.Redirect(http.StatusFound, target+"?hide=ok")
}

// BulkRequestMissingFromArchive bulk-creates download requests for
// every Missing row on the archive page in one click. Owner- (or
// mod-) only. Each row goes through the same dedup the regular
// request handler does — existing-catalog hits short-circuit,
// existing-request hits skip silently — so a click on this is
// idempotent.
//
// Capped at 50 requests per click so a runaway loop can't dump
// thousands of request rows from one POST. Larger archives need
// multiple clicks (and the per-group cooldown rate-limits that).
//
// POST /release-groups/:slug/archive/request-missing
func (h *Handlers) BulkRequestMissingFromArchive(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug + "/archive"

	// Gate: must be an owner OR mod. Same pattern as bio + refresh.
	if !user.Mod {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		if !isOwner {
			c.Redirect(http.StatusFound, target+"?bulk=forbidden")
			return
		}
	}

	const bulkRequestCap = 50
	// Walk the archive snapshot top-of-list (newest first) so the
	// owner's most-recent uploads get filed first.
	rows, _, err := h.deps.Groups.ListExternalReleaseGroupTorrents(ctx, group.ID, bulkRequestCap, 0)
	if err != nil {
		h.errs.Report(ctx, "release-group/archive-bulk-list", err)
		c.Redirect(http.StatusFound, target+"?bulk=error")
		return
	}

	var (
		filed     int
		alreadyIn int // already in our catalog (infohash hit on nzbs)
		dupReq    int // already an open request
		skipped   int // missing infohash → can't dedup safely
	)
	for _, r := range rows {
		if r.OurNzbID != nil {
			alreadyIn++
			continue
		}
		ih := strings.ToLower(strings.TrimSpace(r.InfoHash))
		if ih == "" {
			// Without an infohash we can't dedup, and the agent
			// dispatcher needs one to find the torrent — silently
			// skip rather than file an orphan request.
			skipped++
			continue
		}
		// Catalog dedup. If the infohash is in the catalog, the archive
		// page row should already be tagged Available; if it's not,
		// the row's our_nzb_id will get filled on the next refresh.
		if nzbID, _ := h.deps.NzbIDByInfoHash(ctx, ih); nzbID > 0 {
			alreadyIn++
			continue
		}
		// Request dedup.
		if exists, _ := h.deps.RequestExistsByInfoHash(ctx, ih); exists {
			dupReq++
			continue
		}
		sizeBytes := r.FilesizeBytes
		req := BulkRequest{
			UserID:    user.ID,
			Username:  user.Username,
			Title:     r.Title,
			Category:  "Anime",
			NyaaURL:   "https://nekobt.to/torrents/" + r.NekobtTorrentID,
			InfoHash:  ih,
			SeedCount: r.Seeders,
			SizeBytes: &sizeBytes,
		}
		if err := h.deps.CreateRequest(ctx, req); err != nil {
			h.errs.Report(ctx, "release-group/archive-bulk-create", err)
			continue
		}
		filed++
	}

	log.Printf("release-group %s bulk-request: filed=%d already=%d dup=%d skipped=%d",
		group.Slug, filed, alreadyIn, dupReq, skipped)
	c.Redirect(http.StatusFound,
		fmt.Sprintf("%s?bulk=ok&filed=%d&dup=%d&already=%d&skipped=%d",
			target, filed, dupReq, alreadyIn, skipped))
}

// EditReleaseGroupBio renders the owner's markdown bio editor.
//
// GET /release-groups/:slug/bio
func (h *Handlers) EditReleaseGroupBio(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	// Gate: must be an owner (or mod). Same check the news-post
	// form uses so the surfaces stay consistent.
	if !user.Mod {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		if !isOwner {
			c.Redirect(http.StatusFound, "/release-groups/"+slug+"?bio=forbidden")
			return
		}
	}
	h.render(c, http.StatusOK, group.Name+" — Bio", "release_group_bio_edit.html", gin.H{
		"Group":     group,
		"BioSource": group.BioMarkdown,
		"BioFlash":  c.Query("bio"),
	})
}

// SaveReleaseGroupBio persists the owner's markdown bio.
//
// POST /release-groups/:slug/bio
func (h *Handlers) SaveReleaseGroupBio(c *gin.Context) {
	user := h.deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	slug := c.Param("slug")
	group, err := h.deps.Groups.GetReleaseGroupBySlug(ctx, slug)
	if err != nil || group == nil {
		h.fail(c, http.StatusNotFound, "nogroup")
		return
	}
	target := "/release-groups/" + slug
	if !user.Mod {
		isOwner, _ := h.deps.Groups.IsReleaseGroupOwner(ctx, group.ID, user.ID)
		if !isOwner {
			c.Redirect(http.StatusFound, target+"?bio=forbidden")
			return
		}
	}
	md := c.PostForm("bio_markdown")
	if err := h.deps.Groups.SetReleaseGroupBio(ctx, group.ID, md); err != nil {
		h.errs.HandlerError(c, "release-group/bio-save", err)
		return
	}
	c.Redirect(http.StatusFound, target+"?bio=saved")
}

// VerifyTokenRedirect resolves a per-claim verification token to the
// matching release-group's slug and redirects there. Public URL
// referenced from the claim-verification snippet
// (`<a href="…/v/<token>">…</a>`) so a visitor who clicks the small
// link lands on the group's page instead of a 404. The endpoint
// itself is read-only and never approves a claim — that lives behind
// the authenticated verify flow.
//
// On no-match (typo, expired claim row, etc.) we 302 to the public
// release-groups list so the user has somewhere obvious to go next.
//
// GET /v/:token
func (h *Handlers) VerifyTokenRedirect(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		c.Redirect(http.StatusFound, "/release-groups")
		return
	}
	slug, err := h.deps.Groups.GetReleaseGroupSlugByClaimToken(c.Request.Context(), token)
	if err != nil {
		h.errs.Report(c.Request.Context(), "release-group/verify-redirect", err)
		c.Redirect(http.StatusFound, "/release-groups")
		return
	}
	if slug == "" {
		c.Redirect(http.StatusFound, "/release-groups")
		return
	}
	c.Redirect(http.StatusFound, "/release-groups/"+slug)
}
