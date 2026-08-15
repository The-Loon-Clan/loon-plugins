// Package offers is the offer system: members register what they can deliver
// (a private tracker, a personal collection), other members file "request
// this" against a bucket, and an external Python offer-agent claims and
// delivers through /api/offers/*.
//
// The offer TABLES stay host-owned. The agent fleet's /api/agent/offer/*
// surface works the same rows, so the host is a co-writer, not just a store.
package offers

import (
	"context"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"
)

// ── What the pages display ─────────────────────────────────────────────────
//
// These are the plugin's own view types. They exist because the three pages'
// markup lives here now; while it lived host-side it read the host's rows
// directly and nothing could be narrowed.

// Bucket is one offerable thing: a release at a specific quality.
type Bucket struct {
	BucketID   int
	EntityType string
	EntityID   *int
	// Title is the catalogue name — "Lady Lady!!" rather than "anime #2068".
	//
	// Resolved HOST-side and passed in, because the catalogue belongs to the
	// site: this plugin has no anime_metadata to join against and should not
	// acquire one. Empty when the entity is unknown, and the template falls
	// back to the type-and-id form so a missing row still renders something.
	Title string
	// Picture is a poster/cover path for the same entity, empty when absent.
	Picture          string
	SeasonNum        *int
	EpisodeNum       *int
	Resolution       string
	SourceTag        string
	SizeBucket       string
	OfferCount       int
	ActiveOfferCount int
	MinPoints        int
	HasPrivate       bool
	HasPublic        bool
	HasPersonal      bool
}

// Leader is one row of the fulfilment leaderboard.
type Leader struct {
	UserID          int
	Username        string
	AvatarPath      *string
	OfferCount      int
	FulfilledCount  int
	FailedCount     int
	LastFulfilledAt *time.Time
}

// Fulfillment is a recently delivered request, for the activity strip.
type Fulfillment struct {
	RequestID   int
	BucketID    int
	NzbID       *int64
	DeliveredAt time.Time
	EntityType  string
	EntityID    *int
	// Title is the catalogue name, resolved host-side. See Bucket.Title —
	// same contract, and the fulfillment list had the same problem: a
	// delivery that reads "anime #2068" tells a member nothing.
	Title      string
	SeasonNum  *int
	EpisodeNum *int
	Resolution string
	SourceTag  string
	SizeBucket string
}

// TrackerStat is one source's contribution.
type TrackerStat struct {
	TrackerID         int
	TrackerName       string
	TrackerVisibility string
	OfferCount        int
	UserCount         int
	DeliveriesWeek    int
}

// Tracker is a private-tracker catalog entry as the admin page edits it.
type Tracker struct {
	ID               int
	Name             string
	ShortName        string
	Visibility       string
	Status           string
	RulesMarkdown    string
	ScrapeMinSeconds int
	OfferCount       int
	AccessCount      int
}

// AdminRequest is one row of the admin oversight table.
type AdminRequest struct {
	RequestID         int
	BucketID          int
	Status            string
	PointsOffered     int
	NzbID             *int64
	CreatedAt         time.Time
	ClaimedAt         *time.Time
	ClaimExpiresAt    *time.Time
	DeliveredAt       *time.Time
	RequesterUserID   int
	RequesterUsername string
	ClaimerUserID     *int
	ClaimerUsername   *string
	TrackerName       *string
	// What was actually requested. The admin table prints these, and they
	// were missed on the first pass because the row type is 21 fields long
	// and the extraction was truncated — the render test is what caught it.
	EntityType string
	EntityID   *int
	SeasonNum  *int
	EpisodeNum *int
	Resolution string
	SourceTag  string
	SizeBucket string
}

// StatusCount is the admin page's status histogram.
type StatusCount struct {
	Status string
	Count  int
}

// ── What the plugin writes ─────────────────────────────────────────────────

// BucketInput and OfferInput are what a register call produces. Structs
// convert across a seam cleanly (unlike channels), so these stay the plugin's
// and the host maps them onto its own rows.
type BucketInput struct {
	OfferHash  string
	EntityType string
	EntityID   *int
	SeasonNum  *int
	EpisodeNum *int
	Resolution string
	SourceTag  string
	SizeBucket string
}

type OfferInput struct {
	BucketID       int
	UserID         int
	TrackerID      int
	PointsRequired int
	InfoHash       string
}

// TrackerInput is an admin create/update. ID zero means create.
type TrackerInput struct {
	ID               int
	Name             string
	ShortName        string
	Visibility       string
	Status           string
	RulesMarkdown    string
	ScrapeMinSeconds int
}

