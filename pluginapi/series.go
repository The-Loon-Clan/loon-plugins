// series.go is the index read that groups releases by the show they belong to.
//
// A release title says which episode of which show it is — 66% of a real
// index's do — and until that was parsed and stored, "every copy of Silo
// S03E07" and "everything in season 3" were questions the index held the
// answer to and could not be asked. This is the asking.
//
// A NEW contract rather than three more methods on UsenetIndex: that interface
// is implemented by the plugin and consumed by a host, and widening it breaks
// every implementer for a capability most hosts will not use. A host with no
// series index simply has no series pages — see SEAMS.md on absence being a
// normal state.
package pluginapi

import "context"

// SeriesIndexName is where the indexer publishes it.
const SeriesIndexName = "usenet.series"

// SeriesRow is one show in a list of shows.
type SeriesRow struct {
	// Key is the folded identity — lowercase, no punctuation — and what a URL
	// carries. Name is what a reader sees.
	Key      string
	Name     string
	Releases int
	Seasons  int
	// Latest is the newest release's posting time, so a list can lead with
	// what is actually moving rather than with whatever has the most rows.
	Latest string
}

// SeriesSeason is one season of one show, with how much is in it — the counts
// the season chips carry.
type SeriesSeason struct {
	Season   int
	Releases int
	// Episodes is how many DISTINCT episodes, which is the more useful figure
	// beside a release count: 612 releases of 10 episodes is a well-covered
	// season, and the two numbers say different things.
	Episodes int
}

// SeriesIndex answers about shows rather than about releases.
type SeriesIndex interface {
	// Series lists shows matching a name query (empty = all), most releases
	// first, with the total for paging.
	Series(ctx context.Context, query string, limit, offset int) ([]SeriesRow, int, error)

	// SeriesByKey returns one show's display name and whether it exists at all.
	SeriesByKey(ctx context.Context, key string) (name string, ok bool, err error)

	// Seasons lists a show's seasons with their counts, ascending. Season 0 is
	// a real season — specials — and is not filtered out here, because
	// deciding that is the page's business.
	Seasons(ctx context.Context, key string) ([]SeriesSeason, error)

	// Releases lists one show's releases, newest first. season < 0 means every
	// season; episode < 0 means every episode of the chosen season. That is
	// the whole filter the page needs: pick a season, then pick an episode
	// inside it, and remove either to widen back out.
	Releases(ctx context.Context, key string, season, episode, limit int) ([]Release, error)
}
