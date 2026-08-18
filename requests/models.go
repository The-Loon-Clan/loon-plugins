package requests

import (
	"encoding/json"
	"fmt"
	"time"
)

// The board's vocabulary — mirrors of the host models that cross the seam.
//
// These are FULL mirrors, not the narrowed subsets the handlers alone would
// need, because the two embedded templates (2,100 lines between them) read
// far more fields than the Go code does, and a field a template reads that a
// DTO lacks is a render error at request time. Mirroring the whole struct
// once, with the host's wiring doing one field-for-field copy per type, is
// cheaper to keep right than a per-field inventory of two templates.
//
// The request tables stay host-owned (the agent fulfilment pipeline and the
// upload flows write the same rows), which is why these are DTOs over seams
// rather than a store of the plugin's own.

// Request is one community request row (host: nzb_requests).
type Request struct {
	ID         int64
	UserID     int
	Username   string
	Title      string
	AnimeID    *int
	MangaID    *int
	MusicID    *int64
	MalID      *int
	AnilistID  *int
	EntityKind *string
	EntityID   *int64
	TvdbID     *int
	TmdbID     *int
	ImdbID     *string
	Category   string
	Resolution string
	Source     string
	Season     string
	Episodes   string
	NyaaURL    string
	InfoHash   string
	SeedCount  int

	ScrapedFiles  string
	Notes         string
	NzbID         *int64
	VoteCount     int
	BoostCount    int
	PriorityScore int
	Priorities    []RequestPriority
	SizeBytes     *int64
	Fulfilled     bool
	FulfilledByID *int

	KeepPrivateAfterFulfill bool
	ResurrectionForNzbID    *int64
	ParkedUntil             *time.Time
	ParkedReason            string
	// Backlog. BackloggedAt non-nil means the request left the open queue
	// and now waits for a member to ask for it back; BacklogReason is what
	// was knowable when it was shelved — see BacklogReasons. RequeueCount
	// survives the round trip, so a request that stalled twice can be told
	// from one that stalled once.
	//
	// Parking is a DIFFERENT lifecycle and both can be set: parked means
	// "come back at a time", backlogged means "come back when asked".
	BackloggedAt  *time.Time
	BacklogReason string
	RequeueCount  int
	RemuxOption   string
	UpscaleOption string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BacklogReason describes one shelving cause to a member.
//
// The wording is the point. A request is not being dismissed — it left the
// active queue and can be pulled back — and the three cases carry genuinely
// different information, so collapsing them into one "old request" label
// would throw away the only thing the member can act on.
type BacklogReason struct {
	Slug  string
	Label string
	Hint  string
	// CSS is the Bootstrap colour role for the pill.
	CSS string
}

// BacklogReasons is the display table, ordered strongest-signal first. The
// slugs are the host's (migration 316); an unknown slug falls back to
// "stale", because a reason nobody can render is worse than a vague one.
var BacklogReasons = []BacklogReason{
	{Slug: "attempted", Label: "Tried, failed", CSS: "danger",
		Hint: "An agent picked this up and could not complete it — usually no seeders left, or the source went away."},
	{Slug: "available", Label: "May already exist", CSS: "success",
		Hint: "The title this names already has releases indexed here. It may not be the exact release you asked for."},
	{Slug: "stale", Label: "No movement", CSS: "secondary",
		Hint: "Open a long time with nothing else to say about it — nobody has attempted it and there is no match in the catalog."},
}

// BacklogLabel resolves a stored reason for display.
func BacklogLabel(slug string) BacklogReason {
	for _, r := range BacklogReasons {
		if r.Slug == slug {
			return r
		}
	}
	return BacklogReasons[len(BacklogReasons)-1]
}

// PriorityType is one configurable request-ordering type.
type PriorityType struct {
	ID        int
	Slug      string
	Label     string
	IconHTML  string
	Weight    int
	Mode      string // single_per_user | multiple | bot
	ShowCount bool
	SortOrder int
	Active    bool
}

// RequestPriority is a per-request count for one priority type.
type RequestPriority struct {
	TypeSlug  string
	Count     int
	IconHTML  string
	Label     string
	Weight    int
	ShowCount bool
	Mode      string
}

// LockWarning is one rule-violation countdown surfaced by an agent.
type LockWarning struct {
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Icon      string    `json:"icon"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestLock tracks which agent is working on a request.
type RequestLock struct {
	ID           int
	RequestID    int64
	AgentTokenID int
	Status       string
	Progress     string
	Speed        string
	Warnings     string // JSON array of LockWarning
	FailReason   string
	LockedAt     time.Time
	UpdatedAt    time.Time
	AgentName    string
	Username     string
}

// ParsedWarnings decodes the warnings blob for templates. A missing or
// invalid blob yields an empty slice — never an error, because warnings are
// advisory and a corrupt row should not break the page.
func (l *RequestLock) ParsedWarnings() []LockWarning {
	if l.Warnings == "" || l.Warnings == "[]" {
		return nil
	}
	var out []LockWarning
	if err := json.Unmarshal([]byte(l.Warnings), &out); err != nil {
		return nil
	}
	return out
}

// RequestAction is one community-proposed edit or deletion on a request.
type RequestAction struct {
	ID                   int64
	RequestID            int64
	ProposerID           int
	Action               string
	NewNyaaURL           *string
	NewInfoHash          *string
	NewSeedCount         *int
	NewNotes             *string
	DuplicateOfRequestID *int64
	Reason               string
	Status               string
	CreatedAt            time.Time
	ResolvedAt           *time.Time
	ResolvedBy           *int
	ResolvedVia          string
	ProposerUsername     string
	RequestTitle         string
}

// Anime mirrors the host's anime metadata row — the detail page renders
// cover art, titles and airing facts off it, and LookupAnime serializes a
// stable JSON subset.
type Anime struct {
	AID          int
	Title        string
	Type         string
	Description  string
	Picture      string
	Rating       string
	Episodes     int
	StartDate    string
	FetchedAt    time.Time
	RomajiTitle  string
	NativeTitle  string
	Status       string
	EndDate      string
	Runtime      int
	Genres       string
	MalID        *int
	AnilistID    *int
	CoverLarge   string
	Studios      string
	Source       string
	AverageScore int
	Format       string
	BannerURL    string
	TrailerURL   string
	TmdbID       *int
	TvdbID       *int
	ImdbID       *string
}

// CoverURL returns the URL to use in HTML.
// Priority: local cached file → AniList CDN → AniDB CDN. Mirrors the host
// model's method — the detail template calls it.
func (a *Anime) CoverURL() string {
	if a.AID != 0 {
		return fmt.Sprintf("/static/covers/%d.jpg", a.AID)
	}
	if a.CoverLarge != "" {
		return a.CoverLarge
	}
	if a.Picture != "" {
		return "https://cdn.anidb.net/images/main/" + a.Picture
	}
	return ""
}

// FeedItem is one row of the host's feed_items log, as the Feed tab renders
// it (LEFT-JOINed status fields included).
type FeedItem struct {
	ID               int64
	Source           string
	Sources          []string
	Title            string
	InfoHash         string
	MagnetURL        string
	SourceURL        string
	SizeBytes        int64
	Seeders          int
	Category         string
	Status           string
	RequestID        *int64
	SeenAt           time.Time
	RequestFulfilled *bool
	RequestNzbID     *int64
	MatchedNzbID     *int64
	AnimeID          *int
}

// FeedItemFilter scopes the Feed tab listing.
type FeedItemFilter struct {
	Source string
	Status string
	Q      string
	Limit  int
	Offset int
}

// NzbInfo is the two fields the board reads off a catalog release.
type NzbInfo struct {
	Title    string
	Filename string
}

// ExistingRelease is one catalog release line for the create form's
// "this may already exist" card — enough to render a link with badges.
// Dead is carried rather than filtered: a dead-only episode is a
// legitimate request, so the card flags dead copies instead of hiding
// them.
type ExistingRelease struct {
	ID         int64
	Title      string
	Season     *int
	Episode    *int
	Resolution string
	Source     string
	Size       int64
	CreatedAt  time.Time
	Dead       bool
}

// UpscaleOption is the display data for one AI-upscale model key.
type UpscaleOption struct {
	Key     string
	Label   string
	Content string
	Scale   int
}

// Viewer is who is looking at the page. Nil means anonymous. Contributor and
// Mod are the two authority levels the board's handlers gate on; both are
// "at least" (a mod is also a contributor), and what maps to them is the
// HOST's decision — a plugin that hard-codes role numbers has quietly
// decided something the operator owns.
type Viewer struct {
	ID          int
	Username    string
	Points      int
	Contributor bool
	Mod         bool
}

// ProwlarrResult is one Prowlarr search hit. The JSON tags are load-bearing:
// SearchNekoBT serializes these RAW to the calendar page's JS, so the tags
// must match the host's historical wire format (camelCase) byte for byte.
type ProwlarrResult struct {
	Title       string             `json:"title"`
	Size        int64              `json:"size"`
	Seeders     int                `json:"seeders"`
	Leechers    int                `json:"leechers"`
	Grabs       int                `json:"grabs"`
	InfoHash    string             `json:"infoHash"`
	MagnetURL   string             `json:"magnetUrl"`
	DownloadURL string             `json:"downloadUrl"`
	InfoURL     string             `json:"infoUrl"`
	Indexer     string             `json:"indexer"`
	IndexerID   int                `json:"indexerId"`
	PublishDate string             `json:"publishDate"`
	Protocol    string             `json:"protocol"`
	Categories  []ProwlarrCategory `json:"categories"`
}

// ProwlarrCategory is a Newznab-style category from Prowlarr.
type ProwlarrCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// SizeStr returns a human-readable size string.
func (r *ProwlarrResult) SizeStr() string {
	switch {
	case r.Size >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(r.Size)/float64(1<<30))
	case r.Size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(r.Size)/float64(1<<20))
	case r.Size >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(r.Size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", r.Size)
	}
}
