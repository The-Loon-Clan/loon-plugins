package pluginapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Usenet capability contracts. The usenet plugin publishes these on the core
// extension registry (UsenetIndexName / UsenetAdminName); the host's pages look
// them up so the site queries the indexer without importing the plugin.

// Release is one assembled NZB in search/listing results.
type Release struct {
	ID         int64
	Title      string
	Size       int64
	Posted     time.Time
	Group      string
	Resolution string
	Source     string
	Codec      string
	Audio      string
	Language   string
	CategoryID int    // Newznab category id (from the catalog capability)
	Category   string // display name, resolved when the catalog is present
}

// GroupInfo is one watched newsgroup + how many NZBs it has produced.
type GroupInfo struct {
	Name   string
	Active bool
	NZBs   int64

	// Per-group tuning. RetentionDays 0 means "follow the site-wide crawl
	// depth"; LowPriority groups are crawled only after the normal ones.
	RetentionDays int
	ThrottleMs    int
	LowPriority   bool
}

// GroupStat is the crawl status of one active group.
type GroupStat struct {
	// Backbone this row's article numbers belong to. Coverage is only ever
	// meaningful within one backbone: two backbones number the same articles
	// differently, so their watermarks cannot be compared or merged.
	Backbone string
	Name              string
	NZBs              int
	Staged            int // articles waiting in staging (not yet assembled)
	LastCrawl         time.Time
	HighWatermark     int64     // highest article number crawled (forward position)
	HighWatermarkDate time.Time // posting date at the forward position
	BackWatermark     int64     // backfill position; walks down toward ServerLow
	BackWatermarkDate time.Time // posting date reached by backfill
	ServerLow         int64     // server's oldest retained article number
	ServerHigh        int64     // server's highest article number
	BackfillDone      bool      // backfill reached ServerLow or the retention horizon
}

// CoverageBar is the 3-segment coverage of a group over [ServerLow..ServerHigh],
// as percentages that sum to ~100 when Known. Back = still-to-backfill (below
// back_watermark), Have = indexed span, New = not-yet-fetched-forward (above
// high_watermark).
type CoverageBar struct {
	BackPct float64
	HavePct float64
	NewPct  float64
	Known   bool // false when the server span is unknown (never crawled)
}

// Coverage derives the coverage bar from the watermarks. Pure; the host page
// renders BackPct/HavePct/NewPct as a stacked bar.
func (g GroupStat) Coverage() CoverageBar {
	span := float64(g.ServerHigh - g.ServerLow)
	if span <= 0 {
		return CoverageBar{Known: false}
	}
	back := g.BackWatermark
	if back < g.ServerLow {
		back = g.ServerLow
	}
	if back > g.HighWatermark {
		back = g.HighWatermark
	}
	pct := func(n int64) float64 {
		if n < 0 {
			n = 0
		}
		return float64(n) / span * 100
	}
	return CoverageBar{
		BackPct: pct(back - g.ServerLow),
		HavePct: pct(g.HighWatermark - back),
		NewPct:  pct(g.ServerHigh - g.HighWatermark),
		Known:   true,
	}
}

// IndexStats summarizes what the indexer has pulled so far.
type IndexStats struct {
	TotalNZBs              int
	TotalStaged            int
	TotalBackfillRemaining int64       // sum of (back_watermark - server_low) over active groups still backfilling
	Groups                 []GroupStat // active groups
}

// Server is the NNTP server configuration the setup wizard edits.
type Server struct {
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
	Enabled  bool
	// Backbone names the upstream network this account resells. Providers on the
	// same backbone hand out the SAME article numbers, so they share crawl state;
	// different backbones share nothing numeric. Empty means "assume its own".
	// See usenet/PROVIDERS.md.
	Backbone string
}

// ReleaseFile is one file inside an assembled release (parsed from the NZB).
type ReleaseFile struct {
	Filename string
	Bytes    int64
	Segments int
}

// ReleaseDetail is a single release plus its poster and file list, for the
// release-detail page.
type ReleaseDetail struct {
	Release
	Poster string
	Files  []ReleaseFile
}

