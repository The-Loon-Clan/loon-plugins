package feeds

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon/bencode"
)

const (
	feedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	nyaaRSSURL    = "https://nyaa.si/?page=rss&c=1_0" // Anime - All
	anirenaRSSURL = "https://www.anirena.com/rss"
	// Tokyo Toshokan aggregates anime-adjacent torrents from many
	// sources (Nyaa, AniRena, AniDex, …). Filter type=1 = Anime
	// category only — without it the feed includes Raws (often
	// dupes of anime), Manga, Music, Hentai, etc. which would
	// pollute the request queue.
	tokyotoshoRSSURL = "https://www.tokyotosho.info/rss.php?filter=1"
)

// reMagnetInfoHash extracts the info_hash from a `magnet:?xt=urn:btih:HASH&...`
// URI. Used for feeds where the magnet link sits inside <description>
// rather than getting its own field — without parsing it out we can't dedup
// by info_hash and every item would re-queue every poll.
var reMagnetInfoHash = regexp.MustCompile(`(?i)urn:btih:([0-9a-f]{40}|[A-Z2-7]{32})`)

// reTokyoToshoSize pulls "Size: 403.35MB" / "Size: 1.2GB" / "Size: 42KB"
// out of Tokyo Toshokan's <description> HTML. Their RSS has no enclosure
// length attribute, so the size lives only in the description body. Spaces
// around the number are tolerated; unit is required.
var reTokyoToshoSize = regexp.MustCompile(`(?i)\bSize:\s*([\d.]+)\s*(B|KB|MB|GB|TB)\b`)