// Viewer is the signed-in member. IsMod/IsAdmin are answered by the HOST:
// what counts as staff is an operator's decision, and a plugin that hard-codes
// a role ordering has quietly taken it.
type Viewer struct {
	ID       int
	Username string
	IsMod    bool
	IsAdmin  bool
}

// APIUser is the holder of an API key on the external agent surface.
type APIUser struct {
	ID       int
	Username string
}

// Deps carries everything the plugin cannot do for itself.
type Deps struct {
	// ── page chrome ──
	RenderPage  func(c *gin.Context, title string, body template.HTML)
	RenderError func(c *gin.Context, code int, msg string)
	// CSRFToken for the admin tracker form. Host-owned because the token is
	// minted and validated by host middleware.
	CSRFToken func(c *gin.Context) string

	// ── identity ──
	Viewer func(c *gin.Context) *Viewer
	// ResolveAPIKey authenticates the external offer-agent. Returns nil when
	// the key is missing, unknown, or the account is suspended — the host
	// owns "suspended", since it owns the role ordering.
	ResolveAPIKey func(ctx context.Context, key string) *APIUser

	// ── offer identity, shared with the host ──
	//
	// ComputeOfferHash is NOT reimplemented here, and that is deliberate.
	// The host's own /api/agent/offer/* handler computes the same hash to
	// find the same bucket. Two copies that ever drifted would quietly
	// create duplicate buckets for identical content, and nothing would
	// report it — so there is exactly one implementation and both callers
	// use it.
	ComputeOfferHash  func(entityType string, entityID, season, episode int, resolution, sourceTag string) string
	EntityTypeAllowed func(string) bool
	SizeBucketAllowed func(string) bool

	// ── responses ──
	JSONOK    func(c *gin.Context, extras gin.H)
	JSONError func(c *gin.Context, code int, msg string)
	// ReportError records a 500 against the host's error sink and answers the
	// client. op is a stable label.
	ReportError func(c *gin.Context, op string, err error)
	// LogError records a failure WITHOUT answering the client. The offer pages
	// degrade rather than 500 -- a leaderboard that fails to load should not
	// take the page with it -- so those failures need somewhere to go that is
	// not the response.
	LogError func(ctx context.Context, op string, err error)

	// ── reads for the pages ──
	RecentBuckets     func(ctx context.Context, entityType, sizeBucket string, limit int) ([]Bucket, error)
	Leaderboard       func(ctx context.Context, limit int) ([]Leader, error)
	RecentDeliveries  func(ctx context.Context, limit int) ([]Fulfillment, error)
	TrackerStats      func(ctx context.Context) ([]TrackerStat, error)
	ListTrackers      func(ctx context.Context, includeBanned bool) ([]Tracker, error)
	AdminRequests     func(ctx context.Context, limit int) ([]AdminRequest, error)
	AdminStatusCounts func(ctx context.Context) ([]StatusCount, error)

	// ── reads for the external API ──
	//
	// Typed `any` on purpose. These payloads are consumed by Python agents
	// already deployed in the wild; re-describing them with plugin structs
	// would risk changing a JSON field name and breaking every agent
	// silently. The plugin forwards them to the encoder untouched.
	BucketByID      func(ctx context.Context, id int) (any, error)
	PendingForUser  func(ctx context.Context, userID int) (any, error)
	PendingNotifs   func(ctx context.Context, userID int, sinceID int64, limit int) (any, error)
	KnownHashes     func(ctx context.Context, hashes []string) ([]string, error)
	NzbIDByInfoHash func(ctx context.Context, hash string) (int64, error)

	// ── writes ──
	UpsertBucket   func(ctx context.Context, b BucketInput) (bucketID int, err error)
	UpsertOffer    func(ctx context.Context, o OfferInput) error
	Heartbeat      func(ctx context.Context, userID int, bucketIDs []int) error
	UserHasTracker func(ctx context.Context, userID, trackerID int) (bool, error)
	GrantTracker   func(ctx context.Context, userID, trackerID int) error
	// notes is the member's free text on the request, which the offerer
	// reads before deciding to take it -- the only prose on the whole
	// exchange, so it crosses rather than being dropped.
	CreateRequest  func(ctx context.Context, bucketID, userID, points int, notes string) (int, error)
	ClaimRequest   func(ctx context.Context, reqID, userID, offerID int, window time.Duration) (bool, error)
	DeliverRequest func(ctx context.Context, reqID, userID int, nzbID int64) (bool, error)
	FailRequest    func(ctx context.Context, reqID, userID int) (released bool, err error)
	RequesterOf    func(ctx context.Context, reqID int) (int, error)
	OfferersFor    func(ctx context.Context, bucketID int) ([]int, error)

	SaveTracker   func(ctx context.Context, t TrackerInput) error
	DeleteTracker func(ctx context.Context, id int) error

	// ── notifications (optional; nil disables the ping) ──
	NotifyRequest   func(ctx context.Context, recipientIDs []int, requesterID int, requesterName string, bucketID, requestID int)
	NotifyClaimed   func(ctx context.Context, requesterID, requestID int)
	NotifyDelivered func(ctx context.Context, requesterID, requestID int, nzbID int64)
	NotifyFailed    func(ctx context.Context, requesterID, requestID int)
}

