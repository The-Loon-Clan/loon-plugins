// Package trackersearch asks external trackers what they have.
//
// The third piece of the content pipeline: tv.schedule knows an episode
// aired, tv.gaps knows the index holds nothing matching it, and this answers
// where a copy might come from. trackerdir is the map; this is the part that
// actually knocks on doors.
//
// ADAPTERS, NOT DEFINITIONS. Each source gets a hand-written client for its
// own clean interface -- a JSON API with documented parameters -- and a
// source without one does not get wired. This is the deliberate opposite of
// the Prowlarr/Jackett approach of describing every tracker's HTML: their
// definitions are their expression (and scraping is an arms race nobody is
// polite in). Three public, no-credential sources are wired today; private
// sources join when per-site credential storage exists, which is the wiring
// step's business, not this package's.
//
// POLITENESS IS STRUCTURAL. Every adapter runs behind a per-source limiter
// primed from trackerdir's request_delay_seconds, with a floor of two
// seconds -- the figure Prowlarr itself enforces as a minimum. A source that
// asked for more gets more. There is no configuration to get this wrong.
package trackersearch

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/trackerdir"
)

// adapter is one source's client. Implementations live in this package, one
// file each, and parse only documented JSON.
type adapter interface {
	// Slug is the trackerdir identity of the source.
	Slug() string
	// Search returns candidates, or (nil, nil) when the source cannot answer
	// this query at all -- eztv without an IMDb id, for instance. That is
	// different from an error, which means the source was asked and failed.
	Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error)
}

// perSearchTimeout bounds one source's answer. Generous next to a normal
// API round-trip, tight next to a page render: the caller fans out, so the
// slowest source sets the wall time, and a hung source must not hold a
// search open indefinitely.
const perSearchTimeout = 15 * time.Second

// politeFloor is the minimum interval between requests to one source, used
// when the directory records no delay of its own.
const politeFloor = 2 * time.Second

// Client implements pluginapi.TrackerSearcher over the wired adapters.
type Client struct {
	adapters []adapter

	mu      sync.Mutex
	lastErr map[string]string
	// nextAt is the earliest time each source may be asked again -- the
	// limiter. Simpler than tokens because the whole budget is "one polite
	// request at a time", and held across searches: two operators clicking
	// at once must not double the rate a source sees.
	nextAt map[string]time.Time
	delay  map[string]time.Duration
}

var _ pluginapi.TrackerSearcher = (*Client)(nil)

// New builds the client over the public no-credential adapters, plus one
// UNIT3D adapter per configured private tracker. The directory supplies each
// source's domains and politeness; the demo passes no UNIT3D configs, so those
// stay dormant until a host stores keys -- see unit3d.go.
func New(unit3d ...Unit3dConfig) *Client {
	c := &Client{
		lastErr: map[string]string{},
		nextAt:  map[string]time.Time{},
		delay:   map[string]time.Duration{},
	}
	httpc := &http.Client{Timeout: perSearchTimeout}
	adapters := []adapter{
		newKnaben(httpc),
		newTorrentsCSV(httpc),
		newEZTV(httpc),
		newPirateBay(httpc),
	}
	// One UNIT3D client per configured tracker -- the 75-strong family behind
	// a single implementation.
	adapters = append(adapters, Unit3dAdapters(httpc, unit3d)...)
	for _, a := range adapters {
		c.adapters = append(c.adapters, a)
		d := politeFloor
		if t, ok := trackerdir.BySlug(a.Slug()); ok {
			if want := time.Duration(t.RequestDelaySeconds * float64(time.Second)); want > d {
				d = want
			}
		}
		c.delay[a.Slug()] = d
	}
	return c
}

// SearchEpisode fans out to every source and merges what came back.
func (c *Client) SearchEpisode(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	if q.ShowTitle == "" && q.IMDbID == "" && q.TVMazeID == "" {
		return nil, fmt.Errorf("empty query: no title and no id")
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []pluginapi.TrackerCandidate
	)
	for _, a := range c.adapters {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, perSearchTimeout)
			defer cancel()
			c.waitTurn(cctx, a.Slug())
			got, err := a.Search(cctx, q)
			c.mu.Lock()
			if err != nil {
				// Recorded, not returned: one dead source must not turn
				// "knaben found twelve copies" into an error page.
				c.lastErr[a.Slug()] = err.Error()
			} else {
				c.lastErr[a.Slug()] = ""
			}
			c.mu.Unlock()
			if len(got) == 0 {
				return
			}
			mu.Lock()
			out = append(out, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	// Healthiest swarm first: a candidate nobody seeds is a name, not a
	// copy. Size breaks ties so two equally-seeded rips order stably.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Seeders != out[j].Seeders {
			return out[i].Seeders > out[j].Seeders
		}
		return out[i].SizeBytes > out[j].SizeBytes
	})
	return out, nil
}

// waitTurn blocks until this source may politely be asked again, or the
// context dies. The reservation is taken up front so concurrent searches
// queue behind each other rather than racing the clock.
func (c *Client) waitTurn(ctx context.Context, slug string) {
	c.mu.Lock()
	now := time.Now()
	at := c.nextAt[slug]
	if at.Before(now) {
		at = now
	}
	c.nextAt[slug] = at.Add(c.delay[slug])
	c.mu.Unlock()
	if wait := time.Until(at); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
}

// Sources reports the wired sources and their last-known health.
func (c *Client) Sources() []pluginapi.TrackerSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]pluginapi.TrackerSource, 0, len(c.adapters))
	for _, a := range c.adapters {
		out = append(out, pluginapi.TrackerSource{
			Slug: a.Slug(), LastErr: c.lastErr[a.Slug()],
		})
	}
	return out
}

// episodeCode is "S03E07", the form every candidate title uses.
func episodeCode(season, episode int) string {
	return fmt.Sprintf("S%02dE%02d", season, episode)
}
