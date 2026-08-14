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

// The predb.ovh HTTP API — https://predbdotovh.github.io/pre-api/
//
// The second of two sources, and the better one for two of the three jobs a
// PreDB does here:
//
//   - LOOKUP (de-obfuscate one release): the API, because ?q= searches 7.7M
//     records we would otherwise have to hold ourselves.
//   - LIVE (record what is being released now): either the API's WebSocket or
//     the #PreNNTmux IRC channel. Same data, different transport.
//   - BULK MIRROR: neither, and this is the constraint that shapes the design.
//     The API is "hard limited at 1000" rows by its Sphinx backend, so offset
//     paging cannot walk 7.7M records. A full local mirror is not on offer.
//
// That last point is why this is a lookup client and not an importer. What we
// keep locally is what we saw live plus what we asked about; the rest stays
// remote and is queried when a specific obfuscated release needs a name.

const (
	apiBase = "https://predb.ovh/api/v1"

	// The documented limits: 30 requests / 60s on the cached endpoints, and
	// 2 / 20s on /live. Honoured with a floor between calls rather than a
	// bucket, because a lookup path that occasionally bursts is exactly what
	// gets an IP banned from a free service somebody runs as a favour.
	apiMinInterval = 2 * time.Second

	// The backend's own ceiling. Asking for more is silently truncated, and a
	// caller that believes it received everything is the failure that matters.
	apiMaxRows = 1000
)

// Row is one release as the API returns it. Field names are theirs.
type Row struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Team  string  `json:"team"`
	Cat   string  `json:"cat"`
	Genre string  `json:"genre"`
	URL   string  `json:"url"`
	Size  int64   `json:"size"`
	Files int64   `json:"files"`
	PreAt int64   `json:"preAt"` // unix seconds
	Nuke  *string `json:"nuke"`  // null when not nuked
}

// At is the release time as a time.Time. Separate from the field because the
// wire format is a unix integer and callers should not each re-derive that.
func (r Row) At() time.Time {
	if r.PreAt <= 0 {
		return time.Time{}
	}
	return time.Unix(r.PreAt, 0).UTC()
}

// Nuked reports whether the release carries a nuke reason.
func (r Row) Nuked() bool { return r.Nuke != nil && strings.TrimSpace(*r.Nuke) != "" }

type apiEnvelope struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		RowCount int   `json:"rowCount"`
		Offset   int   `json:"offset"`
		ReqCount int   `json:"reqCount"`
		Total    int   `json:"total"`
		Rows     []Row `json:"rows"`
	} `json:"data"`
}

// Client talks to predb.ovh.
type Client struct {
	base string
	http *http.Client

	// last is when the previous request went out, so the interval floor
	// applies across calls rather than within one.
	last time.Time
}

func NewClient() *Client {
	return &Client{base: apiBase, http: httpclient.NewAPI()}
}

// Search returns releases matching q, newest first as the API orders them.
//
// q takes SphinxSearch syntax. For de-obfuscation the useful shape is the
// candidate name itself: the index is over release names, so an exact or
// near-exact posting title finds its pre when one exists.
func (c *Client) Search(ctx context.Context, q string, count int) ([]Row, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if count <= 0 || count > apiMaxRows {
		count = 20
	}
	v := url.Values{}
	v.Set("q", q)
	v.Set("count", strconv.Itoa(count))
	return c.get(ctx, "/?"+v.Encode())
}

// ByID fetches one release by the API's own id.
func (c *Client) ByID(ctx context.Context, id int64) (Row, bool, error) {
	v := url.Values{}
	v.Set("id", strconv.FormatInt(id, 10))
	rows, err := c.get(ctx, "/?"+v.Encode())
	if err != nil || len(rows) == 0 {
		return Row{}, false, err
	}
	return rows[0], true, nil
}

func (c *Client) get(ctx context.Context, path string) ([]Row, error) {
	// Space the calls. A free service run as a favour deserves it, and a
	// burst is what gets an IP banned.
	if wait := apiMinInterval - time.Since(c.last); wait > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	c.last = time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("predb: rate limited (%d) — the interval floor is too low", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("predb: http %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("predb: decode: %w", err)
	}
	// The API reports failure in the BODY with a 200 status, so the status
	// code alone is not the answer to "did this work".
	if env.Status != "" && env.Status != "success" {
		return nil, fmt.Errorf("predb: %s: %s", env.Status, env.Message)
	}
	return env.Data.Rows, nil
}
