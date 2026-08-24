package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nyaa joined the search capability because the importer's newest-items
// firehose cannot reach anything that has scrolled off it — a months-old
// missing episode is exactly what a search exists for. These tests pin the
// pieces that earn their keep: the padded second try (release groups pad the
// episode to the run's magnitude, so "92" and "092" are different tokens),
// the merge-and-dedup against the Torznab endpoint, and availability with no
// key at all.

const nyaaSearchFeed = `<?xml version="1.0" encoding="utf-8"?>
<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>
<item><title>[Group] BEYBLADE X - 092 (1080p)</title>
<link>https://nyaa.si/download/111.torrent</link>
<guid>https://nyaa.si/view/111</guid>
<nyaa:seeders>17</nyaa:seeders><nyaa:leechers>2</nyaa:leechers>
<nyaa:downloads>412</nyaa:downloads>
<nyaa:infoHash>ABCDEF0123456789ABCDEF0123456789ABCDEF01</nyaa:infoHash>
<nyaa:categoryId>1_2</nyaa:categoryId><nyaa:category>Anime - English</nyaa:category>
<nyaa:size>1.4 GiB</nyaa:size><nyaa:trusted>No</nyaa:trusted><nyaa:remake>No</nyaa:remake>
</item>
<item><title>[ReEnc] BEYBLADE X - 092 (re-encode)</title>
<link>https://nyaa.si/download/112.torrent</link>
<guid>https://nyaa.si/view/112</guid>
<nyaa:seeders>99</nyaa:seeders>
<nyaa:infoHash>FFFF</nyaa:infoHash>
<nyaa:categoryId>1_2</nyaa:categoryId><nyaa:category>Anime - English</nyaa:category>
<nyaa:size>700 MiB</nyaa:size><nyaa:remake>Yes</nyaa:remake>
</item>
</channel></rss>`

// The plain-number query finds nothing, the padded one finds the release —
// which is the BEYBLADE shape exactly: the groups pad to three digits on a
// hundred-episode run. The remake in the feed must not survive parsing (the
// importer's own rule, shared by construction).
func TestNyaaSearchRetriesWithPaddedEpisode(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		queries = append(queries, q)
		if strings.Contains(q, "092") {
			w.Write([]byte(nyaaSearchFeed))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	defer srv.Close()

	s := &torznabSearch{nyaa: srv.Client(), nyaaURL: srv.URL + "/?page=rss&c=1_0&q="}
	res, err := s.Search(context.Background(), "BEYBLADE X", 1, 92)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(queries) != 2 || queries[0] != "BEYBLADE X 92" || queries[1] != "BEYBLADE X 092" {
		t.Fatalf("queries = %v, want the plain try then the padded one", queries)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1 — the remake must be dropped", len(res))
	}
	r := res[0]
	if r.InfoHash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("info hash not lowercased: %q", r.InfoHash)
	}
	if r.Seeders != 17 || r.Size != 1503238553 { // 1.4 GiB
		t.Errorf("seeders/size = %d/%d, want 17/1503238553", r.Seeders, r.Size)
	}
	if r.Link != "https://nyaa.si/view/111" {
		t.Errorf("link = %q, want the view page (the guid), not the .torrent", r.Link)
	}
}

// With a Torznab endpoint configured too, nyaa answers BESIDE it: results
// merge endpoint-first and a hash both sources return appears once.
func TestSearchMergesNyaaBesideTheEndpoint(t *testing.T) {
	tz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>
<item><title>[Neko] Show - 07</title><link>http://x/1</link>
<newznab:attr name="infohash" value="aaaa"/><newznab:attr name="seeders" value="5"/></item>
</channel></rss>`))
	}))
	defer tz.Close()
	ny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?>
<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>
<item><title>[Neko] Show - 07</title><guid>https://nyaa.si/view/1</guid>
<nyaa:infoHash>AAAA</nyaa:infoHash><nyaa:seeders>50</nyaa:seeders>
<nyaa:categoryId>1_2</nyaa:categoryId><nyaa:size>1 GiB</nyaa:size><nyaa:remake>No</nyaa:remake></item>
<item><title>[Other] Show - 07</title><guid>https://nyaa.si/view/2</guid>
<nyaa:infoHash>bbbb</nyaa:infoHash><nyaa:seeders>9</nyaa:seeders>
<nyaa:categoryId>1_2</nyaa:categoryId><nyaa:size>1 GiB</nyaa:size><nyaa:remake>No</nyaa:remake></item>
</channel></rss>`))
	}))
	defer ny.Close()

	s := &torznabSearch{
		endpoint: tz.URL + "/api", key: "k", client: tz.Client(),
		nyaa: ny.Client(), nyaaURL: ny.URL + "/?q=",
	}
	res, err := s.Search(context.Background(), "Show", 0, 7)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d, want 2 — 'aaaa' from both sources must appear once", len(res))
	}
	if res[0].InfoHash != "aaaa" || res[0].Seeders != 5 {
		t.Errorf("endpoint result must lead the merge: %+v", res[0])
	}
	if res[1].InfoHash != "bbbb" {
		t.Errorf("nyaa-only result missing: %+v", res[1])
	}
}

// No key at all: nyaa alone makes the capability available — that is the
// point for a deployment that never configured nekoBT or Prowlarr.
func TestSearchAvailableWithNyaaAlone(t *testing.T) {
	ny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(nyaaSearchFeed))
	}))
	defer ny.Close()
	s := &torznabSearch{nyaa: ny.Client(), nyaaURL: ny.URL + "/?q="}
	if !s.Available() {
		t.Fatal("not available with a nyaa client and no key")
	}
	res, err := s.Search(context.Background(), "BEYBLADE X", 0, 92)
	if err != nil || len(res) != 1 {
		t.Fatalf("res=%d err=%v, want the one real hit", len(res), err)
	}
	// And with neither backend, unavailable — the pre-nyaa contract.
	if (&torznabSearch{}).Available() {
		t.Error("available with no backend at all")
	}
}

func TestParseNyaaSize(t *testing.T) {
	for in, want := range map[string]int64{
		"1.4 GiB": 1503238553, "700 MiB": 734003200, "512 KiB": 524288,
		"3 B": 3, "2 TiB": 2199023255552, "": 0, "weird": 0, "-1 GiB": 0,
	} {
		if got := parseNyaaSize(in); got != want {
			t.Errorf("parseNyaaSize(%q) = %d, want %d", in, got, want)
		}
	}
}
