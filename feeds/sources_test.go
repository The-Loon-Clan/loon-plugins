package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nyaa's real feed namespaces its custom elements (xmlns:nyaa); Go's
// encoding/xml matches on local name, which is what the parser relies on.
// The fixture keeps the namespace so that assumption is actually exercised.
const nyaaFixture = `<?xml version="1.0" encoding="utf-8"?>
<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa" version="2.0">
<channel>
 <item>
  <title>[SubsPlease] Show A - 05 (1080p) [ABCD1234].mkv</title>
  <link>https://nyaa.si/download/1900001.torrent</link>
  <guid isPermaLink="true">https://nyaa.si/view/1900001</guid>
  <nyaa:seeders>120</nyaa:seeders>
  <nyaa:infoHash>AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA</nyaa:infoHash>
  <nyaa:categoryId>1_2</nyaa:categoryId>
  <nyaa:category>Anime - English-translated</nyaa:category>
  <nyaa:size>1.4 GiB</nyaa:size>
  <nyaa:remake>No</nyaa:remake>
 </item>
 <item>
  <title>Some Software 2.0</title>
  <guid>https://nyaa.si/view/1900002</guid>
  <nyaa:categoryId>6_2</nyaa:categoryId>
  <nyaa:remake>No</nyaa:remake>
 </item>
 <item>
  <title>[ReEncodes] Show A - 05 (720p)</title>
  <guid>https://nyaa.si/view/1900003</guid>
  <nyaa:categoryId>1_2</nyaa:categoryId>
  <nyaa:remake>Yes</nyaa:remake>
 </item>
</channel>
</rss>`

func TestParseNyaaFiltersToAnimeAndDropsRemakes(t *testing.T) {
	items, err := parseNyaa([]byte(nyaaFixture))
	if err != nil {
		t.Fatalf("parseNyaa: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (non-anime and remake rows dropped): %+v", len(items), items)
	}
	it := items[0]
	if it.infoHash != strings.ToLower("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Errorf("infoHash = %q — must be lowercased, it is the dedup key", it.infoHash)
	}
	if it.link != "https://nyaa.si/view/1900001" {
		t.Errorf("link = %q, want the GUID (the view page), not the .torrent download", it.link)
	}
	if it.seeders != 120 || it.sizeStr != "1.4 GiB" || it.source != "nyaa" {
		t.Errorf("item fields wrong: %+v", it)
	}
	if it.category != "Anime - English-translated" {
		t.Errorf("category = %q", it.category)
	}
}

const anirenaFixture = `<?xml version="1.0"?>
<rss version="2.0">
<channel>
 <item>
  <title>[Group] Magnet Show - 01</title>
  <link>https://www.anirena.com/viewtracker.php?action=details&amp;id=111</link>
  <description>magnet:?xt=urn:btih:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB&amp;dn=x</description>
 </item>
 <item>
  <title>[Group] Torrent-Only Show - 02</title>
  <link>https://www.anirena.com/viewtracker.php?action=details&amp;id=222</link>
  <enclosure url="https://www.anirena.com/torrents/deadbeef.torrent" length="734003200" type="application/x-bittorrent"/>
 </item>
 <item>
  <title>[Group] Hopeless Item - 03</title>
  <link>https://www.anirena.com/viewtracker.php?action=details&amp;id=333</link>
  <description>no magnet here</description>
 </item>
</channel>
</rss>`

func TestParseAniRenaHashSources(t *testing.T) {
	entries, err := parseAniRena([]byte(anirenaFixture))
	if err != nil {
		t.Fatalf("parseAniRena: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 — the item with no magnet and no .torrent is unusable (no dedup key) and must be dropped", len(entries))
	}

	withMagnet := entries[0]
	if withMagnet.it.infoHash != strings.ToLower("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB") {
		t.Errorf("magnet item hash = %q", withMagnet.it.infoHash)
	}
	if withMagnet.torrentURL != "" {
		t.Errorf("magnet item should not need the .torrent fallback")
	}
	if withMagnet.it.category != "Anime" || withMagnet.it.source != "anirena" {
		t.Errorf("item tagging wrong: %+v", withMagnet.it)
	}

	needsFetch := entries[1]
	if needsFetch.it.infoHash != "" {
		t.Errorf("torrent-only item should have no inline hash, got %q", needsFetch.it.infoHash)
	}
	if needsFetch.torrentURL != "https://www.anirena.com/torrents/deadbeef.torrent" {
		t.Errorf("torrentURL = %q — the fetch half needs it for the fallback", needsFetch.torrentURL)
	}
	if needsFetch.it.size != 734003200 {
		t.Errorf("size = %d, want the enclosure length", needsFetch.it.size)
	}
}

// The same minimal-but-real torrent used by loon/bencode's own tests. The
// fallback must return the SHA-1 of the info span, hex-encoded lowercase.
const torrentFixture = "d8:announce19:http://tracker/annc4:infod6:lengthi1024e4:name8:Some.mkv12:piece lengthi16384eee"

func TestInfoHashFromTorrentURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.torrent":
			_, _ = w.Write([]byte(torrentFixture))
		case "/junk.torrent":
			_, _ = w.Write([]byte("this is not bencode"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Plugin{torrentClient: srv.Client()}

	h, err := p.infoHashFromTorrentURL(context.Background(), srv.URL+"/ok.torrent")
	if err != nil {
		t.Fatalf("ok.torrent: %v", err)
	}
	if len(h) != 40 || strings.ToLower(h) != h {
		t.Errorf("hash = %q, want 40 lowercase hex chars", h)
	}

	if _, err := p.infoHashFromTorrentURL(context.Background(), srv.URL+"/junk.torrent"); err == nil {
		t.Error("junk body produced a hash — that hash would dedup unrelated items onto one key")
	}
	if _, err := p.infoHashFromTorrentURL(context.Background(), srv.URL+"/missing.torrent"); err == nil {
		t.Error("404 produced a hash")
	}
}

const tokyoToshoFixture = `<?xml version="1.0"?>
<rss version="2.0">
<channel>
 <item>
  <title>[Group] Tosho Show - 07</title>
  <link>https://nyaa.si/download/1900042.torrent</link>
  <guid isPermaLink="true">https://www.tokyotosho.info/details.php?id=42</guid>
  <category>Anime</category>
  <description><![CDATA[<a href="magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc&tr=x">Magnet</a> :: Size: 403.35MB | Authorized: Yes]]></description>
 </item>
 <item>
  <title>Filtered Manga Thing</title>
  <guid>https://www.tokyotosho.info/details.php?id=43</guid>
  <category>Manga</category>
  <description><![CDATA[magnet:?xt=urn:btih:dddddddddddddddddddddddddddddddddddddddd]]></description>
 </item>
 <item>
  <title>[Group] No Magnet Item</title>
  <guid>https://www.tokyotosho.info/details.php?id=44</guid>
  <category>Anime</category>
  <description>Size: 1.2GB</description>
 </item>
</channel>
</rss>`

func TestParseTokyoTosho(t *testing.T) {
	items, err := parseTokyoTosho([]byte(tokyoToshoFixture))
	if err != nil {
		t.Fatalf("parseTokyoTosho: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (non-Anime category and magnet-less rows dropped)", len(items))
	}
	it := items[0]
	if it.infoHash != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("infoHash = %q", it.infoHash)
	}
	// 403.35 MB → bytes, truncated through the float multiply. Computed via a
	// variable because a constant expression with a fractional part will not
	// convert to int64 at compile time.
	mb := float64(1024 * 1024)
	want := int64(403.35 * mb)
	if it.size != want {
		t.Errorf("size = %d, want %d (parsed from the description text)", it.size, want)
	}
	if it.link != "https://www.tokyotosho.info/details.php?id=42" {
		t.Errorf("link = %q, want the GUID details page, not the .torrent link", it.link)
	}
}

const torznabFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss xmlns:torznab="http://torznab.com/schemas/2015/feed" version="2.0">
<channel>
 <item>
  <title>Neko Show S02E04 1080p WEB-DL</title>
  <link>https://nekobt.to/torrents/9001/download</link>
  <guid>https://nekobt.to/torrents/9001</guid>
  <size>1500000000</size>
  <torznab:attr name="infohash" value="EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"/>
  <torznab:attr name="seeders" value="33"/>
  <torznab:attr name="tvdbid" value="123456"/>
  <torznab:attr name="tmdbid" value="654321"/>
 </item>
 <item>
  <title>Magnet Fallback Item</title>
  <link>https://nekobt.to/torrents/9002</link>
  <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF&amp;dn=x"/>
  <torznab:attr name="size" value="2000"/>
 </item>
</channel>
</rss>`

func TestParseTorznab(t *testing.T) {
	items, err := parseTorznab([]byte(torznabFixture))
	if err != nil {
		t.Fatalf("parseTorznab: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	first := items[0]
	if first.infoHash != strings.ToLower("EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE") {
		t.Errorf("infohash attr not read/lowered: %q", first.infoHash)
	}
	if first.seeders != 33 || first.tvdbID != 123456 || first.tmdbID != 654321 {
		t.Errorf("attrs wrong: %+v", first)
	}
	if first.link != "https://nekobt.to/torrents/9001" {
		t.Errorf("link = %q, want the GUID page URL", first.link)
	}
	if first.size != 1500000000 {
		t.Errorf("size = %d, want the <size> element", first.size)
	}

	second := items[1]
	if second.infoHash != strings.ToLower("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF") {
		t.Errorf("magneturl fallback not used: %q", second.infoHash)
	}
	if second.size != 2000 {
		t.Errorf("size = %d, want the size attr fallback when <size> is absent", second.size)
	}
}
