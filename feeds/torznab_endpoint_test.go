package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The search endpoint is configurable so a site can point it at Prowlarr,
// which aggregates a dozen indexers — public ones and an operator's private
// trackers — behind one URL that speaks Torznab. That is the whole
// integration: Torznab is the contract, and Prowlarr is one more thing that
// speaks it, so there is no Prowlarr-specific code to test. What IS worth
// pinning is that the request still comes out well-formed against an endpoint
// that is not nekoBT's.

const torznabFeed = `<?xml version="1.0"?>
<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>
<item><title>[Group] Show - 07 [1080p]</title>
<link>http://x/dl/1</link>
<newznab:attr name="infohash" value="abc123"/>
<newznab:attr name="seeders" value="42"/>
<newznab:attr name="size" value="1400000000"/>
</item></channel></rss>`

func TestSearchUsesTheConfiguredEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(torznabFeed))
	}))
	defer srv.Close()

	// The shape a Prowlarr aggregate endpoint takes.
	s := &torznabSearch{endpoint: srv.URL + "/api/v1/indexer/-/newznab", key: "k", client: srv.Client()}
	res, err := s.Search(context.Background(), "Show", 1, 7)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotPath != "/api/v1/indexer/-/newznab" {
		t.Errorf("path = %q — the configured endpoint was not used", gotPath)
	}
	for _, want := range []string{"apikey=k", "t=tvsearch", "season=1", "ep=7", "q=Show"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q is missing %q", gotQuery, want)
		}
	}
	// The response parser needs no change: Torznab is an RSS dialect and an
	// aggregator returns the same item shape. What differs is how many
	// indexers answered.
	if len(res) != 1 || res[0].InfoHash != "abc123" || res[0].Seeders != 42 {
		t.Errorf("parsed %+v, want one result with infohash abc123 and 42 seeders", res)
	}
}

func TestSearchEndpointDefaultsToNekoBT(t *testing.T) {
	// An unconfigured site must behave exactly as it did before the endpoint
	// existed, or this change is a silent outage for every deployment that
	// never sets it.
	s := &torznabSearch{key: "k"}
	if got := s.url(); got != defaultTorznabEndpoint {
		t.Errorf("url() = %q, want the nekoBT default", got)
	}
}

func TestSearchJoinsAQueryStringThatAlreadyHasOne(t *testing.T) {
	// Prowlarr endpoints are sometimes handed over already carrying a
	// parameter. Appending "?" a second time produces a URL no server parses,
	// and the failure looks like "the indexer returned nothing".
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(torznabFeed))
	}))
	defer srv.Close()

	s := &torznabSearch{endpoint: srv.URL + "/api?src=loon", key: "k", client: srv.Client()}
	if _, err := s.Search(context.Background(), "Show", 0, 0); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(gotQuery, "src=loon") || !strings.Contains(gotQuery, "apikey=k") {
		t.Errorf("query = %q — both the endpoint's own parameter and ours must survive", gotQuery)
	}
	if strings.Count(gotQuery, "?") != 0 {
		t.Errorf("query = %q — a second '?' was appended", gotQuery)
	}
}

func TestSearchUnavailableWithoutAKey(t *testing.T) {
	s := &torznabSearch{endpoint: "http://example.invalid/api"}
	if s.Available() {
		t.Error("reported available with no api key")
	}
	res, err := s.Search(context.Background(), "x", 0, 0)
	if err != nil || res != nil {
		t.Errorf("want (nil, nil) when unavailable, got (%v, %v)", res, err)
	}
}
