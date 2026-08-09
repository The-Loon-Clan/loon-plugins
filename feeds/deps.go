package feeds

import "context"

// The importer is a host-data worker: every table it touches (nzb_requests,
// feed_items, release_groups, search_terms, anime metadata) is host-owned and
// read by host pages, so it takes narrow function seams via SetJobDeps rather
// than owning a store. The DTOs below are the complete surface that crosses;
// the host's wiring file converts to and from its own storage/model types.
//
// Function fields rather than repository interfaces on purpose (the offers
// pattern): the host's methods return host structs, so an interface here would
// either drag those types across or need an adapter struct per repo — a
// closure per METHOD is the same adapter with less ceremony, and it keeps the
// measured method surface visible in one place.

// FeedItem is one observation for the host's feed_items table, which backs the
// public Feed tab. Status uses the Status* constants below.
type FeedItem struct {
	Source    string
	Title     string
	InfoHash  string
	SourceURL string
	SizeBytes int64
	Seeders   int
	Category  string
	Status    string
	RequestID *int64
}

// Status values for FeedItem — kept in sync with the host's
// storage.FeedItemStatus* constants, which its Feed-tab template and repo
// share. The strings are the contract; these constants exist so a typo is a
// compile error on this side too.
const (
	StatusObserved        = "" // observed only, no action taken
	StatusRequestCreated  = "request_created"
	StatusSkippedOld      = "skipped_old"
	StatusSkippedDupHash  = "skipped_dup_hash"
	StatusSkippedDupTitle = "skipped_dup_title"
)

// ReleaseRequest is one auto-created community request. PageURL lands in the
// host's nyaa_url column — historical name, it holds whatever source page the
// item came from.
type ReleaseRequest struct {
	UserID     int
	Username   string
	Title      string
	Category   string
	Resolution string
	Source     string
	Season     string
	Episodes   string
	PageURL    string
	InfoHash   string
	Seeders    int
	AnimeID    *int
	MalID      *int
	AnilistID  *int
	TvdbID     *int
	TmdbID     *int
	Notes      string
	SizeBytes  *int64
}

// AnimeRef is a catalog identity resolved from an external id. AID is the
// AniDB id (the host catalog's primary key); the pointers mirror the host's
// nullable columns.
type AnimeRef struct {
	AID       int
	MalID     *int
	AnilistID *int
}

// AiringEntry is one currently-airing show from the host's calendar cache.
// Title is raw; the plugin normalizes both sides of every comparison through
// NormalizeTitle so item titles and calendar titles meet in the same space.
type AiringEntry struct {
	AnilistID int
	AID       int
	Title     string
}

// FailedSearch is one popular member query that returned zero grabs — the
// top-searched discovery pass chases these.
type FailedSearch struct {
	Query string
	Count int
}

// GroupTorrent is one row for the release-group archive cross-write.
// ExternalID is the source-specific stable key ("nyaa-<view id>" or a nekoBT
// snowflake) the host upserts on.
type GroupTorrent struct {
	ExternalID string
	GroupID    int
	Title      string
	InfoHash   string
	SizeBytes  int64
	Seeders    int
}

// JobDeps are the host seams the importer needs. Staged before core.Boot in
// every process that runs jobs (worker, and "all"); Provision fails loud when
// the importer would run without them. Every field is required.
type JobDeps struct {
	// CreateRequest files one request and returns its id. A duplicate-key
	// failure crosses as an error whose text contains "duplicate" — the
	// importer treats that as an ordinary dedup outcome, not a fault.
	CreateRequest func(ctx context.Context, r ReleaseRequest) (int64, error)
	// BumpPriority increments a request's priority bucket ("new_release",
	// "top_searched").
	BumpPriority func(ctx context.Context, requestID int64, typeSlug string) error
	// RecentRequestKeys returns the dedup set: lowercased info_hashes and
	// lowercased titles of every request from the last `days` days.
	RecentRequestKeys func(ctx context.Context, days int) (map[string]bool, error)

	// UpsertFeedItem records one observation for the public Feed tab.
	UpsertFeedItem func(ctx context.Context, item FeedItem) error

	// Anime lookups, (nil, nil) on miss.
	AnimeByID        func(ctx context.Context, aid int) (*AnimeRef, error)
	AnimeByAnilistID func(ctx context.Context, anilistID int) (*AnimeRef, error)
	AnimeByTvdbID    func(ctx context.Context, tvdbID int) (*AnimeRef, error)
	AnimeByTmdbID    func(ctx context.Context, tmdbID int) (*AnimeRef, error)

	// FailedSearches returns popular zero-grab queries, search-count
	// descending.
	FailedSearches func(ctx context.Context, limit int) ([]FailedSearch, error)

	// AiringEntries is the current calendar snapshot. May be empty before the
	// calendar cache warms — the importer degrades to no airing boost.
	AiringEntries func() []AiringEntry

	// GroupIDBySlug resolves a release-group slug, ok=false on miss. The
	// importer never creates groups — admin-curated grouping is preserved.
	GroupIDBySlug func(ctx context.Context, slug string) (int, bool, error)
	// UpsertGroupTorrent mirrors an item into the group archive table.
	UpsertGroupTorrent func(ctx context.Context, t GroupTorrent) error

	// Site vocabulary, crossed as functions so there is exactly one copy of
	// each rule (the host's).
	NormalizeTitle   func(title string) string
	BlockedExtension func(filename string) bool
	SlugifyGroup     func(name string) string

	// BotUserID is the identity requests are filed under. Resolved lazily on
	// the first run (Provision may not do I/O) and cached once non-zero.
	BotUserID func(ctx context.Context) (int, error)
}

func (d *JobDeps) ok() bool {
	return d != nil &&
		d.CreateRequest != nil && d.BumpPriority != nil && d.RecentRequestKeys != nil &&
		d.UpsertFeedItem != nil &&
		d.AnimeByID != nil && d.AnimeByAnilistID != nil && d.AnimeByTvdbID != nil && d.AnimeByTmdbID != nil &&
		d.FailedSearches != nil && d.AiringEntries != nil &&
		d.GroupIDBySlug != nil && d.UpsertGroupTorrent != nil &&
		d.NormalizeTitle != nil && d.BlockedExtension != nil && d.SlugifyGroup != nil &&
		d.BotUserID != nil
}

var jobDeps *JobDeps

// SetJobDeps stages the host seams. Call once, before core.Boot, in every
// process whose Core will run the importer (worker, and single-process "all").
func SetJobDeps(d JobDeps) { jobDeps = &d }
