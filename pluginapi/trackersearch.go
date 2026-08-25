package pluginapi

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// TrackerSearchName is where the external-tracker search client is published.
//
// EXTERNAL trackers -- the ones in trackerdir -- not this site's own tracker
// plugin, which is what every other `tracker.*` key on the registry belongs
// to. The plural in the key is that distinction, deliberately: search.torznab
// answers what THIS site has as torrents, trackers.search asks the rest of
// the world what it has that this site does not.
//
// This is the third piece of the content pipeline. tv.schedule knows an
// episode aired, tv.gaps knows the index has nothing matching it, and this
// answers where a copy might come from -- which is the input the auto-request
// trigger needs to decide a request is actually fillable before filing it.
const TrackerSearchName = "trackers.search"

// EpisodeSearch is one question: this show, this season, this episode.
type EpisodeSearch struct {
	ShowTitle string
	Season    int
	Episode   int
	// External ids, empty when unknown. An adapter uses the strongest one it
	// speaks: an id search skips title matching entirely, which is the whole
	// failure mode of asking for text. TVMazeID is first-class because the
	// schedule provider IS TVmaze -- a gap already carries it, no mapping.
	IMDbID   string // "tt1234567"
	TVMazeID string
	TVDBID   string
}

// TrackerCandidate is one release somebody out there claims to have.
type TrackerCandidate struct {
	// TrackerSlug is the trackerdir slug of the source that ANSWERED.
	TrackerSlug string
	// Via names the origin when the answering source is an aggregator that
	// caches other trackers ("The Pirate Bay" via knaben). Empty otherwise.
	Via       string
	Title     string
	SizeBytes int64
	Seeders   int
	Leechers  int
	InfoHash  string
	Magnet    string
	// DownloadURL is an authenticated .torrent URL, the way a PRIVATE tracker
	// hands over a file: public sources give a magnet, a UNIT3D tracker gives
	// a link that carries the member's key. Empty for magnet sources; a
	// consumer prefers Magnet when set and falls back to this.
	DownloadURL string
	// PageURL is the release's page on the source, or "".
	PageURL string
	// PostedAt is zero when the source does not say.
	PostedAt time.Time
}

// TrackerSearcher asks the wired external sources, politely, in parallel.
type TrackerSearcher interface {
	// SearchEpisode returns candidates best-first (healthiest swarm leading).
	//
	// One source failing does NOT fail the search: the answer "two trackers
	// have it, a third did not respond" is useful and common, and a caller
	// who needs the failures reads them from Sources. An error here means
	// the search as a whole could not run.
	SearchEpisode(ctx context.Context, q EpisodeSearch) ([]TrackerCandidate, error)

	// Sources reports what was wired and how the LAST search went per
	// source, so an operator page can say "asked 3, heard from 2" instead
	// of presenting silence as absence.
	Sources() []TrackerSource
}

// TrackerSource is one wired source's identity and last-known health.
type TrackerSource struct {
	Slug string
	// LastErr is "" when the most recent use succeeded (or it has not been
	// used yet); otherwise the failure, so absence of candidates can be told
	// apart from failure to ask.
	LastErr string
}

// LookupTrackerSearch resolves the searcher, or false when nothing publishes
// one -- an ordinary state on a host that has not wired external search.
func LookupTrackerSearch(c *core.Core) (TrackerSearcher, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(TrackerSearchName)
	if !ok {
		return nil, false
	}
	p, ok := v.(TrackerSearcher)
	return p, ok
}