// fetchRSS is the shared GET half of every fetcher: browser UA (some of these
// hosts refuse obvious bots), bounded read, non-200 as error.
func fetchRSS(ctx context.Context, client *http.Client, feedURL, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", feedUserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// ── Nyaa RSS ────────────────────────────────────────────────────────────────

type nyaaRSS struct {
	XMLName xml.Name    `xml:"rss"`
	Channel nyaaChannel `xml:"channel"`
}

type nyaaChannel struct {
	Items []nyaaItem `xml:"item"`
}

type nyaaItem struct {
	Title      string `xml:"title"`
	Link       string `xml:"link"`
	GUID       string `xml:"guid"`
	Seeders    int    `xml:"seeders"`
	Leechers   int    `xml:"leechers"`
	Downloads  int    `xml:"downloads"`
	InfoHash   string `xml:"infoHash"`
	CategoryID string `xml:"categoryId"`
	Category   string `xml:"category"`
	Size       string `xml:"size"`
	Trusted    string `xml:"trusted"`
	Remake     string `xml:"remake"`
}

// parseNyaa keeps anime categories (1_*) and drops remakes (re-encodes).
func parseNyaa(body []byte) ([]item, error) {
	var feed nyaaRSS
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	var items []item
	for _, ni := range feed.Channel.Items {
		if !strings.HasPrefix(ni.CategoryID, "1_") {
			continue
		}
		if ni.Remake == "Yes" {
			continue
		}
		items = append(items, item{
			title:    ni.Title,
			link:     ni.GUID, // nyaa.si/view/ID
			infoHash: strings.ToLower(ni.InfoHash),
			seeders:  ni.Seeders,
			sizeStr:  ni.Size,
			category: ni.Category,
			source:   "nyaa",
		})
	}
	return items, nil
}

func (p *Plugin) fetchNyaa(ctx context.Context) ([]item, error) {
	body, err := fetchRSS(ctx, p.client, nyaaRSSURL, "", 5<<20)
	if err != nil {
		return nil, err
	}
	return parseNyaa(body)
}

// fetchNyaaSearch is the targeted-query variant of fetchNyaa. Instead of
// pulling the firehose anime feed, it asks Nyaa for results matching
// `query`, sorted by seeders descending. Returns up to `limit` items.
//
// Used by the top-searched discovery loop: the host's search analytics give
// us the queries members typed that returned zero grabs, this pulls the
// best-seeded torrent for each so the community can vote on whether it
// should be ingested. URL form is the same RSS endpoint as the firehose
// feed (page=rss) with q + s=seeders + o=desc applied.
func (p *Plugin) fetchNyaaSearch(ctx context.Context, query string, limit int) ([]item, error) {
	if limit <= 0 {
		limit = 5
	}
	u := "https://nyaa.si/?page=rss&c=1_0&s=seeders&o=desc&q=" + url.QueryEscape(query)
	body, err := fetchRSS(ctx, p.client, u, "", 5<<20)
	if err != nil {
		return nil, err
	}
	items, err := parseNyaa(body)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ── AniRena RSS ─────────────────────────────────────────────────────────────
//
// AniRena is a public anime-fansub-curated tracker; their RSS is plain
// RSS 2.0 with no Torznab attrs, no <infoHash>, no <seeders>. The
// info_hash has to be lifted out of the magnet URI that the site puts
// inside <description> (or sometimes <link>) — or, in their actual current
// shape, derived by fetching the .torrent enclosure. Items that yield no
// hash are skipped: without an info_hash the bot can't dedup, so logging
// them every poll would just grow feed_items without giving anyone
// anything useful.

type anirenaRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Channel anirenaChannel `xml:"channel"`
}

type anirenaChannel struct {
	Items []anirenaItem `xml:"item"`
}

type anirenaItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Enclosure   struct {
		URL    string `xml:"url,attr"`
		Length string `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
}

// anirenaEntry is parseAniRena's output: a fully-formed item when the hash
// was found inline, or one still needing the .torrent fallback.
type anirenaEntry struct {
	it item
	// torrentURL is set when no magnet was found anywhere but the enclosure
	// carries a .torrent URL — the fetch half derives the hash from it.
	torrentURL string
}

func parseAniRena(body []byte) ([]anirenaEntry, error) {
	var feed anirenaRSS
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	var out []anirenaEntry
	for _, ai := range feed.Channel.Items {
		// info_hash hunt — try description first (typical magnet URI),
		// then enclosure URL, then GUID, then link. First match wins.
		var hash string
		for _, blob := range []string{ai.Description, ai.Enclosure.URL, ai.GUID, ai.Link} {
			if m := reMagnetInfoHash.FindStringSubmatch(blob); m != nil {
				hash = strings.ToLower(m[1])
				break
			}
		}
		// AniRena's actual RSS shape (verified against live examples): no
		// magnet anywhere — only a .torrent enclosure URL like
		// https://www.anirena.com/torrents/<UUID>.torrent. Without the
		// info_hash the dedup gate trips and every item gets dropped on
		// every poll, so the fetch half falls back to downloading the
		// .torrent and SHA-1-ing its info dict.
		torrentURL := ""
		if hash == "" && ai.Enclosure.URL != "" && strings.HasSuffix(strings.ToLower(ai.Enclosure.URL), ".torrent") {
			torrentURL = ai.Enclosure.URL
		}
		if hash == "" && torrentURL == "" {
			continue
		}

		var size int64
		if ai.Enclosure.Length != "" {
			size, _ = strconv.ParseInt(ai.Enclosure.Length, 10, 64)
		}

		// AniRena's RSS doesn't expose a category, but the feed itself
		// is anime-only by site policy, so tag it that way for the
		// downstream filters. Source URL prefers <link> over <guid> —
		// link is the human-facing page, guid is sometimes a magnet.
		page := ai.Link
		if page == "" {
			page = ai.GUID
		}

		out = append(out, anirenaEntry{
			it: item{
				title:    ai.Title,
				link:     page,
				infoHash: hash,
				size:     size,
				category: "Anime",
				source:   "anirena",
			},
			torrentURL: torrentURL,
		})
	}
	return out, nil
}

func (p *Plugin) fetchAniRena(ctx context.Context) ([]item, error) {
	body, err := fetchRSS(ctx, p.client, anirenaRSSURL, "application/rss+xml, application/xml;q=0.9, */*;q=0.8", 5<<20)
	if err != nil {
		return nil, err
	}
	entries, err := parseAniRena(body)
	if err != nil {
		return nil, err
	}
	var items []item
	for _, e := range entries {
		if e.it.infoHash == "" {
			// One small HTTP per NEW item; AniRena ships ~30 items per feed
			// and most are repeats across polls, so the info_hash dedup at
			// the call site keeps repeated fetches from doing real work past
			// the first cycle. On any error the item is skipped rather than
			// filed with a hash that would never match.
			h, err := p.infoHashFromTorrentURL(ctx, e.torrentURL)
			if err != nil {
				continue
			}
			e.it.infoHash = h
		}
		items = append(items, e.it)
	}
	return items, nil
}

// infoHashFromTorrentURL downloads the .torrent file at u and computes its
// info_hash by SHA-1-ing the raw `info` dict bytes. Used by the AniRena path
// where the RSS feed only ships a .torrent URL (no magnet anywhere).
//
// The fetch goes through the whitelisted client: the URL comes from an
// upstream feed, not from this codebase, so it gets the SSRF dial guard and
// an anirena.com host pin rather than a blind GET. Bounded by a 4 MB read cap
// (a real .torrent shouldn't exceed that; refuses to allocate more).
func (p *Plugin) infoHashFromTorrentURL(ctx context.Context, torrentURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", torrentURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", feedUserAgent)
	req.Header.Set("Accept", "application/x-bittorrent, */*")
	resp, err := p.torrentClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("torrent fetch %s: status %d", torrentURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	h, err := bencode.InfoHash(body)
	if err != nil {
		return "", fmt.Errorf("torrent parse %s: %w", torrentURL, err)
	}
	return hex.EncodeToString(h[:]), nil
}

// ── Tokyo Toshokan RSS ──────────────────────────────────────────────────────
//
// Same RSS 2.0 shape as anirena, but the magnet sits inside the
// <description> CDATA and the size is a "Size: 403.35MB" plain-text line
// in the same blob. There's no enclosure element. Most items' <link>
// points to nyaa.si (Tokyo Toshokan is largely an aggregator); the
// info_hash dedup downstream collapses cross-source duplicates.

type tokyoToshoRSS struct {
	XMLName xml.Name          `xml:"rss"`
	Channel tokyoToshoChannel `xml:"channel"`
}

type tokyoToshoChannel struct {
	Items []tokyoToshoItem `xml:"item"`
}

type tokyoToshoItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Category    string `xml:"category"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

func parseTokyoTosho(body []byte) ([]item, error) {
	var feed tokyoToshoRSS
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	var items []item
	for _, it := range feed.Channel.Items {
		// Belt + suspenders on the filter=1 URL — the upstream
		// occasionally returns Raws / Hentai (Manga) in the Anime
		// filter feed; keep only Anime to match the catalog scope.
		cat := strings.TrimSpace(it.Category)
		if !strings.EqualFold(cat, "Anime") {
			continue
		}
		// info_hash from the magnet link in <description>. Tokyo
		// Toshokan always emits a magnet; skip the item if not.
		hash := ""
		if m := reMagnetInfoHash.FindStringSubmatch(it.Description); m != nil {
			hash = strings.ToLower(m[1])
		}
		if hash == "" {
			continue
		}
		// Size from the "Size: X.XX(KB|MB|GB)" plain-text line in
		// the description. Missing/unparseable size leaves it at 0
		// (the downstream filters tolerate 0 by treating size
		// rules as inert for that item).
		var size int64
		if m := reTokyoToshoSize.FindStringSubmatch(it.Description); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				var mul int64
				switch strings.ToUpper(m[2]) {
				case "B":
					mul = 1
				case "KB":
					mul = 1024
				case "MB":
					mul = 1024 * 1024
				case "GB":
					mul = 1024 * 1024 * 1024
				case "TB":
					mul = 1024 * 1024 * 1024 * 1024
				}
				size = int64(v * float64(mul))
			}
		}
		// Page URL — prefer GUID (Tokyo Toshokan details page),
		// fall back to link (often the upstream .torrent direct
		// download which is a worse human-facing URL).
		page := it.GUID
		if page == "" {
			page = it.Link
		}
		items = append(items, item{
			title:    it.Title,
			link:     page,
			infoHash: hash,
			size:     size,
			category: "Anime",
			source:   "tokyotosho",
		})
	}
	return items, nil
}

