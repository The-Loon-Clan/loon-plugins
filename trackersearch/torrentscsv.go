package trackersearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// torrents-csv is a public, open-source index of well-seeded torrents with a
// plain JSON search endpoint. It answers with infohashes rather than pages:
// no magnet, no details URL, just the identity and the swarm numbers -- which
// for the pipeline's question ("does a healthy copy exist?") is the whole
// answer. The magnet is assembled from the infohash, trackerless; any client
// resolves peers over DHT.
const torrentsCSVAPI = "https://torrents-csv.com/service/search"

type torrentsCSV struct {
	http *http.Client
	url  string
}

func newTorrentsCSV(h *http.Client) *torrentsCSV {
	return &torrentsCSV{http: h, url: torrentsCSVAPI}
}

func (t *torrentsCSV) Slug() string { return "torrentscsv" }

type torrentsCSVHit struct {
	InfoHash    string `json:"infohash"`
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedUnix int64  `json:"created_unix"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
}

func (t *torrentsCSV) Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	if q.ShowTitle == "" {
		return nil, nil
	}
	v := url.Values{}
	v.Set("q", q.ShowTitle+" "+episodeCode(q.Season, q.Episode))
	v.Set("size", "25")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("torrents-csv: %s", resp.Status)
	}
	var payload struct {
		Torrents []torrentsCSVHit `json:"torrents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("torrents-csv: %w", err)
	}
	out := make([]pluginapi.TrackerCandidate, 0, len(payload.Torrents))
	for _, h := range payload.Torrents {
		if h.InfoHash == "" {
			continue
		}
		c := pluginapi.TrackerCandidate{
			TrackerSlug: t.Slug(),
			Title:       h.Name,
			SizeBytes:   h.SizeBytes,
			Seeders:     h.Seeders,
			Leechers:    h.Leechers,
			InfoHash:    h.InfoHash,
			Magnet:      "magnet:?xt=urn:btih:" + h.InfoHash,
		}
		if h.CreatedUnix > 0 {
			c.PostedAt = time.Unix(h.CreatedUnix, 0).UTC()
		}
		out = append(out, c)
	}
	return out, nil
}
