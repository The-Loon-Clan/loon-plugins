package feeds

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// torznabSearch is the published lpapi.TorznabSearch implementation — the
// on-demand half of this plugin, consumed by the host's resurrector and the
// request-board handlers. It shares the importer's config key but none of its
// deps, which is why it registers in every process while the importer runs
// only in the worker.
type torznabSearch struct {
	key    string
	client *http.Client
}

func (t *torznabSearch) Available() bool {
	return t != nil && t.key != ""
}

// Search performs an on-demand nekoBT Torznab query. When season>0 and
// episode>0 we issue a t=tvsearch with those params (the Torznab-spec way to
// scope results to a specific episode); otherwise we fall back to freeform
// t=search. Returns (nil, nil) when no API key is configured so callers can
// treat "unavailable" and "empty results" alike — the contract's documented
// shape.
func (t *torznabSearch) Search(ctx context.Context, query string, season, episode int) ([]lpapi.TorznabResult, error) {
	if !t.Available() {
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
	items, err := fetchNekoBTURL(ctx, t.client, "https://nekobt.to/api/torznab/api?"+q.Encode())
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
