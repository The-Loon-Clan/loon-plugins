package feeds

// Nyaa as a SEARCH source, beside the Torznab endpoint.
//
// Nyaa has no JSON API; its machine interface is the same RSS this plugin's
// importer already polls, and that RSS takes a q= parameter. The importer
// consumes the newest-items firehose, which means anything old enough to have
// scrolled off it is invisible to this site even though nyaa still has it —
// exactly the shape of an airing-gap backfill, where the missing episode is
// months old. Searching is what reaches it, and everything needed already
// lived here: the parser (parseNyaa — anime categories only, remakes
// dropped), the per-source client with its proxy hook, and the fetch helper
// with its size cap.
//
// No key, no configuration, on by default — the importer already talks to
// nyaa unconditionally, so searching it is not a new relationship with the
// site, just a second question asked of it. disable_nyaa_search turns it off
// for a deployment that wants its searches to stay wherever torznab_url
// points.

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

const nyaaSearchURL = "https://nyaa.si/?page=rss&c=1_0&q="

// nyaaQuery builds the text query nyaa's search actually matches. Torznab
// callers pass (query, season, episode) as separate fields; nyaa is
// full-text over release titles, where anime carries the episode as a bare
// number and almost never a season marker — so the season is deliberately
// dropped rather than turned into a token no fansub title contains.
func nyaaQuery(query string, episode int) string {
	q := strings.TrimSpace(query)
	if episode > 0 {
		q += " " + strconv.Itoa(episode)
	}
	return q
}

// nyaaPaddedQuery is the second try when the plain number finds nothing:
// "BEYBLADE X 92" and "BEYBLADE X 092" are different tokens to nyaa's
// search, and release groups pad to the magnitude of the run — two digits
// on a one-cour show ("- 05"), three on a long-runner ("- 092"). One
// alternative per magnitude, empty when there is no useful one.
func nyaaPaddedQuery(query string, episode int) string {
	q := strings.TrimSpace(query)
	switch {
	case episode <= 0 || episode >= 100:
		return ""
	case episode < 10:
		return fmt.Sprintf("%s %02d", q, episode)
	default:
		return fmt.Sprintf("%s %03d", q, episode)
	}
}

// parseNyaaSize turns the RSS's human size ("1.4 GiB") into bytes, zero when
// it cannot. Best-effort on purpose: size only breaks ties between equally
// seeded results (pickResurrectionResult), and a zero loses ties rather than
// corrupting anything.
func parseNyaaSize(s string) int64 {
	parts := strings.Fields(strings.TrimSpace(s))
	if len(parts) != 2 {
		return 0
	}
	n, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || n < 0 {
		return 0
	}
	var mult float64
	switch strings.ToLower(parts[1]) {
	case "b", "bytes":
		mult = 1
	case "kib", "kb":
		mult = 1 << 10
	case "mib", "mb":
		mult = 1 << 20
	case "gib", "gb":
		mult = 1 << 30
	case "tib", "tb":
		mult = 1 << 40
	default:
		return 0
	}
	return int64(n * mult)
}

// searchNyaa runs one query. The parser is the importer's own, so the same
// rules apply to search hits as to feed items: anime categories only,
// remakes dropped, info hashes lowercased.
func (t *torznabSearch) searchNyaa(ctx context.Context, query string) ([]lpapi.TorznabResult, error) {
	base := t.nyaaURL
	if base == "" {
		base = nyaaSearchURL
	}
	body, err := fetchRSS(ctx, t.nyaa, base+url.QueryEscape(query), "", 5<<20)
	if err != nil {
		return nil, err
	}
	items, err := parseNyaa(body)
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
			Size:     parseNyaaSize(it.sizeStr),
			Category: it.category,
		})
	}
	return out, nil
}
