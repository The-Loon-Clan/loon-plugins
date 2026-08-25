//go:build integration

package usenet

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The Newznab surface is the plugin's one EXTERNAL interop contract — *arr
// clients parse this XML — and it had zero tests until the 2026-07-24 release
// review. These pin the load-bearing parts: caps advertises limits it now
// actually enforces, untrusted release titles are XML-escaped everywhere they
// appear, the apikey echoes back escaped inside download links, and t=get
// round-trips the stored NZB bytes.
//
//	go test -tags=integration -count=1 -run Newznab ./usenet/
func TestNewznabContract(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	svc := &service{store: s, retentionDays: 100}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(`<nzb><file subject="a"><segments><segment>x@y</segment></segments></file></nzb>`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	hostile := `Evil <Title> & "Friends" 'S01'`
	for i, n := range []nzbRow{
		{Title: hostile, Filename: "evil.nzb", Size: 1 << 20, Group: "alt.binaries.anime",
			ContentHash: "hash-evil-0001", Posted: time.Now().Add(-time.Hour), Data: gz.Bytes(), CategoryID: 5070},
		{Title: "Plain.Release.1080p", Filename: "plain.nzb", Size: 2 << 20, Group: "alt.binaries.anime",
			ContentHash: "hash-plain-0002", Posted: time.Now(), Data: gz.Bytes(), CategoryID: 5070},
	} {
		if _, ok, err := s.insertNzb(ctx, n); err != nil || !ok {
			t.Fatalf("seed %d: ok=%v err=%v", i, ok, err)
		}
	}

	t.Run("caps advertises the enforced limit", func(t *testing.T) {
		res, err := svc.Newznab(ctx, pluginapi.NewznabRequest{Function: "caps", BaseURL: "http://x", Title: "T"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(res.Body), `<limits max="100" default="50"/>`) {
			t.Errorf("caps missing the limits line:\n%s", res.Body)
		}
	})

	t.Run("feed escapes untrusted titles and the apikey", func(t *testing.T) {
		res, err := svc.Newznab(ctx, pluginapi.NewznabRequest{
			Function: "search", BaseURL: "http://x", Title: "T",
			APIKey: `k&"<evil>`, Limit: 100000, Offset: -5,
		})
		if err != nil {
			t.Fatal(err)
		}
		body := string(res.Body)
		if strings.Contains(body, hostile) {
			t.Error("raw hostile title leaked into the XML")
		}
		if !strings.Contains(body, "Evil &lt;Title&gt; &amp; &quot;Friends&quot; &apos;S01&apos;") {
			t.Errorf("escaped title missing:\n%s", body)
		}
		if strings.Contains(body, `apikey=k&"`) {
			t.Error("raw apikey metacharacters leaked into a link")
		}
		// URL-encoded first (QueryEscape — the link must stay a valid URL),
		// then XML-escaped by the template like every other interpolation.
		if !strings.Contains(body, "apikey=k%26%22%3Cevil%3E") {
			t.Errorf("url-encoded apikey missing from download links:\n%s", body)
		}
		// The hostile Limit/Offset were clamped, not an SQL error, and the
		// clamped offset is what the response reports.
		if !strings.Contains(body, `offset="0"`) {
			t.Errorf("clamped offset not reported:\n%s", body)
		}
		if !strings.Contains(body, `total="2"`) {
			t.Errorf("expected both seeded releases:\n%s", body)
		}
	})

	t.Run("get round-trips the NZB bytes", func(t *testing.T) {
		feed, err := svc.Newznab(ctx, pluginapi.NewznabRequest{Function: "rss", BaseURL: "http://x"})
		if err != nil {
			t.Fatal(err)
		}
		// Pull an id out of the feed rather than guessing serial values.
		body := string(feed.Body)
		i := strings.Index(body, "t=get&amp;id=")
		if i < 0 {
			t.Fatalf("no download link in feed:\n%s", body)
		}
		id := body[i+len("t=get&amp;id="):]
		id = id[:strings.IndexAny(id, `"<&`)]
		res, err := svc.Newznab(ctx, pluginapi.NewznabRequest{Function: "get", ID: id})
		if err != nil {
			t.Fatal(err)
		}
		if res.ContentType != "application/x-nzb" || len(res.Body) == 0 || res.Filename == "" {
			t.Errorf("get: ct=%q len=%d filename=%q", res.ContentType, len(res.Body), res.Filename)
		}
	})

	t.Run("unknown function answers a newznab error, not a 500", func(t *testing.T) {
		res, err := svc.Newznab(ctx, pluginapi.NewznabRequest{Function: "explode"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(res.Body), `code="202"`) {
			t.Errorf("want newznab error 202, got:\n%s", res.Body)
		}
	})
}

// A parent Newznab category id must reach subcategory-filed releases: caps
// advertises 5000, Prowlarr's connectivity test queries it, and categorize
// files everything under subcats — the exact IN(5000) answered total=0 for
// an indexer full of TV. Runs with a nil catalog, so the expansion's
// fallbackCats path is exercised end to end through the real catClause SQL,
// page and COUNT both.
func TestNewznabParentCategoryReachesSubcatReleases(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	svc := &service{store: s, retentionDays: 100}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("<nzb/>"))
	_ = zw.Close()
	for i, title := range []string{"Frieren.S01E01.1080p", "Frieren.S01E02.1080p"} {
		if _, ok, err := s.insertNzb(ctx, nzbRow{
			Title: title, Filename: title + ".nzb", Size: 1 << 30,
			Group: "a.b.anime", ContentHash: fmt.Sprintf("hash-cat-%04d", i),
			Posted: time.Now(), Data: gz.Bytes(), CategoryID: 5070,
		}); err != nil || !ok {
			t.Fatalf("seed %d: ok=%v err=%v", i, ok, err)
		}
	}

	res, err := svc.Newznab(ctx, pluginapi.NewznabRequest{
		Function: "tvsearch", Categories: []int{5000},
		BaseURL: "http://x", Title: "T", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(res.Body)
	if !strings.Contains(body, `total="2"`) || !strings.Contains(body, "Frieren.S01E01.1080p") {
		t.Errorf("cat=5000 did not reach the 5070-filed releases:\n%s", body)
	}

	// A parent with no matching children stays empty — expansion must not
	// become match-everything.
	res, err = svc.Newznab(ctx, pluginapi.NewznabRequest{
		Function: "movie", Categories: []int{2000},
		BaseURL: "http://x", Title: "T", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Body), `total="0"`) {
		t.Errorf("cat=2000 matched something on a TV-only index:\n%s", res.Body)
	}
}
