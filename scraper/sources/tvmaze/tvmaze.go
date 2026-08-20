// Package tvmaze is an API-search catalog.MetadataSource backed by TVmaze
// (https://api.tvmaze.com) — the tv domain, with NO credential.
//
// It exists so a host that has not obtained a TMDB key still gets series art
// and summaries. TVmaze is purpose-built for television and returns everything
// a release page wants in one call: poster (medium and original), an HTML
// summary, premiere date, genres, network and the IMDb id.
//
//	reg.RegisterSource(tvmaze.New(""))
//
// It serves the SAME domain key as the TMDB tv source, and catalog.Registry
// refuses a duplicate key — so a host registers one or the other, not both.
// See main.go: TMDB wins when its key is set, because it carries backdrops and
// a much larger non-English catalogue.
//
// Rate limit: TVmaze documents "at least 20 calls every 10 seconds per IP" and
// answers 429 beyond it. Since there is no key, an over-eager client is
// throttled by IP and takes every other client on the same address down with
// it, so this source serialises its own requests behind a minimum interval
// rather than trusting the caller's concurrency to stay polite.
package tvmaze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/httpclient"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ErrNoLocalID is returned by Fetch — this source is query-only.
var ErrNoLocalID = errors.New("tvmaze: no local id space (use Search)")

const (
	defaultBaseURL = "https://api.tvmaze.com"
	// minInterval keeps this client under TVmaze's documented 20 calls per 10
	// seconds. 600ms leaves headroom for the clock skew between "when we sent"
	// and "when they counted".
	minInterval = 600 * time.Millisecond
	userAgent   = "loon-scraper/1.0 (+https://github.com/the-loon-clan)"
)

// Source is the TVmaze metadata source for the "tv" domain.
type Source struct {
	baseURL string
	http    *http.Client

	// mu serialises requests and holds the last send time — see the package
	// comment on why politeness is this source's own job.
	mu   sync.Mutex
	last time.Time

	// seen caches one lookup per SERIES name, because the job asks per
	// RELEASE. 87,768 TV releases on the reference index reduce to 6,797
	// distinct series — a 13x difference, and at the 600ms self-throttle that
	// is 68 minutes of requests instead of 14.6 hours.
	//
	// Misses are cached too, as a zero entry. Without that, every unmatchable
	// release name re-asks TVmaze forever, and unmatchable names are the ones
	// a crawler produces most of.
	//
	// Process-lifetime and unbounded by design: the key space is distinct
	// series names, the job is idempotent, and a restart simply re-warms it.
	cacheMu sync.RWMutex
	seen    map[string]cached
}

// cached is one remembered lookup. ok=false records a miss.
type cached struct {
	entry catalog.CatalogEntry
	ok    bool
}

var _ catalog.MetadataSource = (*Source)(nil)

// New builds the source. Never returns nil: there is nothing to configure.
func New(baseURL string) *Source {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// httpclient.NewAPI rather than a bespoke &http.Client{}. core's
	// HTTPClientService contract is explicit that "raw &http.Client{} is
	// forbidden in plugin code" — seven sources each building their own is
	// exactly the sprawl pkg/httpclient was written to end (it counted 21
	// such places). NewAPI shares one pooled transport across every source,
	// so a scrape of fifty releases reuses connections instead of opening a
	// fresh one per lookup, and any future outbound policy lands in one file.
	//
	// NOT SafeFetch: that carries an SSRF dial guard for URLs a MEMBER
	// supplied, and would refuse the loopback address these tests point at.
	// The host here is a fixed, operator-configured API endpoint.
	return &Source{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    httpclient.NewAPI(),
		seen:    map[string]cached{},
	}
}

// Domain returns the tv domain, at the same priority the TMDB tv source uses —
// they are alternatives for one slot, so a differing number would only be
// misleading.
func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "tv", UnitNoun: "series", Priority: 55}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// Fetch is unsupported: TVmaze ids ride on CatalogEntry.External.
func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

// Normalize keeps the domain-neutral cleaner.
func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// ---------------------------------------------------------------------------
// Release-name parsing
// ---------------------------------------------------------------------------

var (
	// The series name ends at the season/episode marker. Everything after it is
	// episode identity and encoding decoration, and TVmaze searches SERIES.
	reSeasonEp = regexp.MustCompile(`(?i)\b(s\d{1,2}\s*e\d{1,3}|s\d{1,2}\b|\d{1,2}x\d{2}\b|\bseason\b)`)
	reBracket  = regexp.MustCompile(`[\[\{\(][^\]\}\)]*[\]\}\)]`)
	reYear     = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	// Quality/source/codec noise that survives the season cut on odd namings.
	reNoise = regexp.MustCompile(`(?i)\b(1080p|2160p|720p|480p|web-?dl|webrip|bluray|blu-ray|hdtv|dvdrip|x26[45]|h\.?26[45]|hevc|avc|aac\d?(\.\d)?|ddp?\d(\.\d)?|atmos|dts(-hd)?|remux|proper|repack|internal|multi|dual|subbed|dubbed|complete)\b`)
	reSpace = regexp.MustCompile(`\s+`)
	reTags  = regexp.MustCompile(`<[^>]*>`)
)

