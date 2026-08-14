package predb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/httpclient"
)

// api.predb.net — the PRIMARY source.
//
// Found by reading the SPA's own HTML for the host it calls, because
// predb.net/api-documentation renders client-side and shows nothing to a
// fetcher. The API itself is plain JSON and needs no key.
//
// Preferred over predb.ovh on every axis that matters here:
//
//	                  predb.net     predb.ovh
//	  records         14,242,239     7,751,280
//	  search          q=             q=
//	  paging          page=          offset=, hard limited to 1000 rows
//	  auth            none           none
//
// The row also carries `section` (MP3-WEB, APPS-0DAY, TV-HD…) and a
// `status`/`reason` pair for nukes, which is what a de-obfuscation lookup wants
// to know beyond the name itself.
//
// PARAMETERS, established by probing rather than documentation: `q` searches
// and `limit` sizes the page; `page` walks; `offset` and `count` are ignored.
// A filtered response omits results_total, which is how you tell a search from
// a listing.

const (
	netBase = "https://api.predb.net"

	// No published rate limit, which is a reason for MORE restraint rather
	// than less: an unstated limit is enforced by whoever is annoyed, and
	// this is a free service. One request per 2s, same as the .ovh client.
	netMinInterval = 2 * time.Second
)

// NetRow is one release as api.predb.net returns it. Field names are theirs.
type NetRow struct {
	ID      int64  `json:"id"`
	PreTime int64  `json:"pretime"` // unix seconds
	Release string `json:"release"`
	Section string `json:"section"` // MP3-WEB, APPS-0DAY, TV-HD, ...
	Files   int64  `json:"files"`
	Size    int64  `json:"size"` // megabytes, per the RSS feed's "Size: 64MB"
	Status  int    `json:"status"`
	Reason  string `json:"reason"`
	Group   string `json:"group"`
	Genre   string `json:"genre"`
	URL     string `json:"url"`
}

// At is the pre time.
func (r NetRow) At() time.Time {
	if r.PreTime <= 0 {
		return time.Time{}
	}
	return time.Unix(r.PreTime, 0).UTC()
}

// Nuked reports whether this release carries a nuke.
//
// status is 0 on the healthy rows observed; a non-zero status with a reason is
// the nuke. Reason alone is not enough — an empty reason with a set status is
// still a nuke, and a set reason with status 0 has not been seen but would be
// a nuke worth surfacing rather than hiding.
func (r NetRow) Nuked() bool {
	return r.Status != 0 || strings.TrimSpace(r.Reason) != ""
}

type netEnvelope struct {
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Results int      `json:"results"`
	Total   int      `json:"results_total"`
	Data    []NetRow `json:"data"`
}

// NetClient talks to api.predb.net.
type NetClient struct {
	base string
	http *http.Client
	last time.Time
}

func NewNetClient() *NetClient {
	return &NetClient{base: netBase, http: httpclient.NewAPI()}
}

// Search finds releases whose name matches q.
//
// This is the de-obfuscation primitive: given a posting title we cannot read,
// ask whether the scene announced something by that name. A hit gives the real
// release name, its section and its group without fetching a single article.
func (c *NetClient) Search(ctx context.Context, q string, limit int) ([]NetRow, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	v := url.Values{}
	v.Set("q", q)
	v.Set("limit", strconv.Itoa(limit))
	env, err := c.get(ctx, "/?"+v.Encode())
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

// Recent returns the newest releases, for the live-ingest path.
//
// page is 1-based and walks backwards through time. `offset` is accepted by
// the server and ignored, which is worth knowing: a caller that pages with it
// re-reads page one forever and looks like it is working.
func (c *NetClient) Recent(ctx context.Context, limit, page int) ([]NetRow, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	v := url.Values{}
	v.Set("limit", strconv.Itoa(limit))
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	env, err := c.get(ctx, "/?"+v.Encode())
	if err != nil {
		return nil, 0, err
	}
	return env.Data, env.Total, nil
}

func (c *NetClient) get(ctx context.Context, path string) (netEnvelope, error) {
	if wait := netMinInterval - time.Since(c.last); wait > 0 {
		select {
		case <-ctx.Done():
			return netEnvelope{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	c.last = time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return netEnvelope{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return netEnvelope{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return netEnvelope{}, fmt.Errorf("predb.net: rate limited — raise netMinInterval")
	}
	if resp.StatusCode != http.StatusOK {
		return netEnvelope{}, fmt.Errorf("predb.net: http %d", resp.StatusCode)
	}
	var env netEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return netEnvelope{}, fmt.Errorf("predb.net: decode: %w", err)
	}
	// Failure is reported in the body with a 200, so the status code alone
	// does not answer "did this work".
	if env.Status != "" && env.Status != "success" {
		return netEnvelope{}, fmt.Errorf("predb.net: %s: %s", env.Status, env.Message)
	}
	return env, nil
}