// ExpiredClaim is one request the sweeper reopened, and who was waiting on it.
type ExpiredClaim struct {
	RequestID       int
	RequesterUserID int
}

// JobDeps is the worker side — the claim sweeper and the stale-offer pruner.
type JobDeps struct {
	// ExpireStaleClaims reopens requests whose claim window elapsed and
	// returns them, so the requester can be told their request is live again.
	ExpireStaleClaims func(ctx context.Context) ([]ExpiredClaim, error)
	PruneStaleOffers  func(ctx context.Context, olderThan time.Duration) (int, error)
	ReportError       func(ctx context.Context, op string, err error)
	// NotifyFailed is optional: reopening the row is the canonical event, so a
	// missing notifier degrades the experience without losing the outcome.
	NotifyFailed func(ctx context.Context, requesterID, requestID int)
}

var (
	deps    *Deps
	jobDeps *JobDeps
)

// SetDeps hands the plugin its web/api-side dependencies.
func SetDeps(d Deps) { deps = &d }

// SetJobDeps hands the plugin its worker-side dependencies.
func SetJobDeps(d JobDeps) { jobDeps = &d }

// okAPI reports whether the external-API surface can run. The api process
// serves only that, so it must not require the page seams.
func (d *Deps) okAPI() bool {
	return d != nil &&
		d.ResolveAPIKey != nil && d.ComputeOfferHash != nil &&
		d.EntityTypeAllowed != nil && d.SizeBucketAllowed != nil &&
		d.JSONOK != nil && d.JSONError != nil && d.ReportError != nil &&
		d.LogError != nil &&
		d.BucketByID != nil && d.PendingForUser != nil && d.PendingNotifs != nil &&
		d.KnownHashes != nil && d.NzbIDByInfoHash != nil &&
		d.UpsertBucket != nil && d.UpsertOffer != nil && d.Heartbeat != nil &&
		d.UserHasTracker != nil && d.GrantTracker != nil &&
		d.ClaimRequest != nil && d.DeliverRequest != nil && d.FailRequest != nil &&
		d.RequesterOf != nil && d.ListTrackers != nil
}

// okWeb adds the seams only the session-backed pages need.
func (d *Deps) okWeb() bool {
	return d.okAPI() &&
		d.RenderPage != nil && d.RenderError != nil && d.CSRFToken != nil &&
		d.Viewer != nil &&
		d.RecentBuckets != nil && d.Leaderboard != nil && d.RecentDeliveries != nil &&
		d.TrackerStats != nil && d.AdminRequests != nil && d.AdminStatusCounts != nil &&
		d.CreateRequest != nil && d.OfferersFor != nil &&
		d.SaveTracker != nil && d.DeleteTracker != nil
}

func (d *JobDeps) ok() bool {
	return d != nil && d.ExpireStaleClaims != nil && d.PruneStaleOffers != nil && d.ReportError != nil
}

// The offer domain's vocabulary, duplicated here with the SAME string values
// the host stores.
//
// Duplicated rather than crossed the seam because these are compared against
// what is already in the database — a tracker row says "private", and both
// sides have to agree that means private. They are also the plugin's own
// domain language: an offer system that cannot name its own visibilities is
// not a plugin, it is a view. A drift here fails loudly and immediately (a
// visibility nothing matches renders nothing and saves nothing), which is the
// opposite of ComputeOfferHash, where a drift is silent — hence that one
// crosses as a function and these do not.
const (
	VisibilityPrivate  = "private"
	VisibilityPublic   = "public"
	VisibilityPersonal = "personal"

	StatusUnvetted = "unvetted"
	StatusActive   = "active"
	StatusBanned   = "banned"

	// VerificationHonor is what a personal-collection attachment records:
	// uploading a personal scan IS the access claim, so there is nothing to
	// verify against.
	VerificationHonor = "honor"

	EntityAnime = "anime"
)
