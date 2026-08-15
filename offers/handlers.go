// Package offers is the offer system — users register what they
// can deliver (private tracker / personal collection), other users
// file "request this" against buckets, and the Python offer-agent
// claims + delivers via /api/offers/*. Fifth pkg/core plugin.
//
// SHARED-domain plugin (same pattern as plugins/requests): the
// offer tables stay in pkg/storage because the agent fleet's
// /api/agent/offer/* surface (host) works the same rows, and the
// pruner/sweeper background jobs stay host-side because they run
// in the WORKER process and plugins currently boot only in web
// mode. Deps arrive via SetDeps from cmd/main.go.
package offers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Handlers serves the offer-system surfaces. Deps carries the
// shared app-domain repos + host singletons (see deps.go); the
// Core seams are Errors (persistent 500s) and Router/Auth at
// registration time.
type Handlers struct {
	// core is the mediator, for announcing requests and deliveries. Nil in tests.
	core *core.Core
}

// resolveAPIUser pulls the API key from `?apikey=` or
// `Authorization: Bearer <key>` and returns the matching user. Same
// shape as ApiHandler.requireAPIKey but without the per-call
// daily-quota tracking (offer-system calls don't count against the
// Newznab quota; that's policy choice).
func (h *Handlers) resolveAPIUser(c *gin.Context) (*APIUser, bool) {
	key := c.Query("apikey")
	if key == "" {
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			key = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
	}
	if key == "" {
		deps.JSONError(c, http.StatusUnauthorized, "missing apikey")
		return nil, false
	}
	user := deps.ResolveAPIKey(c.Request.Context(), key)
	if user == nil {
		deps.JSONError(c, http.StatusUnauthorized, "invalid apikey")
		return nil, false
	}
	return user, true
}

// ───────────────────────────────────────────────────────────────
// A. Python-script API
// ───────────────────────────────────────────────────────────────

