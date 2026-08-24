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

// ─────────────────────────────────────────────────────────────────────────────
// Aired, and we do not have it.
// ─────────────────────────────────────────────────────────────────────────────

// TVGapsName is where the join between the schedule and the index is published.
//
// A SECOND capability rather than a method on TVScheduleProvider, because it
// has a different owner. The schedule is the scraper plugin's: it owns the
// upstream client and its rate limit, and knows nothing about what this site
// has indexed. The gap is the HOST's: only the host holds both the schedule and
// a SeriesIndex, and joining them is the one thing neither side can do alone.
// Folding them into one interface would oblige every schedule provider to also
// answer for an index it cannot see.
//
// This is the detection half of the auto-request. Nothing files a request yet;
// what exists here is the input that step needs, and an operator view of what
// the index missed, which is worth having on its own.
const TVGapsName = "tv.gaps"

// TVGap is one episode that aired with nothing in the index to match it.
type TVGap struct {
	// Episode is the airing, exactly as the schedule reported it.
	Episode TVEpisode
	// SeriesKey is Episode.ShowTitle run through SeriesKey -- the key a
	// consumer would search the index by, carried so it does not refold and
	// risk disagreeing about what it asked.
	SeriesKey string
	// Indexed reports whether the index holds ANY release of this show.
	//
	// The distinction the list is useless without. False means the site
	// catalogued a show and never indexed a byte of it -- on real data that is
	// 18% of carried TV shows, and every episode of every one of them is
	// technically a gap. Reported separately because it is a DIFFERENT problem:
	// one missed episode of a show we otherwise carry is worth requesting,
	// while a show we have never had a release of is usually a catalogue
	// artifact rather than something anybody wants filed.
	Indexed bool
}

// TVGapFinder answers what aired without arriving.
type TVGapFinder interface {
	// Gaps returns the episodes that aired in [from, to) and have no matching
	// release, oldest first.
	//
	// Only episodes that aired far enough in the past to be judged -- see the
	// host's grace period. An episode broadcast twenty minutes ago is missing
	// from every index on earth, and reporting it as a gap would bury the real
	// ones under the merely recent.
	//
	// (nil, nil) when the schedule has not filled yet, when no index is
	// wired, or when there is genuinely nothing missing. A caller cannot tell
	// those apart and does not need to: all three mean "nothing to do".
	Gaps(ctx context.Context, from, to time.Time) ([]TVGap, error)
}

// LookupTVGaps resolves the gap finder, or false when nothing publishes one.
func LookupTVGaps(c *core.Core) (TVGapFinder, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(TVGapsName)
	if !ok {
		return nil, false
	}
	p, ok := v.(TVGapFinder)
	return p, ok
}
