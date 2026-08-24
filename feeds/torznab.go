package feeds

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// torznabSearch is the published lpapi.TorznabSearch implementation — the
// on-demand half of this plugin, consumed by the host's resurrector and the
// request-board handlers. It shares the importer's config key but none of its
// deps, which is why it registers in every process while the importer runs
// only in the worker.
type torznabSearch struct {
	// endpoint is the full Torznab API URL, defaulting to nekoBT's.
	//
	// Configurable because "search the torrent sites we have" is a different
	// question from "search nekoBT", and the second was hardcoded. A site with
	// Prowlarr in front of a dozen indexers — public ones and the operator's
	// own private trackers — exposes one aggregate Torznab endpoint that
	// speaks exactly this protocol, so pointing this at it turns a
	// single-index lookup into a search across everything they hold, without a
	// line of Prowlarr-specific code. That is the whole integration: Torznab
	// is the contract, and Prowlarr is one more thing that speaks it.
	endpoint string
	key      string
	client   *http.Client
	// nyaa is the client for direct nyaa.si RSS search (nyaa_search.go), nil
	// when disabled. A separate client because it is a separate SOURCE: it
	// honours its own source_proxies entry, exactly like the importer's
	// fetches of the same site.
	nyaa *http.Client
	// nyaaURL overrides the nyaa search base in tests; empty means nyaa.si.
	nyaaURL string
}

// defaultTorznabEndpoint is nekoBT, which is what every deployment used before
// the endpoint was configurable. Kept as the default so an unconfigured site
// behaves exactly as it did.
const defaultTorznabEndpoint = "https://nekobt.to/api/torznab/api"

func (t *torznabSearch) Available() bool {
	return t != nil && (t.key != "" || t.nyaa != nil)
}

func (t *torznabSearch) url() string {
	if t.endpoint != "" {
		return t.endpoint
	}
	return defaultTorznabEndpoint
}

// Search performs an on-demand Torznab query against the configured endpoint. When season>0 and
// episode>0 we issue a t=tvsearch with those params (the Torznab-spec way to
// scope results to a specific episode); otherwise we fall back to freeform
// t=search. Returns (nil, nil) when no API key is configured so callers can
// treat "unavailable" and "empty results" alike — the contract's documented
// shape.
func (t *torznabSearch) Search(ctx context.Context, query string, season, episode int) ([]lpapi.TorznabResult, error) {
	if !t.Available() {
		return nil, nil
	}
	out, err := t.searchTorznab(ctx, query, season, episode)
	if err != nil && t.nyaa == nil {
		// Torznab was the only backend; its failure is the search's failure.
		return nil, err
	}
	// Nyaa answers beside the endpoint, not instead of it: the endpoint (or
	// the aggregator behind it) may hold private-tracker results nyaa never
	// will, and nyaa holds the public back-catalogue the importer's
	// newest-items firehose scrolled past months ago. Results merge with the
	// endpoint's first, deduplicated by info hash; a nyaa failure with
	// endpoint results in hand degrades to those rather than failing a
	// search that half-succeeded.
	if t.nyaa != nil {
		hits, nerr := t.searchNyaa(ctx, nyaaQuery(query, episode))
		if nerr == nil && len(hits) == 0 {
			if padded := nyaaPaddedQuery(query, episode); padded != "" {
				hits, nerr = t.searchNyaa(ctx, padded)
			}
		}
		if nerr != nil {
			if err != nil || out == nil && t.key == "" {
				// Nothing else answered either — surface the failure.
				if err == nil {
					err = nerr
				}
				return nil, err
			}
		} else {
			seen := make(map[string]bool, len(out))
			for _, r := range out {
				seen[r.InfoHash] = true
			}
			for _, h := range hits {
				if h.InfoHash != "" && seen[h.InfoHash] {
					continue
				}
				out = append(out, h)
			}
		}
	}
	return out, nil
}

// searchTorznab is the endpoint half — nekoBT by default, a Prowlarr
// aggregate wherever torznab_url points. (nil, nil) with no key configured.
func (t *torznabSearch) searchTorznab(ctx context.Context, query string, season, episode int) ([]lpapi.TorznabResult, error) {
	if t.key == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("apikey", t.key)
	if query != "" {
		q.Set("q", query)
	}
	if season > 0 && episode > 0 {
		q.Set("t", "tvsearch")
		q.Set("season", strconv.Itoa(season))
		q.Set("ep", strconv.Itoa(episode))
	} else {
		q.Set("t", "search")
	}
	// The response parser is unchanged and does not need to change: Torznab is
	// an RSS dialect, and an aggregator returns the same <item> shape with the
	// same newznab:attr fields. What differs is how MANY indexers answered.
	sep := "?"
	if strings.Contains(t.url(), "?") {
		sep = "&"
	}
	items, err := fetchNekoBTURL(ctx, t.client, t.url()+sep+q.Encode())
	if err != nil {
		return nil, err
	}
	out := make([]lpapi.TorznabResult, 0, len(items))
	for _, it := range items {
		out = append(out, lpapi.TorznabResult{
			Title:    it.title,
			Link:     it.link,
			InfoHash: it.infoHash,
			Seeders:  it.seeders,
			Size:     it.size,
			Category: it.category,
		})
	}
	return out, nil
}

var _ lpapi.TorznabSearch = (*torznabSearch)(nil)
