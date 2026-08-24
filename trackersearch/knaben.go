package trackersearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Knaben is an aggregator: it caches many trackers' listings and answers one
// JSON query over the lot, naming the origin per hit. As a first wire it
// covers the most ground per request -- one polite call instead of one per
// tracker -- and every hit carries the tracker it came from, kept in Via so
// a reader knows what "found on knaben" actually means.
//
// The API endpoint is its own host (api.knaben.org), not the browse domain
// the directory records. Fixed here rather than derived: the directory
// carries facts about the tracker, and where its API lives is implementation
// -- exactly the kind this package exists to own.
const knabenAPI = "https://api.knaben.org/v1"

type knaben struct {
	http *http.Client
	url  string
}

func newKnaben(h *http.Client) *knaben { return &knaben{http: h, url: knabenAPI} }

func (k *knaben) Slug() string { return "knaben" }

// knabenHit is one result, fields as documented. virusDetection is kept so
// flagged uploads can be dropped rather than recommended.
type knabenHit struct {
	Title     string `json:"title"`
	Bytes     int64  `json:"bytes"`
	Seeders   int    `json:"seeders"`
	Peers     int    `json:"peers"`
	Hash      string `json:"hash"`
	MagnetURL string `json:"magnetUrl"`
	Details   string `json:"details"`
	Date      string `json:"date"`
	Tracker   string `json:"tracker"`
	// virusDetection is a SCORE, 0..1, not a list -- the probability upstream
	// assigns the upload being malware. The name reads like a flag and the
	// first sample happened to be an empty array; a live search proved it a
	// float and the strict typing rejected the whole response. Above the
	// threshold the candidate is dropped.
	VirusDetection float64 `json:"virusDetection"`
}

// virusThreshold is where a knaben malware score stops being noise. Their own
// hide_unsafe filter is the first line; this is a conservative second one, set
// well clear of the ~0.2 scores ordinary releases carry.
const virusThreshold = 0.8

func (k *knaben) Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	if q.ShowTitle == "" {
		return nil, nil // a pure-id query is a question knaben cannot be asked
	}
	body, err := json.Marshal(map[string]any{
		"search_field": "title",
		"query":        q.ShowTitle + " " + episodeCode(q.Season, q.Episode),
		"size":         25,
		// Upstream's own filter for known-bad uploads. Belt and braces with
		// the virusDetection check below: the filter is theirs to change.
		"hide_unsafe": true,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("knaben: %s", resp.Status)
	}
	var payload struct {
		Hits []knabenHit `json:"hits"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("knaben: %w", err)
	}
	out := make([]pluginapi.TrackerCandidate, 0, len(payload.Hits))
	for _, h := range payload.Hits {
		if h.VirusDetection >= virusThreshold {
			continue
		}
		c := pluginapi.TrackerCandidate{
			TrackerSlug: k.Slug(),
			Via:         h.Tracker,
			Title:       h.Title,
			SizeBytes:   h.Bytes,
			Seeders:     h.Seeders,
			Leechers:    h.Peers,
			InfoHash:    h.Hash,
			Magnet:      h.MagnetURL,
			PageURL:     h.Details,
		}
		if t, err := time.Parse(time.RFC3339, h.Date); err == nil {
			c.PostedAt = t
		}
		out = append(out, c)
	}
	return out, nil
}
