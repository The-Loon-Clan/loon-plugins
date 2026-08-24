package trackersearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/trackerdir"
)

// Fixtures are shaped from the REAL responses captured 24 Aug 2026 -- field
// names, string-typed numbers and all -- because a test against a tidied-up
// imaginary API passes forever while the adapter fails against the one that
// exists.

const knabenFixture = `{"total":{"value":12},"hits":[
 {"title":"The Ark S03E04 XviD-AFG","bytes":435148554,"seeders":30,"peers":11,
  "hash":"2F0A92A8FB973932C18977C6E003C0FD41FFC985",
  "magnetUrl":"magnet:?xt=urn:btih:2F0A92A8FB973932C18977C6E003C0FD41FFC985",
  "details":"https://knaben.xyz/thepiratebay/description.php?id=84198292",
  "date":"2026-08-22T18:55:00+00:00","tracker":"The Pirate Bay","virusDetection":0.18},
 {"title":"The.Ark.S03E04.TOTALLY.A.VIRUS.exe","bytes":1024,"seeders":999,"peers":0,
  "hash":"AAAA","magnetUrl":"magnet:?xt=urn:btih:AAAA","details":"",
  "date":"2026-08-22T00:00:00+00:00","tracker":"Shady","virusDetection":0.97}
]}`

const torrentsCSVFixture = `{"torrents":[
 {"infohash":"4b30c6fb57d7807e3c7a6ad6a17821dd1e629da6",
  "name":"Succession S02E04 Safe Room 1080p AMZN WEB-DL DDP5 1 H 264-NTb",
  "size_bytes":4640953641,"created_unix":1711901700,"seeders":3,"leechers":1,"completed":11}
],"next":null}`

const eztvFixture = `{"torrents_count":3,"torrents":[
 {"title":"The Ark S03E04 720p x264","filename":"the.ark.s03e04.mkv",
  "hash":"beef01","magnet_url":"magnet:?xt=urn:btih:beef01",
  "season":"3","episode":"4","size_bytes":"514986736","seeds":12,"peers":4,
  "date_released_unix":1787606415},
 {"title":"The Ark S03E05 720p x264","filename":"the.ark.s03e05.mkv",
  "hash":"beef02","magnet_url":"magnet:?xt=urn:btih:beef02",
  "season":"3","episode":"5","size_bytes":"1","seeds":99,"peers":1,
  "date_released_unix":1787606415},
 {"title":"The Ark S01E04 720p x264","filename":"the.ark.s01e04.mkv",
  "hash":"beef03","magnet_url":"magnet:?xt=urn:btih:beef03",
  "season":"1","episode":"4","size_bytes":"2","seeds":98,"peers":1,
  "date_released_unix":1787606415}
]}`

func q() pluginapi.EpisodeSearch {
	return pluginapi.EpisodeSearch{ShowTitle: "The Ark", Season: 3, Episode: 4, IMDbID: "tt15039982"}
}

func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A flagged upload is not a candidate, whatever its swarm says.
func TestKnabenDropsVirusFlaggedHits(t *testing.T) {
	srv := serve(t, knabenFixture)
	k := &knaben{http: srv.Client(), url: srv.URL}
	got, err := k.Search(context.Background(), q())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want the one clean hit, got %d", len(got))
	}
	c := got[0]
	if c.Via != "The Pirate Bay" || c.Seeders != 30 || c.SizeBytes != 435148554 {
		t.Fatalf("clean hit misparsed: %+v", c)
	}
	if c.PostedAt.IsZero() {
		t.Fatal("date was parseable and must be carried")
	}
}

// The query knaben receives carries the episode code, not just the title.
func TestKnabenAsksForTheEpisode(t *testing.T) {
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		asked.Store(body["query"])
		fmt.Fprint(w, `{"hits":[]}`)
	}))
	defer srv.Close()
	k := &knaben{http: srv.Client(), url: srv.URL}
	if _, err := k.Search(context.Background(), q()); err != nil {
		t.Fatal(err)
	}
	if got := asked.Load(); got != "The Ark S03E04" {
		t.Fatalf("asked %q, want the title plus the code", got)
	}
}

func TestTorrentsCSVBuildsAMagnetFromTheInfohash(t *testing.T) {
	srv := serve(t, torrentsCSVFixture)
	a := &torrentsCSV{http: srv.Client(), url: srv.URL}
	got, err := a.Search(context.Background(), q())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].SizeBytes != 4640953641 {
		t.Fatalf("a 4.6GB size must survive parsing, got %d", got[0].SizeBytes)
	}
	if !strings.HasPrefix(got[0].Magnet, "magnet:?xt=urn:btih:4b30c6fb") {
		t.Fatalf("no magnet assembled: %q", got[0].Magnet)
	}
}

