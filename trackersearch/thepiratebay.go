package trackersearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The Pirate Bay, through its documented JSON endpoint (apibay.org) rather
// than the HTML site. It is the broadest public source for mainstream TV, and
// every hit carries its own info_hash, seeders, size and IMDb id -- no page to
// scrape, which is the bar this package sets for wiring a source at all.
//
// The API host is apibay.org, not the thepiratebay.org browse domain the
// directory records; fixed here because where a tracker's API lives is
// implementation, the kind this package owns.
//
// WHY BESIDE KNABEN, which already aggregates TPB. An aggregator caches, and a
// cache lags and drops: knaben returned The Ark S03E04 at ~30 seeders while
// TPB direct had the same episode at 577. Asking the source itself gets the
// live swarm and the fuller list, and the two agree often enough that the
// merge de-duplicates naturally on info_hash downstream.
const piratebayAPI = "https://apibay.org/q.php"

// TV category ids on TPB: 205 is SD television, 208 is HD. Restricting the
// query to them keeps a show name that is also a film or a game out of the
// results, without a second round trip.
var piratebayTVCats = map[string]bool{"205": true, "208": true}

type piratebay struct {
	http *http.Client
	url  string
}

func newPirateBay(h *http.Client) *piratebay { return &piratebay{http: h, url: piratebayAPI} }

func (p *piratebay) Slug() string { return "thepiratebay" }

// piratebayHit is one result. Every numeric field arrives as a string, which
// is the API's own shape, not a choice worth fighting.
type piratebayHit struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	InfoHash string `json:"info_hash"`
	Seeders  string `json:"seeders"`
	Leechers string `json:"leechers"`
	Size     string `json:"size"`
	Added    string `json:"added"`
	Category string `json:"category"`
	IMDb     string `json:"imdb"`
}

func (p *piratebay) Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	if q.ShowTitle == "" {
		return nil, nil
	}
	v := url.Values{}
	v.Set("q", q.ShowTitle+" "+episodeCode(q.Season, q.Episode))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("thepiratebay: %s", resp.Status)
	}
	var hits []piratebayHit
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&hits); err != nil {
		return nil, fmt.Errorf("thepiratebay: %w", err)
	}
	out := make([]pluginapi.TrackerCandidate, 0, len(hits))
	for _, h := range hits {
		// The empty result is a ONE-ELEMENT array with id "0" and a zeroed
		// info_hash, not an empty list. Treating it as a real hit would put a
		// "No results returned" row with no swarm into every gap that has no
		// copy -- the exact false positive the seeder floor exists to stop,
		// but cheaper to reject here by its sentinel.
		if h.ID == "0" || h.InfoHash == "" || h.InfoHash == "0000000000000000000000000000000000000000" {
			continue
		}
		if !piratebayTVCats[h.Category] {
			continue
		}
		c := pluginapi.TrackerCandidate{
			TrackerSlug: p.Slug(),
			Title:       h.Name,
			SizeBytes:   atoi64(h.Size),
			Seeders:     atoi(h.Seeders),
			Leechers:    atoi(h.Leechers),
			InfoHash:    h.InfoHash,
			Magnet:      "magnet:?xt=urn:btih:" + h.InfoHash,
		}
		if n, err := strconv.ParseInt(h.Added, 10, 64); err == nil && n > 0 {
			c.PostedAt = time.Unix(n, 0).UTC()
		}
		out = append(out, c)
	}
	return out, nil
}
