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
	// Picture is the cover URL for the entity, and PictureFallback the remote
	// one to swap to when the local file 404s — the same two-step the site's
	// nzb-thumb partial uses, because a local cover that was never fetched is
	// common and a broken image is worse than a CDN hit.
	Picture          string
	PictureFallback  string
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
	// BackerCount is how many members are behind this bucket's live request,
	// 0 when nobody has asked. It is the demand signal the listing was
	// missing: an offerer scanning the page could see what they COULD give
	// and never what anyone WANTED.
	BackerCount int
	// PoolPoints is the escrowed stake behind that live request — already
	// debited, paid to whoever delivers. Rendered beside the count, because a
	// bounty the listing cannot show is a bounty that motivates nobody.
	PoolPoints int
	// Fulfilled says this bucket's most relevant request is delivered and
	// the delivered release is still retrievable — the host computes it,
	// since health vocabulary is the host's. Fulfilled buckets move to
	// their own tab (a delivered bucket in the open list reads as "nobody
	// has this yet", the opposite of the truth) and come back the moment
	// the release's articles die. DeliveredNzbID is where the tab links.
	Fulfilled      bool
	DeliveredNzbID *int64
	// FileCount is how many files back the bucket — the fact that tells
	// two lookalike rows apart when neither filename parsed a resolution:
	// "12 files" and "1 file" are different things to request.
	FileCount int
	// SampleFile is one representative filename. It is the identity a
	// requester actually reads: three same-show buckets rendered as bare
	// title rows told nobody that one was the 1080p episodes, one the
	// 720p OADs, and one a folder of menu PNGs.
	SampleFile string
}

// BucketGroup is one SHOW on the listing: its identity resolved host-side,
// and every qualifying variant bucket under it. Twenty variants of one show
// as twenty flat rows was noise; the group is the unit a member scans.
type BucketGroup struct {
	EntityType      string
	EntityID        int
	Title           string
	Picture         string
	PictureFallback string
	Buckets         []Bucket
	TotalFiles      int
}

// OfferedFile is one staged file behind a bucket, as the detail page shows it.
// Everything except Name and SizeBytes may be absent: a file the agent has not
// probed yet still belongs on the page, described by what little is known.
type OfferedFile struct {
	Name      string
	Path      string
	SizeBytes int64
	Probed    bool
	// Queued means a description has been ASKED FOR and has not arrived.
	// Distinct from not-probed, because "coming shortly" and "may be a
	// fortnight" are different promises to a member deciding whether to wait.
	Queued bool

	Duration    string
	Dimensions  string
	VideoCodec  string
	Container   string
	FrameRate   string
	BitrateKbps int
	AudioTracks []OfferedAudio
	Subtitles   []OfferedSubtitle
}

type OfferedAudio struct {
	Language string
	Codec    string
	Channels int
}

