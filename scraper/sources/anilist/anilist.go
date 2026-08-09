// Package anilist is a GraphQL catalog.MetadataSource for the anime domain,
// with NO credential.
//
// It exists because anime was the worst-covered category on the reference index
// that HAD a source: 1,847 releases, 6.2% with cover art, against 59% for
// television. The cause was not a missing source but a mute one — anidb.New
// returned a usable Source whatever you passed it, so with no client name
// registered it took the "anime" domain at priority 100, answered every lookup
// from an empty title index, and blocked those releases from reaching any
// source that could have helped. Nothing logged, because nothing failed.
//
//	reg.RegisterSource(anilist.New(""))
//
// AniList needs no key and no account for public queries. It also carries what
// this site actually renders — a full-size cover, a banner, genres, episode
// counts — plus the MyAnimeList id, which becomes a cross-id and therefore a
// link button on the release page.
//
// It serves the same domain key as the AniDB source, and catalog.Registry
// refuses a duplicate, so a host registers one or the other.
package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/catalog"
)

// ErrNoLocalID is returned by Fetch — this source is query-only.
var ErrNoLocalID = errors.New("anilist: no local id space (use Search)")

const (
	defaultBaseURL = "https://graphql.anilist.co"
	// AniList publishes 90 requests per minute. 700ms leaves headroom for the
	// burst at the head of a sweep and still clears a batch quickly; this
	// source makes ONE call per lookup, unlike TVmaze which needs a second for
	// its artwork.
	minInterval = 700 * time.Millisecond
	userAgent   = "loon-scraper/1.0 (+https://github.com/the-loon-clan; metadata enrichment)"
)

// Source is the AniList metadata source for the "anime" domain.
type Source struct {
	baseURL string
	http    *http.Client

	mu   sync.Mutex
	last time.Time

	// seen caches one lookup per SERIES, because the job asks per release and
	// an anime index is almost entirely siblings — the same reason TVmaze
	// caches. Misses are cached too: a release whose title cannot be resolved
	// would otherwise re-ask on every sweep forever.
	cacheMu sync.RWMutex
	cached  map[string]cachedEntry
}

type cachedEntry struct {
	entry catalog.CatalogEntry
	ok    bool
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
		cached:  map[string]cachedEntry{},
	}
}

func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "anime", UnitNoun: "episode", Priority: 100}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// Fetch is unsupported: AniList ids ride on CatalogEntry.External.
func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

// Normalize keeps the domain-neutral cleaner.
func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// ---------------------------------------------------------------------------
// Release-name parsing
// ---------------------------------------------------------------------------

