package trackersearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/trackerdir"
)

// UNIT3D is not a tracker -- it is the software 75 of them run, and they share
// one documented JSON API. So this is ONE adapter for a whole family: give it a
// tracker's domain and a member's API key and it speaks to Aither, Blutopia,
// LST and seventy-odd more without a line of per-tracker code.
//
// This is the standard the bespoke public adapters lack. apibay, eztv and
// torrents-csv each invented their own shape; UNIT3D publishes
// /api/torrents/filter (https://hdinnovations.github.io/UNIT3D/torrent_api.html)
// with a Bearer token, takes the external ids a gap already carries, and
// answers with a uniform attributes object. The field mapping here is the one
// Prowlarr's own Cardigann definition uses, so it tracks the API, not a guess.
//
// PRIVATE, so DORMANT until credentials exist. Every UNIT3D tracker needs the
// member's own key, and this host stores none -- per-tracker enablement and
// key storage is the wiring step, not this package. Unit3dAdapters returns the
// instances a host has configured, which is empty here; the type is built and
// tested so the day a host stores a key, that tracker is searchable with no new
// code.

// Unit3dConfig is one configured UNIT3D tracker: which one, and the key.
type Unit3dConfig struct {
	// Slug is the trackerdir identity, so the adapter inherits the recorded
	// domain and politeness rather than restating them.
	Slug string
	// APIKey is the member's bearer token for this tracker.
	APIKey string
	// BaseURL overrides the directory's domain when a tracker's API lives on a
	// different host; empty uses the directory's primary domain.
	BaseURL string
}

// unit3d is one configured tracker's client.
type unit3d struct {
	http    *http.Client
	slug    string
	baseURL string
	apiKey  string
}

// Unit3dAdapters builds a client per configured UNIT3D tracker. A config whose
// slug is not a known UNIT3D tracker, or which carries no key, is skipped: a
// half-configured private source is not a source.
func Unit3dAdapters(h *http.Client, configs []Unit3dConfig) []adapter {
	var out []adapter
	for _, cfg := range configs {
		if cfg.APIKey == "" {
			continue
		}
		base := strings.TrimRight(cfg.BaseURL, "/")
		if base == "" {
			t, ok := trackerdir.BySlug(cfg.Slug)
			if !ok || len(t.Domains) == 0 {
				continue
			}
			base = strings.TrimRight(t.Domains[0], "/")
		}
		out = append(out, &unit3d{http: h, slug: cfg.Slug, baseURL: base, apiKey: cfg.APIKey})
	}
	return out
}

func (u *unit3d) Slug() string { return u.slug }

// unit3dResponse is the JSON:API-shaped envelope UNIT3D returns: a data array
// whose every element carries the release in an attributes object.
type unit3dResponse struct {
	Data []struct {
		Attributes unit3dAttrs `json:"attributes"`
	} `json:"data"`
}

// unit3dAttrs is the fields this package reads. UNIT3D returns many more;
// these are the ones a candidate needs, named as the API names them.
type unit3dAttrs struct {
	Name         string          `json:"name"`
	Size         json.RawMessage `json:"size"` // number in bytes, occasionally a string
	Seeders      int             `json:"seeders"`
	Leechers     int             `json:"leechers"`
	InfoHash     string          `json:"info_hash"`
	DownloadLink string          `json:"download_link"`
	DetailsLink  string          `json:"details_link"`
	CreatedAt    string          `json:"created_at"`
}

func (u *unit3d) Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	v := url.Values{}
	// Season and episode as their own parameters -- UNIT3D v6+ filters on
	// them directly, which is more precise than folding them into the name.
	if q.Season > 0 {
		v.Set("seasonNumber", strconv.Itoa(q.Season))
	}
	if q.Episode > 0 {
		v.Set("episodeNumber", strconv.Itoa(q.Episode))
	}
	// The strongest identity available. An id search on a private tracker is
	// exact -- no title folding -- which is the whole reason to prefer these
	// over the public sources when a site has the account.
	if q.IMDbID != "" {
		v.Set("imdbId", strings.TrimPrefix(q.IMDbID, "tt"))
	}
	if q.TVDBID != "" {
		v.Set("tvdbId", q.TVDBID)
	}
	if q.TVMazeID != "" {
		// UNIT3D has no tvmaze filter; the name still narrows it.
	}
	// A name is always sent: with no id it is the query, and with an id it is
	// a harmless extra constraint the API ANDs in.
	if q.ShowTitle != "" {
		v.Set("name", q.ShowTitle)
	}
	if v.Get("imdbId") == "" && v.Get("tvdbId") == "" && q.ShowTitle == "" {
		return nil, nil // nothing to ask by
	}
	v.Set("perPage", "50")

	endpoint := u.baseURL + "/api/torrents/filter?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		// 401/403 is a bad or expired key, not an empty result -- surfaced as
		// an error so the operator sees "key rejected", not "nothing found".
		return nil, fmt.Errorf("unit3d %s: %s", u.slug, resp.Status)
	}
	var payload unit3dResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("unit3d %s: %w", u.slug, err)
	}
	out := make([]pluginapi.TrackerCandidate, 0, len(payload.Data))
	for _, d := range payload.Data {
		a := d.Attributes
		c := pluginapi.TrackerCandidate{
			TrackerSlug: u.slug,
			Title:       a.Name,
			SizeBytes:   rawInt64(a.Size),
			Seeders:     a.Seeders,
			Leechers:    a.Leechers,
			InfoHash:    a.InfoHash,
			// The authenticated .torrent URL: a private tracker hands over a
			// file, not a magnet, and the link already carries the key.
			DownloadURL: a.DownloadLink,
			PageURL:     a.DetailsLink,
		}
		if t, err := time.Parse(time.RFC3339, a.CreatedAt); err == nil {
			c.PostedAt = t
		}
		out = append(out, c)
	}
	return out, nil
}

// rawInt64 reads a JSON number that UNIT3D usually sends bare but sometimes
// quotes. A value it cannot parse is an honest zero, never a panic.
func rawInt64(raw json.RawMessage) int64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