// EZTV answers a whole show; only the asked episode may come back.
func TestEZTVFiltersToTheAskedEpisode(t *testing.T) {
	srv := serve(t, eztvFixture)
	e := &eztv{http: srv.Client(), url: srv.URL}
	got, err := e.Search(context.Background(), q())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("S03E05 and S01E04 must be filtered out; got %d hits", len(got))
	}
	c := got[0]
	if c.InfoHash != "beef01" || c.SizeBytes != 514986736 || c.Seeders != 12 {
		t.Fatalf("misparsed: %+v", c)
	}
}

// No IMDb id means EZTV cannot be asked -- and that is a nil, not an error.
func TestEZTVDeclinesWithoutAnIMDbID(t *testing.T) {
	e := &eztv{http: http.DefaultClient, url: "http://127.0.0.1:1"}
	query := q()
	query.IMDbID = ""
	got, err := e.Search(context.Background(), query)
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) -- the endpoint must not even be dialled; got (%v, %v)", got, err)
	}
}

// One dead source must not turn a found episode into an error page.
func TestOneFailingSourceDoesNotFailTheSearch(t *testing.T) {
	good := serve(t, knabenFixture)
	c := &Client{
		lastErr: map[string]string{}, nextAt: map[string]time.Time{},
		delay: map[string]time.Duration{"knaben": 0, "torrentscsv": 0},
	}
	c.adapters = []adapter{
		&knaben{http: good.Client(), url: good.URL},
		&torrentsCSV{http: &http.Client{Timeout: 200 * time.Millisecond}, url: "http://127.0.0.1:1"},
	}
	got, err := c.SearchEpisode(context.Background(), q())
	if err != nil {
		t.Fatalf("the search as a whole must survive: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the healthy source's hit must come through, got %d", len(got))
	}
	var sawErr bool
	for _, s := range c.Sources() {
		if s.Slug == "torrentscsv" && s.LastErr != "" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("the failure must be readable from Sources, or silence looks like absence")
	}
}

// Best-first means the healthiest swarm leads.
func TestCandidatesAreSortedBySwarmHealth(t *testing.T) {
	multi := serve(t, `{"hits":[
	 {"title":"weak","bytes":10,"seeders":1,"peers":0,"hash":"01","magnetUrl":"m","details":"","date":"2026-08-22T00:00:00+00:00","tracker":"x","virusDetection":0.1},
	 {"title":"strong","bytes":10,"seeders":50,"peers":9,"hash":"02","magnetUrl":"m","details":"","date":"2026-08-22T00:00:00+00:00","tracker":"x","virusDetection":0.1}
	]}`)
	c := &Client{
		lastErr: map[string]string{}, nextAt: map[string]time.Time{},
		delay: map[string]time.Duration{"knaben": 0},
	}
	c.adapters = []adapter{&knaben{http: multi.Client(), url: multi.URL}}
	got, err := c.SearchEpisode(context.Background(), q())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if got[0].Title != "strong" {
		t.Fatalf("the healthy swarm must lead; got %q first", got[0].Title)
	}
}

// Two searches back to back must respect the per-source spacing.
func TestPolitenessSpacesRequestsToOneSource(t *testing.T) {
	var stamps []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stamps = append(stamps, time.Now())
		fmt.Fprint(w, `{"hits":[]}`)
	}))
	defer srv.Close()
	c := &Client{
		lastErr: map[string]string{}, nextAt: map[string]time.Time{},
		delay: map[string]time.Duration{"knaben": 150 * time.Millisecond},
	}
	c.adapters = []adapter{&knaben{http: srv.Client(), url: srv.URL}}
	ctx := context.Background()
	c.SearchEpisode(ctx, q())
	c.SearchEpisode(ctx, q())
	if len(stamps) != 2 {
		t.Fatalf("want 2 requests, got %d", len(stamps))
	}
	if gap := stamps[1].Sub(stamps[0]); gap < 140*time.Millisecond {
		t.Fatalf("second request after %v; the polite spacing is not being kept", gap)
	}
}

// The wired set is primed from the directory, delays floored at two seconds.
func TestNewPrimesPolitenessFromTheDirectory(t *testing.T) {
	c := New()
	if len(c.adapters) != 3 {
		t.Fatalf("want the 3 public adapters, got %d", len(c.adapters))
	}
	for slug, d := range c.delay {
		if d < politeFloor {
			t.Fatalf("%s: delay %v is under the polite floor", slug, d)
		}
	}
	// nyaa-style declared delays must survive the floor, not be replaced by
	// it -- checked indirectly: every wired slug exists in the directory.
	for _, a := range c.adapters {
		if _, ok := trackerdir.BySlug(a.Slug()); !ok {
			t.Fatalf("%s is wired but not in the directory", a.Slug())
		}
	}
}

// An empty question is refused rather than broadcast.
func TestAnEmptyQueryIsRefused(t *testing.T) {
	c := New()
	if _, err := c.SearchEpisode(context.Background(), pluginapi.EpisodeSearch{Season: 1, Episode: 1}); err == nil {
		t.Fatal("an empty query must not fan out to real trackers")
	}
}