// UsenetIndex is the public read surface — registered in the web/all process.
type UsenetIndex interface {
	Search(ctx context.Context, query string, limit int) ([]Release, error)
	// Browse lists recent releases, optionally filtered to one group (empty = all).
	Browse(ctx context.Context, group string, limit int) ([]Release, error)
	// Feed lists recent releases filtered to one or more Newznab category ids
	// (empty = all), paginated. Returns the page plus the total match count.
	Feed(ctx context.Context, cats []int, limit, offset int) ([]Release, int, error)
	Groups(ctx context.Context) ([]GroupInfo, error)
	// NZB returns the decompressed .nzb bytes + a suggested download filename.
	NZB(ctx context.Context, id int64) (data []byte, filename string, err error)
	// ReleaseByID returns one release with its file list; ok is false if absent.
	ReleaseByID(ctx context.Context, id int64) (detail ReleaseDetail, ok bool, err error)
}

// UsenetAdmin is the setup-wizard surface — registered in the web/all process.
type UsenetAdmin interface {
	Server(ctx context.Context) (Server, error)
	SetServer(ctx context.Context, s Server) error
	TestConnect(ctx context.Context, s Server) error        // Dial + Auth + Quit
	FetchGroups(ctx context.Context) (added int, err error) // NNTP LIST -> insert inactive
	// AllGroups returns groups for the picker, active first. query filters by
	// name substring (case-insensitive); empty query returns the first `limit`.
	AllGroups(ctx context.Context, query string, limit int) ([]GroupInfo, error)
	GroupCount(ctx context.Context) (int, error)   // total groups fetched (for "showing N of M")
	Stats(ctx context.Context) (IndexStats, error) // crawl progress: totals + per-active-group status
	SetGroupActive(ctx context.Context, name string, active bool) error
	TriggerCrawl()    // fire the crawler job now
	TriggerBackfill() // fire the backfill job now
	// ResetBackfill re-arms a group's backfill from its high watermark downward.
	ResetBackfill(ctx context.Context, name string) error
}

// NewznabRequest is a parsed Newznab/Torznab API call. The host mounts /api +
// /rss, parses the query string into this, and hands it to the plugin — which
// owns the whole XML contract. BaseURL + APIKey let the plugin build the
// download links clients follow; the plugin does not itself authenticate
// (apikey validation is the host's concern — the demo runs open).
type NewznabRequest struct {
	Function   string // t= : caps | search | tvsearch | movie | rss | get | details
	Query      string // q=
	Categories []int  // cat= (comma-separated Newznab ids)
	Limit      int
	Offset     int
	ID         string // id= (get/details)
	BaseURL    string // host public base, e.g. http://localhost:8090
	Title      string // site title (caps/feed channel)
	APIKey     string // passed through into download links; plugin may ignore
}

// NewznabResult is a rendered API response (XML feed/caps, or NZB bytes for get).
// The short JSON tags let a host cache it directly with cache.SetJSON/GetJSON.
type NewznabResult struct {
	Body        []byte `json:"b"`
	ContentType string `json:"c"`
	Filename    string `json:"f"` // set for get → Content-Disposition
}

// UsenetNewznab is the Newznab/Torznab API surface — registered in web/all.
type UsenetNewznab interface {
	Newznab(ctx context.Context, req NewznabRequest) (NewznabResult, error)
}

const (
	UsenetIndexName   = "usenet.index"
	UsenetAdminName   = "usenet.admin"
	UsenetNewznabName = "usenet.newznab"
)

// NewznabCachePrefix namespaces every cached Newznab response. A worker clears
// this prefix (cache.PrefixDeleter) after an ingest to invalidate the search
// cache; the topic is pluginapi.EventIngested.
const NewznabCachePrefix = "newznab:v1:"

// NewznabCacheKey hashes the request fields that determine the response, so any
// tier caching Newznab results (the loon-api read tier, a web host) computes the
// SAME key and can share one Redis. BaseURL is excluded (constant per host);
// APIKey is INCLUDED because the plugin embeds it in download links, so two keys
// must not share an entry. t=get downloads should not be cached by the caller.
func NewznabCacheKey(r NewznabRequest) string {
	payload := struct {
		T, Q  string
		C     []int
		L, O  int
		ID, K string
	}{r.Function, r.Query, r.Categories, r.Limit, r.Offset, r.ID, r.APIKey}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return NewznabCachePrefix + hex.EncodeToString(sum[:16])
}
