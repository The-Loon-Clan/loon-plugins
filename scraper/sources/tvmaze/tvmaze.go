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
}

var _ catalog.MetadataSource = (*Source)(nil)

// New builds the source. Never returns nil: there is nothing to configure.
func New(baseURL string) *Source {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Source{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
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
		IMDB   string `json:"imdb"`
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
		return catalog.CatalogEntry{}, false, fmt.Errorf("tvmaze request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
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
		return catalog.CatalogEntry{}, false, nil
	}
	return s.toEntry(sh), true, nil
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