// Query is the series name recovered from a release name, plus a year hint when
// the posting disambiguated with one ("Doctor Who 2005").
type Query struct {
	Title string
	Year  int
}

// ParseReleaseName recovers a searchable SERIES name from a release name.
func ParseReleaseName(raw string) Query {
	s := strings.ReplaceAll(raw, "_", " ")
	s = reBracket.ReplaceAllString(s, " ")

	// Cut at the season/episode marker first — it is the most reliable boundary
	// between the series name and everything the poster added.
	if loc := reSeasonEp.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	// Dots are the scene's word separator. Done AFTER the season cut so "S01E02"
	// stays intact for the matcher above.
	s = strings.ReplaceAll(s, ".", " ")

	var year int
	if m := reYear.FindString(s); m != "" {
		year, _ = strconv.Atoi(m)
		// The year is kept in the query: TVmaze ranks "Doctor Who 2005" better
		// WITH it, unlike TMDB where it is a separate filter.
	}
	s = reNoise.ReplaceAllString(s, " ")
	s = strings.Trim(reSpace.ReplaceAllString(s, " "), " -_")
	return Query{Title: s, Year: year}
}

// stripHTML turns TVmaze's HTML summary into plain text. Summaries arrive as
// "<p><b>Show</b> follows ...</p>" and a release page renders text, not markup:
// storing the tags would either show them literally or hand raw third-party
// HTML to a template that trusts it.
func stripHTML(s string) string {
	s = reTags.ReplaceAllString(s, "")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(s)
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

type show struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Language  string   `json:"language"`
	Genres    []string `json:"genres"`
	Status    string   `json:"status"`
	Premiered string   `json:"premiered"`
	Ended     string   `json:"ended"`
	Runtime   int      `json:"runtime"`
	Summary   string   `json:"summary"`
	Image     struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
	Network struct {
		Name string `json:"name"`
	} `json:"network"`
	WebChannel struct {
		Name string `json:"name"`
	} `json:"webChannel"`
	Rating struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Externals struct {
		IMDB    string `json:"imdb"`
		TheTVDB int64  `json:"thetvdb"`
	} `json:"externals"`
}

// wait blocks until this source may send again. See the package comment.
func (s *Source) wait(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := minInterval - time.Since(s.last); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	s.last = time.Now()
	return nil
}

// Search identifies a series from a release name. ok=false with a nil error
// means "no match" — never an error.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	q := ParseReleaseName(query)
	if q.Title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	// The cache key is the parsed SERIES name, not the release name — every
	// episode of a show asks the same question, and the answer is the show.
	key := strings.ToLower(q.Title)
	if c, hit := s.lookup(key); hit {
		return c.entry, c.ok, nil
	}
	if err := s.wait(ctx); err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	// singlesearch returns the ONE best match as a bare object, or 404 when
	// nothing matches — which is why 404 is handled as "no match" below rather
	// than as an error.
	endpoint := fmt.Sprintf("%s/singlesearch/shows?q=%s", s.baseURL, url.QueryEscape(q.Title))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("tvmaze request: %w", pluginapi.RedactURLError(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// A definite "no such series" — worth remembering. The cases below are
		// not: a 429 or a transport error says nothing about this title, and
		// caching one would turn a blip into a permanent hole.
		s.remember(key, catalog.CatalogEntry{}, false)
		return catalog.CatalogEntry{}, false, nil
	case http.StatusTooManyRequests:
		// Say so plainly. A keyless service throttles by IP, so this is the one
		// error an operator can actually act on (slow the job down).
		return catalog.CatalogEntry{}, false, errors.New("tvmaze: rate limited (429) — reduce match concurrency")
	default:
		return catalog.CatalogEntry{}, false, fmt.Errorf("tvmaze status %d", resp.StatusCode)
	}

	var sh show
	if err := json.Unmarshal(body, &sh); err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("tvmaze json: %w", err)
	}
	if sh.ID == 0 {
		s.remember(key, catalog.CatalogEntry{}, false)
		return catalog.CatalogEntry{}, false, nil
	}
	e := s.toEntry(sh)
	// The wide art lives behind a second call. Best-effort by design: a banner
	// is a nicety and the poster, summary and dates are already in hand, so a
	// failure here must not lose the match. Errors are dropped rather than
	// returned for exactly that reason.
	s.addWideArt(ctx, sh.ID, &e)
	s.remember(key, e, true)
	return e, true, nil
}