type OfferedSubtitle struct {
	Language string
	Forced   bool
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
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// RenderPagination returns the HOST's pagination partial, so /offers
	// pages with the same numbered buttons as /browse instead of a
	// hand-rolled Newer/Older pair. baseURL must end in '?' or '&'.
	RenderPagination func(page, totalPages int, baseURL string) template.HTML
	RenderError      func(c *gin.Context, code int, msg string)
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
	// RecentBucketGroups is the listing: a page of SHOWS (limit/offset
	// count entities, not buckets), each carrying every qualifying variant.
	// query filters by catalogue title, /browse-style — the host resolves
	// it against its own metadata, since the plugin has none to search.
	// shelf is 'open' or 'fulfilled': the tabs paginate over their OWN
	// populations, so the split happens in the host's query, not by
	// dividing a fetched page. Returns the page plus the shelf's total.
	RecentBucketGroups func(ctx context.Context, entityType, sizeBucket, query, shelf string, limit, offset int) ([]BucketGroup, int, error)
	// BucketDetail is one bucket plus the staged files behind it — the offer
	// detail page. Files carry the agent's probe (codecs, duration, audio and
	// subtitle tracks) so the page can describe a release nobody uploaded.
	BucketDetail func(ctx context.Context, bucketID int) (*Bucket, []OfferedFile, error)
	// PrioritiseBucketMedia is called when someone opens a detail page holding
	// undescribed files: the view IS the demand signal, and it moves those
	// files to the front of the offering agent's probe queue. Best-effort —
	// failing to raise a priority must never fail the page.
	PrioritiseBucketMedia func(ctx context.Context, bucketID int)
	Leaderboard           func(ctx context.Context, limit int) ([]Leader, error)
	RecentDeliveries      func(ctx context.Context, limit int) ([]Fulfillment, error)
	TrackerStats          func(ctx context.Context) ([]TrackerStat, error)
	ListTrackers          func(ctx context.Context, includeBanned bool) ([]Tracker, error)
	AdminRequests         func(ctx context.Context, limit int) ([]AdminRequest, error)
	AdminStatusCounts     func(ctx context.Context) ([]StatusCount, error)

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
	// CreateOrJoinRequest files a request for a bucket, or joins the live
	// one. A bucket has at most one live request because what is being asked
	// for is a FILE: two members wanting it is one job for an offerer, not
	// two identical downloads. The second member joins as a backer, and
	// `joined` is the only difference the UI has to speak to.
	//
	// notes is the member's free text, which the offerer reads before
	// deciding to take it -- the only prose on the whole exchange, so it
	// crosses rather than being dropped.
	// files scopes a NEW request to named files inside a folder bucket —
	// nil/empty asks for the whole bucket, as before. The host validates
	// names against the bucket's inventory; joining a live request ignores
	// the selection (one live request per bucket, its scope already set).
	CreateOrJoinRequest func(ctx context.Context, bucketID, userID, points int, notes string, files []string) (requestID int, joined bool, err error)
	// WithdrawBacking removes one member's stake and reports what to refund.
	// The last backer leaving cancels the request.
	WithdrawBacking func(ctx context.Context, reqID, userID int) (refund int, cancelled bool, err error)
	// SettleEscrow closes a request's pool EXACTLY ONCE, returning the total
	// and the stakes behind it. A second call returns zero and no error,
	// which is what makes a retried delivery callback safe to pay from.
	SettleEscrow func(ctx context.Context, reqID int) (pool int, backers []RequestBacker, err error)
	// RequestBackers is who is behind one request, for the detail page.
	RequestBackers func(ctx context.Context, reqID int) ([]RequestBacker, error)
	// BackerStats is bucket id -> demand on its live request (how many
	// members, and the points pool they have staked), batched because the
	// listing renders fifty rows and a per-row query would be fifty
	// statements. The pool travels WITH the count because the two were the
	// same query all along — and showing "2 wanting" while a 1000-point pool
	// rendered as "free" is the exact confusion this fixes.
	BackerStats func(ctx context.Context, bucketIDs []int) (map[int]BackerStat, error)
	// LatestRequestForBucket is the detail page's view of where a bucket's
	// most relevant request stands: the LIVE one when there is one, else the
	// most recent settled one. Nil when the bucket has never been requested.
	//
	// This is what stops a fulfilled request from simply vanishing — the page
	// used to reset to a bare Request button the moment delivery landed, as
	// if nothing had happened, with the release reachable from nowhere.
	LatestRequestForBucket func(ctx context.Context, bucketID int) (*RequestState, error)
	// NzbHealth reports a delivered release's health_status ('' when never
	// probed). Optional: without it the page shows the delivery and simply
	// cannot offer the health-based re-request.
	NzbHealth      func(ctx context.Context, nzbID int64) (string, error)
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

// RequestBacker is one member's stake in a request.
//
// Points here are ALREADY DEBITED from that member's balance — escrow, not a
// pledge. That is what makes the pool safe for an offerer to act on: the
// alternative is finding out after the download that the promise was never
// covered.
type RequestBacker struct {
	UserID   int
	Username string
	Points   int
}

// BackerStat is one bucket's live-request demand, as the listing shows it.
type BackerStat struct {
	Count int
	Pool  int
}

// RequestState is where a bucket's most relevant request stands, for the
// detail page. Statuses are the host's own row values (open / claimed /
// delivered / cancelled / expired).
type RequestState struct {
	RequestID   int
	Status      string
	NzbID       *int64
	DeliveredAt *time.Time
	PoolPoints  int
	BackerCount int
	// FileFilter is the request's file scope: empty means the whole
	// bucket, names mean only those files. Display-only here — the scope
	// is set at creation and joiners inherit it.
	FileFilter []string
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
		// SettleEscrow is required on the API half too: delivery is what pays
		// the pool out, and the api process is where delivery lands. A wiring
		// that omitted it would take points off members and never pay them.
		d.SettleEscrow != nil &&
		d.RequesterOf != nil && d.ListTrackers != nil
}

// okWeb adds the seams only the session-backed pages need.
func (d *Deps) okWeb() bool {
	return d.okAPI() &&
		d.RenderPage != nil && d.RenderError != nil && d.CSRFToken != nil &&
		d.RenderPagination != nil &&
		d.Viewer != nil &&
		d.RecentBucketGroups != nil && d.Leaderboard != nil && d.RecentDeliveries != nil &&
		d.TrackerStats != nil && d.AdminRequests != nil && d.AdminStatusCounts != nil &&
		d.CreateOrJoinRequest != nil && d.WithdrawBacking != nil &&
		d.RequestBackers != nil && d.BackerStats != nil &&
		d.LatestRequestForBucket != nil &&
		d.OfferersFor != nil &&
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
