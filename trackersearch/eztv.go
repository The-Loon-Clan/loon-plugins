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
)

// EZTV is TV-only and its API takes exactly one kind of question: an IMDb
// id. No id, no answer -- the no-parameter form returns the site's global
// recent list, which is noise for any particular gap, so this adapter
// declines queries without an IMDb id rather than pretend. Season and
// episode are filtered HERE from the response's own numeric fields: the API
// cannot be asked for them, but it answers with them, which is better than
// title parsing.
const eztvAPI = "https://eztvx.to/api/get-torrents"

type eztv struct {
	http *http.Client
	url  string
}

func newEZTV(h *http.Client) *eztv { return &eztv{http: h, url: eztvAPI} }

func (e *eztv) Slug() string { return "eztv" }

type eztvHit struct {
	Title     string `json:"title"`
	Filename  string `json:"filename"`
	Hash      string `json:"hash"`
	MagnetURL string `json:"magnet_url"`
	// The API returns numbers as strings; strconv, not a retyped struct,
	// because "3" and 3 both occur in the wild depending on endpoint age.
	Season     string `json:"season"`
	Episode    string `json:"episode"`
	SizeBytes  string `json:"size_bytes"`
	Seeds      int    `json:"seeds"`
	Peers      int    `json:"peers"`
	ReleasedAt int64  `json:"date_released_unix"`
}

func (e *eztv) Search(ctx context.Context, q pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	// The API wants the bare number: tt15039982 -> 15039982.
	imdb := strings.TrimPrefix(q.IMDbID, "tt")
	if imdb == "" {
		return nil, nil
	}
	v := url.Values{}
	v.Set("imdb_id", imdb)
	v.Set("limit", "100")
	v.Set("page", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("eztv: %s", resp.Status)
	}
	var payload struct {
		Torrents []eztvHit `json:"torrents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("eztv: %w", err)
	}
	out := make([]pluginapi.TrackerCandidate, 0, 4)
	for _, h := range payload.Torrents {
		if atoi(h.Season) != q.Season || atoi(h.Episode) != q.Episode {
			continue
		}
		title := h.Title
		if title == "" {
			title = h.Filename
		}
		c := pluginapi.TrackerCandidate{
			TrackerSlug: e.Slug(),
			Title:       title,
			SizeBytes:   atoi64(h.SizeBytes),
			Seeders:     h.Seeds,
			Leechers:    h.Peers,
			InfoHash:    h.Hash,
			Magnet:      h.MagnetURL,
		}
		if h.ReleasedAt > 0 {
			c.PostedAt = time.Unix(h.ReleasedAt, 0).UTC()
		}
		out = append(out, c)
	}
	return out, nil
}

// atoi is strconv.Atoi with "anything unparseable is zero", which for a
// season/episode filter means "does not match" rather than an error path.
func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// atoi64 is the same for sizes, which do not fit a filter's shrug: a parse
// failure is an honest zero, never a truncation.
func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
