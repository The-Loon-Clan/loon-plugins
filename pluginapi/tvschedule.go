package pluginapi

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// TVSchedule answers "what is airing, and when", for the shows this site
// actually carries.
//
// WHY THE SITE'S OWN SHOWS AND NOT ALL OF TELEVISION. A public schedule is
// about 140 episodes a day; a calendar showing all of them is a listings
// magazine, not a view of this indexer. The useful question is narrower and is
// the one an indexer can act on: something we carry has a new episode. That is
// also the trigger the request pipeline wants later -- a calendar that already
// knows "we carry this show and episode 4 airs on Tuesday" is one step from
// filing the request for it.
//
// WHY A CAPABILITY. The host draws the calendar and owns the catalog table; the
// plugin owns the upstream client and its rate limit. Neither needs the
// other's schema, and a host that wires no schedule provider simply has a
// calendar without television on it -- which is every indexer that has not
// enabled the scraper.
const TVScheduleName = "tv.schedule"

// TVEpisode is one airing.
//
// Flat and self-describing because it crosses the seam into a calendar cell:
// the consumer draws it without a second lookup, which matters when a month
// view holds a few hundred of them.
type TVEpisode struct {
	// ShowExtID is the upstream show id, in the namespace the catalog stores
	// for this domain. Carried so a consumer can link the episode back to the
	// catalogue entry it belongs to without matching on title -- titles are
	// what the release-name matcher already struggles with.
	ShowExtID string
	ShowTitle string
	Season    int
	Number    int
	// Title is the episode's own name, often empty for daily programming.
	Title string
	// AirsAt is the broadcast instant in UTC. A date alone is not enough: a
	// show airing 23:00 US Eastern is the following day in UTC, and a calendar
	// that stored the date would put it in the wrong cell for half the world.
	AirsAt time.Time
	// URL is the upstream page for the episode, or "".
	URL string
}

// TVScheduleProvider is the capability a plugin publishes.
type TVScheduleProvider interface {
	// Upcoming returns the episodes airing in [from, to), ordered by AirsAt.
	//
	// Returns (nil, nil) rather than an error when the provider has nothing
	// loaded yet -- it is filled by a scheduled job, so "the job has not run"
	// is an ordinary state on a process that started a minute ago, and a
	// calendar must not fail to draw because television is missing from it.
	Upcoming(ctx context.Context, from, to time.Time) ([]TVEpisode, error)
}

// LookupTVSchedule resolves the provider, or false when nothing publishes one.
func LookupTVSchedule(c *core.Core) (TVScheduleProvider, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(TVScheduleName)
	if !ok {
		return nil, false
	}
	p, ok := v.(TVScheduleProvider)
	return p, ok
}