// POST /api/offers/hash-check — request: {"hashes": ["...", ...]}
// response: {"known": ["..."]} subset of the input that already
// exists in offer_buckets. Lets the script avoid shipping metadata
// for hashes the site already knows about.
func (h *Handlers) HashCheck(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	_ = user
	var body struct {
		Hashes []string `json:"hashes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad json")
		return
	}
	if len(body.Hashes) > 5000 {
		deps.JSONError(c, http.StatusBadRequest, "too many hashes (max 5000)")
		return
	}
	found, err := deps.KnownHashes(c.Request.Context(), body.Hashes)
	if err != nil {
		deps.ReportError(c, "offer-api/hash-check", err)
		return
	}
	deps.JSONOK(c, gin.H{"known": found})
}

// POST /api/offers/register — request:
//
//	{
//	  "tracker_short_name": "ab",
//	  "offers": [
//	    {"entity_type":"anime","entity_id":18482,"season":1,"episode":3,
//	     "resolution":"1080p","source_tag":"web-dl","size_bucket":"<1GB",
//	     "points":0},
//	    ...
//	  ]
//	}
//
// Server computes the hash, upserts the bucket, attaches the user/
// tracker pair. Multiple registers for the same hash bump
// last_seen_at — the Python script just re-syncs on each run.
func (h *Handlers) Register(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	var body struct {
		TrackerShortName string `json:"tracker_short_name"`
		Offers           []struct {
			EntityType string `json:"entity_type"`
			EntityID   int    `json:"entity_id"`
			Season     int    `json:"season"`
			Episode    int    `json:"episode"`
			Resolution string `json:"resolution"`
			SourceTag  string `json:"source_tag"`
			SizeBucket string `json:"size_bucket"`
			Points     int    `json:"points"`
			// InfoHash is optional. Tracker scrapers ship it so the
			// fulfillment loop can auto-poll /api/nzbs/by-info-hash;
			// folder scanners leave it empty.
			InfoHash string `json:"info_hash"`
		} `json:"offers"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad json")
		return
	}
	if len(body.Offers) == 0 || len(body.Offers) > 2000 {
		deps.JSONError(c, http.StatusBadRequest, "offers list must be 1..2000")
		return
	}
	ctx := c.Request.Context()
	tracker, err := h.lookupTrackerByShortName(ctx, strings.TrimSpace(body.TrackerShortName))
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, err.Error())
		return
	}
	// Authorisation: user must have access to this tracker. Personal
	// is exempt — the existence of a personal-scan upload is itself
	// the access claim.
	if tracker.Visibility != VisibilityPersonal {
		ok, _ := deps.UserHasTracker(ctx, user.ID, tracker.ID)
		if !ok {
			deps.JSONError(c, http.StatusForbidden, "you don't have access to this tracker — add it on /account-settings first")
			return
		}
	} else {
		// Personal auto-attach so the user can immediately use it.
		_ = deps.GrantTracker(ctx, user.ID, tracker.ID)
	}

	accepted := 0
	bucketIDs := make([]int, 0, len(body.Offers))
	for _, o := range body.Offers {
		if !deps.EntityTypeAllowed(o.EntityType) {
			continue
		}
		if !deps.SizeBucketAllowed(o.SizeBucket) {
			continue
		}
		hash := deps.ComputeOfferHash(o.EntityType, o.EntityID, o.Season, o.Episode, o.Resolution, o.SourceTag)
		var entityID *int
		if o.EntityID > 0 {
			eid := o.EntityID
			entityID = &eid
		}
		var season, episode *int
		if o.Season > 0 {
			s := o.Season
			season = &s
		}
		if o.Episode > 0 {
			e := o.Episode
			episode = &e
		}
		bucketID, err := deps.UpsertBucket(ctx, BucketInput{
			OfferHash:  hash,
			EntityType: o.EntityType,
			EntityID:   entityID,
			SeasonNum:  season,
			EpisodeNum: episode,
			Resolution: strings.ToLower(strings.TrimSpace(o.Resolution)),
			SourceTag:  strings.ToLower(strings.TrimSpace(o.SourceTag)),
			SizeBucket: o.SizeBucket,
		})
		if err != nil {
			continue
		}
		if err := deps.UpsertOffer(ctx, OfferInput{
			BucketID:       bucketID,
			UserID:         user.ID,
			TrackerID:      tracker.ID,
			PointsRequired: o.Points,
			InfoHash:       o.InfoHash,
		}); err != nil {
			continue
		}
		bucketIDs = append(bucketIDs, bucketID)
		accepted++
	}
	_ = deps.Heartbeat(ctx, user.ID, bucketIDs)
	deps.JSONOK(c, gin.H{"accepted": accepted, "submitted": len(body.Offers)})
}

// POST /api/offers/heartbeat — request: {"bucket_ids": [...]}
// re-stamps last_seen_at without re-uploading metadata. Cheap weekly
// keep-alive for stable catalogs.
func (h *Handlers) Heartbeat(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	var body struct {
		BucketIDs []int `json:"bucket_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad json")
		return
	}
	if err := deps.Heartbeat(c.Request.Context(), user.ID, body.BucketIDs); err != nil {
		deps.ReportError(c, "offer-api/heartbeat", err)
		return
	}
	deps.JSONOK(c, gin.H{"count": len(body.BucketIDs)})
}

// GET /api/offers/requests/pending — returns open/expired requests
// the calling user could fulfill (joined on bucket via their offers).
// Python script polls this on a slow interval (default 60s).
func (h *Handlers) PendingRequests(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	rows, err := deps.PendingForUser(c.Request.Context(), user.ID)
	if err != nil {
		deps.ReportError(c, "offer-api/pending", err)
		return
	}
	deps.JSONOK(c, gin.H{"requests": rows})
}

// POST /api/offers/requests/:id/claim — body: {"offer_id": N}
// Optimistic lock. 200 + {"claimed": true} on success.
func (h *Handlers) ClaimRequest(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	reqID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		OfferID int `json:"offer_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad json")
		return
	}
	got, err := deps.ClaimRequest(c.Request.Context(), reqID, user.ID, body.OfferID, 15*time.Minute)
	if err != nil {
		deps.ReportError(c, "offer-api/claim", err)
		return
	}
	// Guarded on the field this actually CALLS, not on NotifyRequest. Deps
	// permits a host to wire some notifiers and not others, and the call below
	// is in a bare goroutine -- a nil func there panics with nothing to recover
	// it and takes the whole web process down. jobs.go:66 has the right shape.
	if got && deps.NotifyClaimed != nil {
		// Fire-and-forget: notify the requester their request was
		// picked up. Background ctx since the HTTP request will
		// return before this completes.
		go func(rid int) {
			bg := context.Background()
			requester, _ := deps.RequesterOf(bg, rid)
			deps.NotifyClaimed(bg, requester, rid)
		}(reqID)
	}
	deps.JSONOK(c, gin.H{"claimed": got})
}

