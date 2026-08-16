package requests

import (
	"context"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The board is a host-data plugin: every table it touches (nzb_requests,
// request_votes/priorities/locks/actions, feed_items, the anime catalog, the
// NZB catalog) is host-owned and written by the host's agent-fulfilment
// pipeline and upload flows too. So the stores cross as interfaces the host
// adapts, chrome crosses as render functions, and site vocabulary crosses as
// functions — one copy of every rule, on the side that owns it.
//
// Interface METHOD NAMES deliberately match the host repositories they adapt.
// This was a lift, and identical names kept the 1,600-line handler port a
// type-level change instead of a rename sweep; they are the contract now.

// RequestStore is the board's slice of the host's request repository.
type RequestStore interface {
	GetOpenNzbRequests(ctx context.Context, limit, offset int) ([]*Request, int, error)
	GetRequestsForAnime(ctx context.Context, aid int) ([]*Request, error)
	GetNzbRequestByID(ctx context.Context, id int64) (*Request, error)
	// CreateNzbRequest returns the persisted row with server-stamped fields.
	CreateNzbRequest(ctx context.Context, req *Request) (*Request, error)
	UpdateNzbRequest(ctx context.Context, req *Request) error
	// DeleteNzbRequest is owner-scoped unless deleteAny (the host maps that
	// to its role semantics; here it just means "skip the owner WHERE").
	DeleteNzbRequest(ctx context.Context, id int64, userID int, deleteAny bool) error
	BulkDeleteNzbRequests(ctx context.Context, ids []int64) (int, error)
	FulfillNzbRequest(ctx context.Context, id int64, userID int, nzbID *int64) error
	UnparkRequest(ctx context.Context, id int64) error
	GetRequestByInfoHash(ctx context.Context, infoHash string) (*Request, error)
	GetSoftDeletedRequestByInfoHash(ctx context.Context, infoHash string) (*Request, error)
	ReviveNzbRequest(ctx context.Context, id int64) (*Request, error)
	HasVotedForRequest(ctx context.Context, id int64, userID int) (bool, error)
	VoteForRequest(ctx context.Context, id int64, userID int) (int, bool, error)
	BoostRequest(ctx context.Context, id int64) error
	IncrementRequestPriority(ctx context.Context, id int64, typeSlug string) error
	GetRequestPriorities(ctx context.Context, id int64) ([]RequestPriority, error)
	GetRequestPrioritiesBatch(ctx context.Context, ids []int64) (map[int64][]RequestPriority, error)
	GetRequestQueuePosition(ctx context.Context, id int64) (int, error)
	GetActiveLockForRequest(ctx context.Context, id int64) (*RequestLock, error)
	GetLastFailedLockForRequest(ctx context.Context, id int64) (*RequestLock, error)
	GetAgentQueueRequestIDs(ctx context.Context, userID int) (map[int64]bool, error)
	GetRequestActionsForRequest(ctx context.Context, id int64) ([]*RequestAction, error)

	// Backlog: requests that stopped moving. The board only READS the
	// backlog and hands one request back when a member asks — the sweep
	// that puts them there is the host's, because it reads the host's
	// agent-lock and catalog tables to decide WHY each one stalled.
	GetBacklogRequests(ctx context.Context, limit, offset int) ([]*Request, int, error)
	// RequeueBacklogRequest reports false when the request was already back
	// in the queue, rather than failing: two members clicking at once is
	// normal, and the second must not see an error.
	RequeueBacklogRequest(ctx context.Context, id int64, userID int) (bool, error)
	// BacklogCounts is keyed by reason slug. The tab badge sums it.
	BacklogCounts(ctx context.Context) (map[string]int, error)
}

// AnimeStore resolves catalog identities, (nil, nil) on miss.
type AnimeStore interface {
	GetAnimeMetadata(ctx context.Context, aid int) (*Anime, error)
	GetAnimeByMalID(ctx context.Context, malID int) (*Anime, error)
	GetAnimeByAnilistID(ctx context.Context, anilistID int) (*Anime, error)
	GetAnimeByTvdbID(ctx context.Context, tvdbID int) (*Anime, error)
	GetAnimeByTmdbID(ctx context.Context, tmdbID int) (*Anime, error)
}

// NzbStore answers "is this hash in the catalog right now" — the board's
// single source of truth for duplicate detection and live release links.
type NzbStore interface {
	// GetNzbIDByInfoHash returns 0 for "not live" (deleted or absent).
	GetNzbIDByInfoHash(ctx context.Context, infoHash string) (int64, error)
	GetNzbByID(ctx context.Context, id int64) (*NzbInfo, error)
	// GetNzbHealthMeta returns the stored health verdict; the board only
	// compares the status against "dead".
	GetNzbHealthMeta(ctx context.Context, id int64) (lastCheckedAt *time.Time, healthStatus string, err error)
}

// FeedItemStore back-links feed rows to the requests they became, and lists
// the Feed tab.
type FeedItemStore interface {
	ListFeedItems(ctx context.Context, f FeedItemFilter) ([]*FeedItem, int, error)
	LinkFeedItemRequestByInfoHash(ctx context.Context, infoHash string, requestID int64) error
}

// AgentLockStore clears failed locks so a request can be retried.
type AgentLockStore interface {
	ClearFailedLocks(ctx context.Context, requestID int64) error
}

// AgentTokenStore lists the union of AI-upscale models capable agents
// advertise.
type AgentTokenStore interface {
	ListAvailableUpscaleModels(ctx context.Context) ([]string, error)
}

// ProwlarrSearch is the optional Prowlarr fallback backend.
type ProwlarrSearch interface {
	Available() bool
	Search(ctx context.Context, query, categories string) ([]ProwlarrResult, error)
}

// Deps are the host seams. Everything is required unless marked optional;
// Provision refuses a partial wiring — a half-wired board would render some
// pages and blank others, which reads as a broken site rather than a missing
// call.
type Deps struct {
	Requests    RequestStore
	Anime       AnimeStore
	Nzbs        NzbStore
	FeedItems   FeedItemStore
	AgentLocks  AgentLockStore
	AgentTokens AgentTokenStore

	// RenderPage wraps a rendered fragment in the site chrome. Status
	// crosses too: a validation failure re-render saying 200 would lie to
	// every client.
	RenderPage func(c *gin.Context, status int, title string, body template.HTML)
	// RenderPagination renders the host's pager for a page the plugin has
	// already counted. Opaque HTML on purpose: the host's Pagination type
	// stays out of this package's vocabulary.
	RenderPagination func(page, totalPages int, baseURL string) template.HTML
	// NzbCardCSS is the shared release-card stylesheet the listing embeds.
	NzbCardCSS func() template.HTML
	// CSRFToken returns the request's CSRF token for the POST forms.
	CSRFToken func(c *gin.Context) string
	// Markdown renders + SANITISES member text (request notes). It must
	// cross rather than be copied: two allow-lists that disagree is a
	// stored-XSS bug waiting on whichever is laxer.
	Markdown func(s string) template.HTML

	// Viewer is who is looking, nil for anonymous. The host decides what
	// maps to Contributor/Mod.
	Viewer func(c *gin.Context) *Viewer

	// Site vocabulary.
	BlockedExtension func(filename string) bool
	// SanitizeHTML is the host's wiki sanitiser, applied to scraped
	// third-party page descriptions before they reach the form preview.
	SanitizeHTML   func(s string) string
	UpscaleOptions func(keys []string) []UpscaleOption
	PriorityTypes  func() []PriorityType
	BoostCost      func(ctx context.Context) int
	BoostPerGB     func(ctx context.Context) int

	// Optional backends — nil/unavailable degrades the feature, not the page.
	Prowlarr ProwlarrSearch
	Torznab  lpapi.TorznabSearch
	// RefreshAnime queues a metadata scrape after a request names an anime;
	// nil when the host has no scraper wired.
	RefreshAnime func(ctx context.Context, aid int) error
}

func (d *Deps) ok() bool {
	return d != nil &&
		d.Requests != nil && d.Anime != nil && d.Nzbs != nil &&
		d.FeedItems != nil && d.AgentLocks != nil && d.AgentTokens != nil &&
		d.RenderPage != nil && d.RenderPagination != nil && d.NzbCardCSS != nil &&
		d.CSRFToken != nil && d.Markdown != nil && d.Viewer != nil &&
		d.BlockedExtension != nil && d.SanitizeHTML != nil &&
		d.UpscaleOptions != nil && d.PriorityTypes != nil &&
		d.BoostCost != nil && d.BoostPerGB != nil
	// Prowlarr, Torznab, RefreshAnime are the deliberate optionals: each
	// degrades one feature (Prowlarr fallback, nekoBT search, cover-art
	// refresh) rather than the page.
}

// JobDeps is the worker side — the backlog sweep, and nothing else.
//
// Separate from Deps because the two halves run in different processes: a
// split-mode web process has the pages and must not run the loop, and a
// headless worker has the loop and no template stack. Requiring one struct
// would force each to satisfy the other's seams for no reason.
type JobDeps struct {
	// BacklogSweep shelves open requests older than the window and returns
	// how many moved. The host owns it because deciding WHY each one stalled
	// reads the host's agent-lock and catalog tables.
	BacklogSweep func(ctx context.Context, olderThanDays int) (int, error)
	// BacklogWindowDays is the operator's setting, read every tick so a change
	// takes effect without a restart. Nil falls back to the default; zero
	// disables the sweep.
	BacklogWindowDays func(ctx context.Context) int
	ReportError       func(ctx context.Context, op string, err error)
}

func (d *JobDeps) ok() bool {
	return d != nil && d.BacklogSweep != nil && d.ReportError != nil
}

var (
	deps    *Deps
	jobDeps *JobDeps
)

// SetDeps hands the plugin its host seams. Called once from the composition
// root before core.Boot.
func SetDeps(d Deps) { deps = &d }

// SetJobDeps hands the plugin its worker-side seams. Optional: a host that
// does not call it simply gets no sweep, and the board still serves.
func SetJobDeps(d JobDeps) { jobDeps = &d }