// lookup reads the series cache.
func (s *Source) lookup(key string) (cached, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	c, ok := s.seen[key]
	return c, ok
}

// remember records a settled answer — a match or a definite miss.
func (s *Source) remember(key string, e catalog.CatalogEntry, ok bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.seen == nil {
		s.seen = map[string]cached{}
	}
	s.seen[key] = cached{entry: e, ok: ok}
}

// image is one entry from /shows/{id}/images.
type image struct {
	Type        string `json:"type"`
	Main        bool   `json:"main"`
	Resolutions struct {
		Original struct {
			URL string `json:"url"`
		} `json:"original"`
		Medium struct {
			URL string `json:"url"`
		} `json:"medium"`
	} `json:"resolutions"`
}

// addWideArt fills banner and background from /shows/{id}/images.
//
// The search endpoint returns ONE image — the poster — but TVmaze also holds
// banner, background and typography art, and a release page wants the wide
// shapes the poster cannot fill. They are a separate call because the show
// object does not carry them.
//
// That call costs a second slot against the rate limit, which is why this is
// the only extra request the source makes and why it is skipped entirely when
// the show has no id to ask about.
func (s *Source) addWideArt(ctx context.Context, id int64, e *catalog.CatalogEntry) {
	if id <= 0 {
		return
	}
	if err := s.wait(ctx); err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/shows/%d/images", s.baseURL, id), nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return
	}
	var imgs []image
	if err := json.Unmarshal(body, &imgs); err != nil {
		return
	}
	// "main" is TVmaze's own pick for a type, so it wins; otherwise the first
	// of that type. A show carries dozens of posters and usually one banner,
	// so this is a real choice only for the poster — which we do not take from
	// here, since the search already gave us the one the show itself uses.
	pick := map[string]string{}
	for _, im := range imgs {
		url := im.Resolutions.Original.URL
		if url == "" {
			url = im.Resolutions.Medium.URL
		}
		if url == "" {
			continue
		}
		if _, have := pick[im.Type]; !have || im.Main {
			pick[im.Type] = url
		}
	}
	for _, t := range []string{"banner", "background"} {
		if u := pick[t]; u != "" {
			e.Fields[t+"_url"] = u
		}
	}
}

func (s *Source) toEntry(sh show) catalog.CatalogEntry {
	e := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "tv"},
		Title:    sh.Name,
		Year:     yearOf(sh.Premiered),
		Genres:   sh.Genres,
		External: []catalog.ExternalID{{Namespace: "tvmaze", Value: strconv.FormatInt(sh.ID, 10)}},
		Fields:   map[string]any{},
	}
	// The original is the full-resolution upload; medium is 210x295. Prefer the
	// original and let the site scale — a poster slot on a release page is
	// bigger than 210px on any modern display.
	if sh.Image.Original != "" {
		e.CoverURL = sh.Image.Original
	} else if sh.Image.Medium != "" {
		e.CoverURL = sh.Image.Medium
	}
	// Cross-ids are what let a later TMDB/TVDB upgrade reconcile with what this
	// source stored, so they are worth carrying even though nothing reads them
	// yet.
	if sh.Externals.IMDB != "" {
		e.External = append(e.External, catalog.ExternalID{Namespace: "imdb", Value: sh.Externals.IMDB})
	}
	if sh.Externals.TheTVDB > 0 {
		e.External = append(e.External, catalog.ExternalID{Namespace: "tvdb", Value: strconv.FormatInt(sh.Externals.TheTVDB, 10)})
	}
	if sum := stripHTML(sh.Summary); sum != "" {
		e.Fields["overview"] = sum
	}
	if sh.Premiered != "" {
		e.Fields["premiered"] = sh.Premiered
	}
	if sh.Ended != "" {
		e.Fields["ended"] = sh.Ended
	}
	if sh.Status != "" {
		e.Fields["status"] = sh.Status
	}
	if net := firstNonEmpty(sh.Network.Name, sh.WebChannel.Name); net != "" {
		e.Fields["network"] = net
	}
	if sh.Rating.Average > 0 {
		e.Fields["vote_average"] = sh.Rating.Average
	}
	if sh.Runtime > 0 {
		e.Fields["runtime"] = sh.Runtime
	}
	if sh.Language != "" {
		e.Fields["language"] = sh.Language
	}
	if sh.Image.Medium != "" {
		e.Fields["thumb_url"] = sh.Image.Medium
	}
	return e
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(date[:4])
	return n
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