var (
	// A leading fansub tag: "[Erai-raws] ", "[ArAn - Kuraki-Subs] ". Anchored,
	// because a bracket later in the name is a quality tag or a checksum and
	// the title sits between them.
	reLeadTag = regexp.MustCompile(`^\s*(\[[^\]]*\]\s*)+`)
	// A posting handle some uploaders prefix: "@AnimesHunt - Title".
	reLeadHandle = regexp.MustCompile(`^\s*@\S+\s*-\s*`)
	// The episode marker in the fansub convention: " - 04", " - 0059". The
	// title ends here. Requires the dash, so a hyphenated title survives
	// ("Souten no Ken Re-Genesis" keeps its hyphen — no spaces around it).
	reEpisodeDash = regexp.MustCompile(`\s+-\s+\d{1,4}(\s|$|\[|\()`)
	// The season/episode form some anime rips borrow from television.
	reSeasonEp = regexp.MustCompile(`(?i)\s+-?\s*S\d{1,2}\s*E\d{1,3}\b`)
	// Everything from the first quality/source marker onwards is packaging.
	reNoiseFrom = regexp.MustCompile(`(?i)\b(1080p|2160p|1440p|720p|576p|480p|360p|4k|bd|bdrip|bluray|blu-ray|web-?dl|web-?rip|hdtv|dvdrip|dvd|remux|x26[45]|h\.?26[45]|hevc|avc|aac\d?|flac|opus|10-?bit|hi10p|dual-?audio|multiple subtitle|multi|batch|uncensored)\b`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// Query is the series title recovered from a release name.
type Query struct{ Title string }

// ParseReleaseName recovers a searchable series title from an anime release
// name.
//
// The dominant convention is "[Group] Title - NN [tags]", and the parts have to
// come off in that order: the leading tag first (it can itself contain " - ",
// as "[ArAn - Kuraki-Subs]" does, and would otherwise be mistaken for the
// episode dash), then the episode marker, then the packaging.
func ParseReleaseName(raw string) Query {
	s := strings.ReplaceAll(raw, "_", " ")
	s = reLeadHandle.ReplaceAllString(s, "")
	s = reLeadTag.ReplaceAllString(s, "")

	// Cut at whichever end-of-title marker comes first.
	cut := len(s)
	if loc := reSeasonEp.FindStringIndex(s); loc != nil && loc[0] < cut {
		cut = loc[0]
	}
	if loc := reEpisodeDash.FindStringIndex(s); loc != nil && loc[0] < cut {
		cut = loc[0]
	}
	s = s[:cut]

	// Whatever survives may still carry packaging, when the release used no
	// episode marker at all.
	if loc := reNoiseFrom.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	// Any remaining bracket group is a tag, never part of the name.
	if i := strings.IndexAny(s, "[("); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(reSpace.ReplaceAllString(strings.ReplaceAll(s, ".", " "), " "), " -–—.")
	return Query{Title: s}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// searchQuery asks for a PAGE of candidates rather than AniList's single best
// guess. The single-result form returns whichever entry ranks highest, and for
// "Frieren Beyond Journey's End" that is the chibi shorts spin-off rather than
// the series — the same failure Wikipedia's search has, and the reason pickBest
// exists below.
const searchQuery = `query($s:String){Page(perPage:8){media(search:$s,type:ANIME,sort:SEARCH_MATCH){` +
	`id idMal format episodes status genres averageScore startDate{year} ` +
	`title{romaji english native} synonyms description(asHtml:false) ` +
	`coverImage{extraLarge large} bannerImage}}}`

type media struct {
	ID        int64    `json:"id"`
	IDMal     int64    `json:"idMal"`
	Format    string   `json:"format"`
	Episodes  int      `json:"episodes"`
	Status    string   `json:"status"`
	Genres    []string `json:"genres"`
	Average   int      `json:"averageScore"`
	StartDate struct {
		Year int `json:"year"`
	} `json:"startDate"`
	Title struct {
		Romaji  string `json:"romaji"`
		English string `json:"english"`
		Native  string `json:"native"`
	} `json:"title"`
	Synonyms    []string `json:"synonyms"`
	Description string   `json:"description"`
	CoverImage  struct {
		ExtraLarge string `json:"extraLarge"`
		Large      string `json:"large"`
	} `json:"coverImage"`
	BannerImage string `json:"bannerImage"`
}

// Search identifies a series from a release name. ok=false with a nil error
// means "no match" — never an error.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	q := ParseReleaseName(query)
	if q.Title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	key := strings.ToLower(q.Title)
	if c, hit := s.lookup(key); hit {
		return c.entry, c.ok, nil
	}
	if err := s.wait(ctx); err != nil {
		return catalog.CatalogEntry{}, false, err
	}

	body, _ := json.Marshal(map[string]any{
		"query":     searchQuery,
		"variables": map[string]any{"s": q.Title},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("anilist request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		// Say so plainly. A keyless API throttles by IP, so this is the one
		// error an operator can act on. NOT cached: it says nothing about the
		// title, and remembering it would turn a blip into a permanent hole.
		return catalog.CatalogEntry{}, false, errors.New("anilist: rate limited (429) — reduce match concurrency")
	default:
		return catalog.CatalogEntry{}, false, fmt.Errorf("anilist status %d", resp.StatusCode)
	}

	var out struct {
		Data struct {
			Page struct {
				Media []media `json:"media"`
			} `json:"Page"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("anilist json: %w", err)
	}
	best, ok := pickBest(out.Data.Page.Media, q.Title)
	if !ok {
		s.remember(key, catalog.CatalogEntry{}, false)
		return catalog.CatalogEntry{}, false, nil
	}
	e := toEntry(best)
	s.remember(key, e, true)
	return e, true, nil
}

// pickBest chooses the entry a release actually names.
//
// NOT simply the first result. AniList ranks a spin-off above its parent often
// enough to matter — "Frieren Beyond Journey's End" returns the chibi shorts —
// and a wrong match is worse than none here: it puts a confident, plausible,
// incorrect poster on a release page where nothing downstream disagrees with
// it. No match just leaves the page as it is.
//
// Two ways to accept, in order:
//
//  1. a title that matches after normalisation, across romaji, english, native
//     and the synonyms — the release name came from one of those, and which one
//     depends on the fansub group;
//  2. failing that, the first FULL SERIES result (TV or ONA), which skips the
//     movies, specials and music videos that share a franchise name.
func pickBest(list []media, want string) (media, bool) {
	norm := foldTitle(want)
	if norm == "" {
		return media{}, false
	}
	for _, m := range list {
		for _, t := range append([]string{m.Title.Romaji, m.Title.English, m.Title.Native}, m.Synonyms...) {
			if t != "" && foldTitle(t) == norm {
				return m, true
			}
		}
	}
	// Formats in order of preference, not as one set. A franchise name returns
	// its shorts and its music videos alongside the series, and an ONA sitting
	// first in the results is not a reason to prefer it over the TV run.
	for _, want := range []string{"TV", "TV_SHORT", "ONA", "MOVIE"} {
		for _, m := range list {
			if m.Format == want {
				return m, true
			}
		}
	}
	return media{}, false
}

// foldTitle normalises for COMPARISON, deleting apostrophes rather than
// letting them become spaces.
//
// DefaultNormalize turns every non-alphanumeric into a separator, so
// "Frieren: Beyond Journey's End" becomes "... journey s end" while the release
// that writes "Journeys End" becomes "... journeys end". The two never match,
// and the difference is punctuation nobody types consistently. Removing the
// apostrophe first collapses both to the same thing.
func foldTitle(s string) string {
	return catalog.DefaultNormalize(strings.NewReplacer("'", "", "’", "", "`", "").Replace(s))
}

func toEntry(m media) catalog.CatalogEntry {
	title := m.Title.Romaji
	if title == "" {
		title = m.Title.English
	}
	if title == "" {
		title = m.Title.Native
	}
	e := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "anime"},
		Title:    title,
		Year:     m.StartDate.Year,
		Genres:   m.Genres,
		CoverURL: m.CoverImage.ExtraLarge,
		External: []catalog.ExternalID{{Namespace: "anilist", Value: strconv.FormatInt(m.ID, 10)}},
		Fields:   map[string]any{},
	}
	if e.CoverURL == "" {
		e.CoverURL = m.CoverImage.Large
	}
	// Alternate titles, so the host can match a release named in a different
	// language from the one stored.
	for _, t := range append([]string{m.Title.English, m.Title.Native}, m.Synonyms...) {
		if t != "" && t != title {
			e.AltTitles = append(e.AltTitles, t)
		}
	}
	// The MyAnimeList id is a cross-id, which is a link button on the release
	// page rather than a number nobody reads.
	if m.IDMal > 0 {
		e.External = append(e.External, catalog.ExternalID{Namespace: "mal", Value: strconv.FormatInt(m.IDMal, 10)})
	}
	if d := stripHTML(m.Description); d != "" {
		e.Fields["overview"] = d
	}
	if m.BannerImage != "" {
		e.Fields["banner_url"] = m.BannerImage
	}
	if m.Episodes > 0 {
		e.Fields["episodes"] = m.Episodes
	}
	if m.Status != "" {
		e.Fields["status"] = m.Status
	}
	if m.Average > 0 {
		e.Fields["rating"] = float64(m.Average) / 10
	}
	return e
}

// stripHTML flattens AniList's description markup. asHtml:false still leaves
// <br> and the odd <i>, and a release page renders text: storing the markup
// either shows the tags literally or hands raw third-party HTML to a template.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
			b.WriteByte(' ')
		case depth == 0:
			b.WriteRune(r)
		}
	}
	out := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'").
		Replace(b.String())
	return strings.TrimSpace(reSpace.ReplaceAllString(out, " "))
}

// wait paces this client. Politeness is the source's own job for a keyless API:
// exceeding the published rate gets the whole IP blocked, which affects every
// other user behind it.
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

func (s *Source) lookup(key string) (cachedEntry, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	c, ok := s.cached[key]
	return c, ok
}

func (s *Source) remember(key string, e catalog.CatalogEntry, ok bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cached == nil {
		s.cached = map[string]cachedEntry{}
	}
	s.cached[key] = cachedEntry{entry: e, ok: ok}
}