// POST /api/offers/requests/:id/deliver — body: {"nzb_id": N}
// Closes the request. Bumps the offer's fulfilled_count.
func (h *Handlers) DeliverRequest(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	reqID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		NzbID int64 `json:"nzb_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.NzbID <= 0 {
		deps.JSONError(c, http.StatusBadRequest, "nzb_id required")
		return
	}
	ok2, err := deps.DeliverRequest(c.Request.Context(), reqID, user.ID, body.NzbID)
	if err != nil {
		deps.ReportError(c, "offer-api/deliver", err)
		return
	}
	// Only on a real delivery. ok2 false means the claim had already been
	// served or released, and announcing it would credit an offerer twice for
	// one file.
	if ok2 {
		h.emit(c.Request.Context(), EventRequestDelivered, user.ID,
			RequestDelivered{RequestID: reqID, NzbID: body.NzbID})
	}
	if ok2 && deps.NotifyDelivered != nil {
		go func(rid int, nid int64) {
			bg := context.Background()
			requester, _ := deps.RequesterOf(bg, rid)
			deps.NotifyDelivered(bg, requester, rid, nid)
		}(reqID, body.NzbID)
	}
	deps.JSONOK(c, gin.H{"delivered": ok2})
}

// POST /api/offers/requests/:id/fail — releases the claim back to
// open and bumps failed_count on the offer.
//
// Scoped to user.ID so a caller with a valid API key can't release
// another user's pending claim (which would bump the wrong offerer's
// failed_count and steal the request out from under them). 404 on
// mismatch so the caller can't probe whether someone else claimed it.
func (h *Handlers) FailRequest(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	reqID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, "bad id")
		return
	}
	// Look up the requester BEFORE FailOfferRequest nulls the claim
	// columns — the requester_user_id stays put, but pulling it
	// first keeps the notify path independent of the storage method's
	// internal state machine.
	requester, _ := deps.RequesterOf(c.Request.Context(), reqID)
	released, err := deps.FailRequest(c.Request.Context(), reqID, user.ID)
	if err != nil {
		deps.ReportError(c, "offer-api/fail", err)
		return
	}
	if !released {
		deps.JSONError(c, http.StatusNotFound, "not found")
		return
	}
	if deps.NotifyFailed != nil && requester > 0 {
		go deps.NotifyFailed(context.Background(), requester, reqID)
	}
	deps.JSONOK(c, nil)
}

// ───────────────────────────────────────────────────────────────
// B. Logged-in-user request creation
// ───────────────────────────────────────────────────────────────

// POST /offers/request — body: {"bucket_id": N, "points": 0, "notes": ""}
// Creates an open request. Returns the new id.
func (h *Handlers) UserCreateRequest(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		deps.JSONError(c, http.StatusUnauthorized, "login required")
		return
	}
	var body struct {
		BucketID int    `json:"bucket_id"`
		Points   int    `json:"points"`
		Notes    string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.BucketID <= 0 {
		deps.JSONError(c, http.StatusBadRequest, "bucket_id required")
		return
	}
	ctx := c.Request.Context()
	id, err := deps.CreateRequest(ctx, body.BucketID, user.ID, body.Points, body.Notes)
	if err != nil {
		deps.ReportError(c, "offer/request-create", err)
		return
	}
	// Fan-out notification to every active offerer attached to this
	// bucket. Best-effort, fire-and-forget so a slow notif write
	// can't gate the response. The recipient list excludes the
	// requester themselves (handled in OnOfferRequest).
	if deps.NotifyRequest != nil {
		go func(bucketID, requestID, requesterID int, name string) {
			bgCtx := context.Background()
			ids, err := deps.OfferersFor(bgCtx, bucketID)
			if err != nil {
				return
			}
			deps.NotifyRequest(bgCtx, ids, requesterID, name, bucketID, requestID)
		}(body.BucketID, id, user.ID, user.Username)
	}
	h.emit(ctx, EventRequestCreated, user.ID,
		RequestCreated{RequestID: id, BucketID: body.BucketID, Points: body.Points})
	deps.JSONOK(c, gin.H{"request_id": id})
}

// GET /api/offers/buckets/:id — returns the bucket fields (incl.
// hash) for one bucket. Lets the Python fulfillment loop translate a
// pending request's bucket_id back to its local file via the
// hash→path cache the sync step writes. Auth via API key.
func (h *Handlers) GetBucket(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	_ = user
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		deps.JSONError(c, http.StatusBadRequest, "bad id")
		return
	}
	row, err := deps.BucketByID(c.Request.Context(), id)
	if err != nil {
		deps.ReportError(c, "offer-api/bucket", err)
		return
	}
	if row == nil {
		deps.JSONError(c, http.StatusNotFound, "not found")
		return
	}
	deps.JSONOK(c, gin.H{"bucket": row})
}

// GET /api/offers/notifications/pending?since=<id>&limit=<n>
// Returns the user's recent notifications where the device channel
// is enabled, with id > since. The script keeps the largest returned
// id locally and asks for everything newer on the next poll. Empty
// payload {ok: true, notifications: []} is the happy path when no
// new device-targeted events fired since `since`.
func (h *Handlers) PendingDeviceNotifs(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := deps.PendingNotifs(c.Request.Context(), user.ID, since, limit)
	if err != nil {
		deps.ReportError(c, "offer-api/notif-pending", err)
		return
	}
	deps.JSONOK(c, gin.H{"notifications": rows})
}

// GET /api/nzbs/by-info-hash?h=<sha1> — returns the nzb_id matching a
// BitTorrent info_hash. Used by the Python fulfillment loop to wait
// for the Go agent's upload to land in the catalog. 404 when no nzb
// matches yet — caller polls until it does.
func (h *Handlers) NzbByInfoHash(c *gin.Context) {
	user, ok := h.resolveAPIUser(c)
	if !ok {
		return
	}
	_ = user
	hash := strings.ToLower(strings.TrimSpace(c.Query("h")))
	if len(hash) != 40 {
		deps.JSONError(c, http.StatusBadRequest, "h must be a 40-char info_hash")
		return
	}
	id, err := deps.NzbIDByInfoHash(c.Request.Context(), hash)
	if err != nil {
		deps.ReportError(c, "offer-api/by-info-hash", err)
		return
	}
	if id == 0 {
		deps.JSONError(c, http.StatusNotFound, "no nzb yet")
		return
	}
	deps.JSONOK(c, gin.H{"nzb_id": id})
}

// ───────────────────────────────────────────────────────────────
// C. Search surface (logged-in user browses offer buckets)
// ───────────────────────────────────────────────────────────────

// CommunityPage renders /offers/community — the public leaderboard +
// recent activity strip + per-tracker stats. Drives discovery of the
// offer system for users who don't yet know what's available. All
// claimer identities on the recent-activity strip stay anonymous;
// only the leaderboard surfaces usernames (and that's opt-in by
// registering an offer in the first place).
// OffersPage renders the unified /offers surface — search + filters
// on the left, community panel (top deliverers, recent fulfillments,
// active trackers, contribute CTA) on the right. Combines the old
// /offers/search + /offers/community pages so a viewer sees both
// browsing context and social proof on a single load.
//
// Query params (preserved from the old SearchPage):
//
//	?type=anime|manga|music|movie  (default: anime)
//	?size=<500MB|<1GB|<2.5GB|>=2.5GB  (optional)
func (h *Handlers) OffersPage(c *gin.Context) {
	ctx := c.Request.Context()
	entityType := c.DefaultQuery("type", EntityAnime)
	if !deps.EntityTypeAllowed(entityType) {
		entityType = EntityAnime
	}
	sizeBucket := c.Query("size")

	buckets, err := deps.RecentBuckets(ctx, entityType, sizeBucket, 100)
	if err != nil {
		deps.LogError(ctx, "offer/page-buckets", err)
	}
	leaders, err := deps.Leaderboard(ctx, 25)
	if err != nil {
		deps.LogError(ctx, "offer/page-leaders", err)
	}
	recent, err := deps.RecentDeliveries(ctx, 20)
	if err != nil {
		deps.LogError(ctx, "offer/page-recent", err)
	}
	trackers, err := deps.TrackerStats(ctx)
	if err != nil {
		deps.LogError(ctx, "offer/page-trackers", err)
	}

	page(c, "Offers", "offers.html", gin.H{
		"PageTitle":  "Offers",
		"ActiveNav":  "community",
		"EntityType": entityType,
		"SizeBucket": sizeBucket,
		"Buckets":    buckets,
		"Leaders":    leaders,
		"Recent":     recent,
		"Trackers":   trackers,
	})
}

// CommunityPage + SearchPage are kept as 302 redirects to the new
// unified /offers so existing notifications + linked-from-templates
// URLs don't 404. New code should target /offers directly.
func (h *Handlers) CommunityPage(c *gin.Context) {
	c.Redirect(http.StatusFound, "/offers")
}
func (h *Handlers) SearchPage(c *gin.Context) {
	c.Redirect(http.StatusFound, "/offers"+func() string {
		if c.Request.URL.RawQuery != "" {
			return "?" + c.Request.URL.RawQuery
		}
		return ""
	}())
}

// ─── helpers ─────────────────────────────────────────────────────

func (h *Handlers) lookupTrackerByShortName(ctx context.Context, short string) (*Tracker, error) {
	if short == "" {
		return nil, errors.New("tracker_short_name required")
	}
	trackers, err := deps.ListTrackers(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, t := range trackers {
		if strings.EqualFold(t.ShortName, short) {
			if t.Status == StatusBanned {
				return nil, errors.New("tracker banned")
			}
			return &t, nil
		}
	}
	return nil, errors.New("unknown tracker")
}

// OfferDetailPage renders one bucket: GET /offers/b/:id.
//
// This is a release page for something nobody uploaded. The offer system's
// whole premise is that the bytes exist on someone else's disk, so a member
// deciding whether to spend points has none of the usual evidence — no
// screenshots, no mediainfo, not even a filename. The agent probes its staged
// files and reports codecs, duration, audio tracks and subtitle languages, and
// this is where that lands.
//
// There is no download button and there never will be. The action is a
// request, which is the only thing the site can honestly offer here.
func (h *Handlers) OfferDetailPage(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		deps.RenderError(c, http.StatusNotFound, "No such offer.")
		return
	}
	if deps.BucketDetail == nil {
		deps.RenderError(c, http.StatusNotFound, "No such offer.")
		return
	}
	ctx := c.Request.Context()
	bucket, files, err := deps.BucketDetail(ctx, id)
	if err != nil {
		deps.ReportError(c, "offer/detail", err)
		return
	}
	if bucket == nil {
		deps.RenderError(c, http.StatusNotFound, "No such offer.")
		return
	}
	page(c, offerDetailTitle(bucket), "offer_detail.html", gin.H{
		"PageTitle": offerDetailTitle(bucket),
		"ActiveNav": "community",
		"Bucket":    bucket,
		"Files":     files,
	})
}

// offerDetailTitle prefers the catalogue name and falls back to the bucket's
// own identity, so a bucket whose entity is unknown still gets a usable title
// rather than an empty tab.
func offerDetailTitle(b *Bucket) string {
	if b.Title != "" {
		return b.Title
	}
	if b.EntityID != nil {
		return fmt.Sprintf("%s #%d", b.EntityType, *b.EntityID)
	}
	return "Offer"
}
