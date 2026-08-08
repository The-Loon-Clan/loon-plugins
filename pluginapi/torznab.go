package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// TorznabSearch is an on-demand search against a Torznab indexer.
//
// Step one of extracting the RSS collector (docs/FEEDS-EXTRACTION.md in the
// site repo). That collector turned out to be two unrelated things sharing a
// file: a scheduled importer polling Nyaa/AniRena/Tokyo Toshokan, and this — a
// search client three other places call. Only the importer belongs in a feeds
// plugin; this is a capability its consumers need whoever provides it.
//
// Which is why it moves FIRST and on its own. The resurrector and the community
// handler currently hold a *services.TorrentFeedService, so extracting the
// importer would have meant either dragging them along or leaving the host
// reaching into plugin internals. Behind a contract, the implementation can move
// without them noticing.
//
// Published under TorznabSearchName. Consumers must treat absence as normal:
// the search is keyed on an operator-supplied API key, so "no key configured" is
// an ordinary state and not a failure.
type TorznabSearch interface {
	// Available reports whether a search can be made at all. Separate from
	// Search returning nothing, because the callers use it to decide whether to
	// OFFER the feature — a button that always returns no results is worse than
	// no button.
	Available() bool

	// Search runs a query. When season and episode are both > 0 the caller wants
	// one episode and the implementation should scope to it (the Torznab
	// t=tvsearch shape); otherwise it is a freeform search.
	//
	// Returns (nil, nil) when unavailable rather than an error, so a caller that
	// skipped Available gets "no results" instead of a failure to handle. That
	// is the existing host behaviour and it is deliberate: the resurrector runs
	// on a schedule and must not log an error every tick on a site that has
	// simply not configured a key.
	Search(ctx context.Context, query string, season, episode int) ([]TorznabResult, error)
}

// TorznabResult is one hit. Flat and JSON-tagged because it crosses the seam
// into templates and an ops endpoint; no methods, so neither side needs the
// other's types.
type TorznabResult struct {
	Title    string `json:"title"`
	Link     string `json:"link"`
	InfoHash string `json:"info_hash"`
	Seeders  int    `json:"seeders"`
	Size     int64  `json:"size"`
	Category string `json:"category"`
}

// TorznabSearchName is the registry key.
const TorznabSearchName = "search.torznab"

// LookupTorznabSearch resolves the capability, returning nil when nothing has
// published one. A nil return is an ordinary state — see the interface doc.
func LookupTorznabSearch(c *core.Core) (TorznabSearch, bool) {
	v, ok := c.Lookup(TorznabSearchName)
	if !ok {
		return nil, false
	}
	s, ok := v.(TorznabSearch)
	return s, ok
}