func (p *Plugin) fetchTokyoTosho(ctx context.Context) ([]item, error) {
	body, err := fetchRSS(ctx, p.client, tokyotoshoRSSURL, "application/rss+xml, application/xml;q=0.9, */*;q=0.8", 5<<20)
	if err != nil {
		return nil, err
	}
	return parseTokyoTosho(body)
}

// ── nekoBT Torznab ──────────────────────────────────────────────────────────

type torznabRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Channel torznabChannel `xml:"channel"`
}

type torznabChannel struct {
	Items []torznabItem `xml:"item"`
}

type torznabItem struct {
	Title     string        `xml:"title"`
	Link      string        `xml:"link"`
	GUID      string        `xml:"guid"`
	Size      int64         `xml:"size"`
	Category  string        `xml:"category"`
	Attrs     []torznabAttr `xml:"attr"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
	} `xml:"enclosure"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (ti *torznabItem) attr(name string) string {
	for _, a := range ti.Attrs {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

func parseTorznab(body []byte) ([]item, error) {
	var feed torznabRSS
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("xml: %w", err)
	}
	var items []item
	for _, ti := range feed.Channel.Items {
		hash := strings.ToLower(ti.attr("infohash"))
		if hash == "" {
			if mag := ti.attr("magneturl"); mag != "" {
				hash = extractHashFromMagnet(mag)
			}
		}
		seeders, _ := strconv.Atoi(ti.attr("seeders"))
		tvdbID, _ := strconv.Atoi(ti.attr("tvdbid"))
		tmdbID, _ := strconv.Atoi(ti.attr("tmdbid"))

		// Torrent page URL — <guid> contains the full URL.
		pageURL := ti.GUID
		if pageURL == "" {
			pageURL = ti.Link
		}

		sz := ti.Size
		if sz == 0 {
			if s, err := strconv.ParseInt(ti.attr("size"), 10, 64); err == nil {
				sz = s
			}
		}

		items = append(items, item{
			title:    ti.Title,
			link:     pageURL,
			infoHash: hash,
			seeders:  seeders,
			size:     sz,
			source:   "nekobt",
			tvdbID:   tvdbID,
			tmdbID:   tmdbID,
		})
	}
	return items, nil
}

func fetchNekoBTURL(ctx context.Context, client *http.Client, feedURL string) ([]item, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", feedUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return parseTorznab(body)
}

func (p *Plugin) fetchNekoBT(ctx context.Context) ([]item, error) {
	return fetchNekoBTURL(ctx, p.client, fmt.Sprintf("https://nekobt.to/api/torznab/api?t=search&apikey=%s", p.cfg.NekoBTAPIKey))
}

func extractHashFromMagnet(magnet string) string {
	lower := strings.ToLower(magnet)
	idx := strings.Index(lower, "btih:")
	if idx < 0 {
		return ""
	}
	h := magnet[idx+5:]
	if amp := strings.IndexByte(h, '&'); amp >= 0 {
		h = h[:amp]
	}
	return strings.ToLower(strings.TrimSpace(h))
}
