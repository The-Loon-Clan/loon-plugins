package pluginapi

import (
	"context"

	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/the-loon-clan/loon/core"
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
	// depth".
	RetentionDays int
	ThrottleMs    int
	// Tier is the crawl priority: "critical" (always first), "normal", or
	// "low" (only with capacity left over). A plain string rather than a
	// typed enum because this is the cross-package contract and the
	// authority is the plugin's own Tier type; empty reads as normal.
	Tier string
	// ResetArticles is how many articles a watermark reset would re-read for
	// this group: the span THIS crawler fetched, not the whole newsgroup. The
	// two differ by orders of magnitude on an adopted install — 10M against
	// 803M on prod's busiest group — so the confirm prompt states the number
	// rather than saying "everything already fetched" and letting the operator
	// guess which of the two it means. 0 = a reset is not available.
	ResetArticles int64
	// ResetHistoryArticles is what reopening the BACKFILL would queue: the
	// articles between server_low and the forward mark that no recorded range
	// covers. On an adopted install this is the span a previous crawler
	// claimed and may not have fully indexed, and it dwarfs the other number —
	// 793M against 10M on prod's busiest group — which is exactly why the two
	// are separate buttons with separate confirmations. 0 = unavailable.
	ResetHistoryArticles int64
}

// GroupStat is the crawl status of one active group.
type GroupStat struct {
	// Backbone this row's article numbers belong to. Coverage is only ever
	// meaningful within one backbone: two backbones number the same articles
	// differently, so their watermarks cannot be compared or merged.
	Backbone          string
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

// CrawlActivity is a sanitized snapshot of crawler liveness: counts only — no
// group names, no hostnames, no error text — so a host may serve it on a
// non-admin stats endpoint as-is. The JSON tags are the wire shape hosts can
// return directly. It describes ONE pass: the running crawl if there is one,
// else a running backfill, else the last finished crawl.
type CrawlActivity struct {
	InProgress bool `json:"in_progress"`
	Backfill   bool `json:"backfill"` // the numbers describe a backfill pass
	// UpdatedAt is the telemetry publish stamp — how fresh these numbers are.
	UpdatedAt time.Time `json:"updated_at"`
	Started   time.Time `json:"started"`
	// Groups/GroupsDone and Batches/BatchesTotal describe the current catch-up
	// ROUND, not the whole pass. A pass keeps going while the servers hold a
	// backlog — routinely dozens of rounds over many hours — and accumulating
	// these across rounds made the ratio meaningless: prod published
	// 542,460 / 520,000 batches, a denominator that was only ever "rounds so far
	// x the per-round budget" and grew for as long as the crawl ran.
	//
	// Round says which round it is, because "48% of round 27" and "48% of round
	// 1" say very different things about how far behind the crawler is.
	Round        int   `json:"round"`
	Groups       int   `json:"groups"`
	GroupsDone   int   `json:"groups_done"`
	Batches      int   `json:"batches"`
	BatchesTotal int   `json:"batches_total"`
	Articles     int   `json:"articles"` // overview lines fetched this pass
	Staged       int   `json:"staged"`   // kept after junk filtering + dedup
	WireBytes    int64 `json:"wire_bytes"`
}

// UsenetActivity is the public-safe liveness surface — registered in every
// process (web serves it; a worker-side cache job may snapshot it).
type UsenetActivity interface {
	Activity(ctx context.Context) (CrawlActivity, error)
}

const (
	UsenetIndexName    = "usenet.index"
	UsenetAdminName    = "usenet.admin"
	UsenetNewznabName  = "usenet.newznab"
	UsenetActivityName = "usenet.activity"
)

// LookupUsenetIndex / Admin / Newznab resolve the plugin-published read
// capabilities, mirroring LookupReleaseSink/LookupReleaseHealthStore so a host
// resolves every usenet capability the same typed way rather than hand-asserting
// the c.Lookup result.
func LookupUsenetIndex(c *core.Core) (UsenetIndex, bool) {
	v, ok := c.Lookup(UsenetIndexName)
	if !ok {
		return nil, false
	}
	s, ok := v.(UsenetIndex)
	return s, ok
}

func LookupUsenetAdmin(c *core.Core) (UsenetAdmin, bool) {
	v, ok := c.Lookup(UsenetAdminName)
	if !ok {
		return nil, false
	}
	s, ok := v.(UsenetAdmin)
	return s, ok
}

func LookupUsenetNewznab(c *core.Core) (UsenetNewznab, bool) {
	v, ok := c.Lookup(UsenetNewznabName)
	if !ok {
		return nil, false
	}
	s, ok := v.(UsenetNewznab)
	return s, ok
}

func LookupUsenetActivity(c *core.Core) (UsenetActivity, bool) {
	v, ok := c.Lookup(UsenetActivityName)
	if !ok {
		return nil, false
	}
	s, ok := v.(UsenetActivity)
	return s, ok
}

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

// ── release sink (the NZB handoff seam) ─────────────────────────────

// UsenetReleaseSinkName is the capability a HOST registers to take ownership of
// assembled releases. With the plugin's sink mode set to "host", the builder
// stops writing its own minimal nzbs table and hands each release here instead —
// which is how a rich host (prod's ~50-column NZB domain: anime matching,
// posters, release groups, media pipeline) adopts the crawler without the
// plugin knowing any of that exists.
const UsenetReleaseSinkName = "usenet.releasesink"

// AssembledRelease is one complete, filtered, ready-to-store release. Everything
// upstream of it — completeness, blocked extensions, the operator blacklist,
// junk filtering, category hinting — has already run in the plugin, mirroring
// where prod's assembler runs the same checks.
type AssembledRelease struct {
	Title  string // extracted + UTF-8-sanitised release title
	Group  string
	Poster string
	// ContentHash identifies the CONTENT: hex of sha256 over the sorted
	// segment message-ids (prod's scheme). Two posts of the same title with
	// different articles hash differently; the same articles always collide.
	// This is the dedup key — a sink SHOULD store it and use it to reject
	// duplicates (e.g. ON CONFLICT (content_hash) DO NOTHING).
	ContentHash string
	SizeBytes   int64
	PostedAt    time.Time // earliest article date; zero when unknown
	NZBGz       []byte    // gzipped NZB XML
	// CategoryHint is a category label the host maps into its own taxonomy;
	// "" = no hint. It is an ANIME-DOMAIN hint (scraping is anime-only by
	// design), drawn from a closed set: "Hentai" (adult terms in the title),
	// "Anime" (an OVA/ONA/OAD/Gekijouban marker), or "Manga" (a .cbz/.cbr in the
	// article filenames). It is a hint, not an id — the host decides what it
	// means, and a non-anime host may ignore it. (In internal-sink mode the
	// plugin ignores this field and categorises via its own catalog plugin.)
	CategoryHint string
	// BaseSubject and Segments are INFORMATIONAL — the plugin sets them (the raw
	// grouping key; the segment count) but neither sink path reads them. Present
	// for a host that wants them for diagnostics or a UI.
	BaseSubject string
	Segments    int
}

// ReleaseSink stores an assembled release in the host's NZB domain.
//
// Contract:
//   - success is (id > 0, true, nil) — the id is INFORMATIONAL (the current
//     plugin ignores it; return your row id or 0);
//   - (_, false, nil) means the release was a DUPLICATE — the plugin clears its
//     staging and moves on;
//   - a non-nil error means the host could not store it — the plugin leaves the
//     set staged and retries on a later pass, so a transient host failure never
//     loses a release. Dedup on ContentHash is what makes that retry safe.
type ReleaseSink interface {
	IngestAssembled(ctx context.Context, r AssembledRelease) (id int64, created bool, err error)
}

// LookupReleaseSink resolves the host-registered sink, if any.
func LookupReleaseSink(c *core.Core) (ReleaseSink, bool) {
	v, ok := c.Lookup(UsenetReleaseSinkName)
	if !ok {
		return nil, false
	}
	s, ok := v.(ReleaseSink)
	return s, ok
}

// ── release health store (the second half of host adoption) ─────────

// UsenetHealthStoreName is the capability a HOST registers so the plugin's
// health job sweeps the HOST'S catalogue. With sink=host the releases live in
// the host's NZB domain — without this seam nothing would health-check them:
// the plugin's checker only knows its own table, and the host's checker is
// exactly the code the adoption deletes.
const UsenetHealthStoreName = "usenet.healthstore"

// HealthCandidate is one release due a check.
type HealthCandidate struct {
	ID    int64
	NZBGz []byte // gzipped NZB XML
}

// ReleaseHealthStore is the host's side of health checking: pick candidates,
// record verdicts. The plugin owns everything between — the connection pool,
// STAT batching, PAR2 scoring, and the tri-state outcome that only writes a
// verdict it can trust (inconclusive rows are touched, never overwritten,
// which is what preserves a definitive prior label without the host having to
// expose it).
type ReleaseHealthStore interface {
	// HealthCandidates returns up to limit releases due a check. Ordering
	// (a SHOULD, because it changes which losses are found first): never-checked
	// rows first, then rows not checked within recheckDays; among never-checked
	// rows, OLDEST POSTED CONTENT first — old articles are the likeliest to have
	// expired, so checking them first surfaces real losses soonest. Releases
	// newer than minAgeHours are excluded (a propagation guard: a fresh upload
	// may not have reached every server yet, and STATting it would wrongly read
	// as missing).
	HealthCandidates(ctx context.Context, limit, recheckDays, minAgeHours int) ([]HealthCandidate, error)
	// SetHealthVerdict records a trustworthy verdict and stamps the checked-at
	// time. status is one of "healthy" | "broken" | "dead" — store it verbatim.
	// The three counts are, precisely:
	//   total   — data + PAR2 segments in the release
	//   missing — missing DATA segments only (PAR2 losses are excluded)
	//   par2    — TOTAL PAR2 segment count (not the missing count)
	SetHealthVerdict(ctx context.Context, id int64, status string, total, missing, par2 int) error
	// TouchHealthChecked stamps checked-at WITHOUT changing the verdict — used
	// for unreadable blobs so they stop jamming the queue head.
	TouchHealthChecked(ctx context.Context, id int64) error
}

// LookupReleaseHealthStore resolves the host-registered health store, if any.
func LookupReleaseHealthStore(c *core.Core) (ReleaseHealthStore, bool) {
	v, ok := c.Lookup(UsenetHealthStoreName)
	if !ok {
		return nil, false
	}
	s, ok := v.(ReleaseHealthStore)
	return s, ok
}

// UsenetCatalogStatsName is an OPTIONAL capability a host registers so the
// plugin's dashboard can show catalog totals and the health breakdown in
// sink=host mode — where the releases (and the verdicts the health job writes
// through ReleaseHealthStore) live in the host's domain, invisible to the
// plugin's own tables. A host should serve this from CACHED numbers: the
// dashboard renders per page view and must never trigger a catalog scan.
// Absent (internal-sink installs, hosts that skip it), the plugin falls back
// to its own tables.
const UsenetCatalogStatsName = "usenet.catalog-stats"

// HealthBreakdown is the catalog's health census. Counts, not percentages —
// the consumer derives shares so rounding stays in one place.
type HealthBreakdown struct {
	Untested int64 `json:"untested"`
	Healthy  int64 `json:"healthy"`
	Broken   int64 `json:"broken"`
	Dead     int64 `json:"dead"`
}

// CatalogStats is a point-in-time summary of the release catalog the sink
// writes into. Staleness is the host's choice (document it in the UI); an
// hourly cache is expected and fine.
type CatalogStats struct {
	Releases       int64           `json:"releases"`
	TotalSizeBytes int64           `json:"total_size_bytes"`
	Health         HealthBreakdown `json:"health"`
}

// CatalogStatsProvider is what the host registers under UsenetCatalogStatsName.
type CatalogStatsProvider interface {
	CatalogStats(ctx context.Context) (CatalogStats, error)
}

// LookupCatalogStats resolves the host-registered catalog stats, if any.
func LookupCatalogStats(c *core.Core) (CatalogStatsProvider, bool) {
	v, ok := c.Lookup(UsenetCatalogStatsName)
	if !ok {
		return nil, false
	}
	s, ok := v.(CatalogStatsProvider)
	return s, ok
}

// UsenetJunkSweepName is an OPTIONAL capability a host registers so the
// plugin's Filters tab can show the host's stored-catalogue junk-sweep
// attribution — which junk rule tagged how many ALREADY-INDEXED releases.
// Distinct from the plugin's own filter-hit counters (ingest-time drops):
// the sweep is the host-side safety net that catches what ingest missed, and
// surfacing both on one page is what tells the operator whether a rule works
// at ingest, only in the sweep, or not at all. Hosts without a sweep simply
// don't register it.
const UsenetJunkSweepName = "usenet.junk-sweep"

// JunkSweepStat is one junk rule's sweep attribution row.
type JunkSweepStat struct {
	Pattern    string    `json:"pattern"`
	Count      int64     `json:"count"`
	LastSample string    `json:"last_sample"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

// JunkSweepStatsProvider is what the host registers under UsenetJunkSweepName.
// Rows come back sorted by Count descending; the set is bounded by the host's
// distinct sweep patterns, so no pagination.
type JunkSweepStatsProvider interface {
	JunkSweepStats(ctx context.Context) ([]JunkSweepStat, error)
}

// LookupJunkSweepStats resolves the host-registered sweep counters, if any.
func LookupJunkSweepStats(c *core.Core) (JunkSweepStatsProvider, bool) {
	v, ok := c.Lookup(UsenetJunkSweepName)
	if !ok {
		return nil, false
	}
	s, ok := v.(JunkSweepStatsProvider)
	return s, ok
}
