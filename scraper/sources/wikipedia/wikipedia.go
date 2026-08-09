// Package wikipedia is an API-search catalog.MetadataSource backed by
// Wikipedia's REST API — the movie domain, with NO credential.
//
// It exists because movies were the largest uncovered category on the index
// (roughly 20,000 releases) and every alternative wants a key: TMDB and OMDb
// require signup, and the iTunes Search API — the obvious keyless candidate —
// returns resultCount 0 for every movie query as of Aug 2026, Apple having
// apparently withdrawn movie search.
//
//	reg.RegisterSource(wikipedia.New(""))
//
// It serves the same domain key as the TMDB movie source, and catalog.Registry
// refuses a duplicate — so a host registers one or the other. TMDB is better
// where a key exists (backdrops, structured genres, a real release date); this
// is what a host gets for free.
//
// TWO calls per match, and both are necessary. Search returns a `description`
// but its thumbnails are 60px and frequently absent even for the right page;
// the summary endpoint carries the extract and the full-size image. Search
// alone cannot answer "what does this film look like", and summary alone cannot
// answer "which page is the film".
//
// Wikipedia text is CC BY-SA, which requires attribution — the host credits it
// in the footer (credits_web.go).
package wikipedia

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
var ErrNoLocalID = errors.New("wikipedia: no local id space (use Search)")

const (
	defaultBaseURL = "https://en.wikipedia.org"
	// Cross-ids live on Wikidata, a different host from the articles.
	defaultWikidataURL = "https://www.wikidata.org"
	// minInterval paces this client. Wikimedia publishes no hard per-second cap
	// for the REST API but asks for reasonable use, and this makes TWO calls
	// per release across a catalogue of tens of thousands — the sort of volume
	// that is only reasonable if it is spread out.
	minInterval = 250 * time.Millisecond
	// Wikimedia's User-Agent policy asks for something identifying, with a way
	// to make contact. An anonymous flood is what gets a client blocked.
	userAgent = "loon-scraper/1.0 (+https://github.com/the-loon-clan; metadata enrichment)"
)

// Source is the Wikipedia metadata source for the "movie" domain.
type Source struct {
	baseURL string
	// wikidataURL is separate because the cross-ids live on a DIFFERENT host
	// from the articles. A field rather than a constant so a test can point it
	// at a fake — hardcoding it made the suite call the live API, which is
	// both slow and a test that fails when someone edits a Wikipedia page.
	wikidataURL string
	http        *http.Client

	mu   sync.Mutex
	last time.Time

	// seen caches one lookup per FILM name, because the job asks per release.
	// The saving is smaller than TVmaze's — 23,061 movie releases carry 13,862
	// distinct films, so ~1.7x rather than 13x — but a film costs three calls
	// here (search, summary, Wikidata) where a series costs two, so the two
	// come out similar in wall-clock.
	//
	// Misses are cached too: Wikipedia has an article for almost everything,
	// so "no page that is a film" is the COMMON answer, and re-asking it every
	// sweep is most of the traffic this would otherwise generate.
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
	return &Source{
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		wikidataURL: defaultWikidataURL,
		http:        &http.Client{Timeout: 20 * time.Second},
		seen:        map[string]cached{},
	}
}

// lookup reads the film cache.
func (s *Source) lookup(key string) (cached, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	c, ok := s.seen[key]
	return c, ok
}

// remember records a settled answer — a match, or a definite "no film here".
// Transport failures are never remembered: they say nothing about the title.
func (s *Source) remember(key string, e catalog.CatalogEntry, ok bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.seen == nil {
		s.seen = map[string]cached{}
	}
	s.seen[key] = cached{entry: e, ok: ok}
}

// Domain returns the movie domain, at the same priority as the TMDB movie
// source — they are alternatives for one slot.
func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "movie", UnitNoun: "movie", Priority: 60}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// Fetch is unsupported: Wikipedia page keys are strings, not int64 local ids.
func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

// Normalize keeps the domain-neutral cleaner.
func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// ---------------------------------------------------------------------------
// Release-name parsing
// ---------------------------------------------------------------------------

var (
	reBracket = regexp.MustCompile(`[\[\{][^\]\}]*[\]\}]`)
	reYear    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	// Everything from the first quality/source/codec marker onwards is
	// packaging, not title. Anchored on the FIRST match so a title containing
	// one of these words keeps whatever precedes it.
	// Includes the EDITION words, not just quality/codec. "The.Matrix.1999.
	// REMASTERED.1080p" cut at 1080p left "The Matrix 1999 REMASTERED", and
	// since the year was then no longer trailing it stayed in the title too —
	// one missing word costing both the cut and the year.
	// The resolution list covers SD and the odd sizes too. It knew only
	// 1080p/2160p/720p/480p, so "Kaali (2023) Tamil 1440p SF WEB-DL" cut at
	// WEB-DL and searched Wikipedia for "Kaali (2023) Tamil 1440p SF".
	reNoiseFrom = regexp.MustCompile(`(?i)\b(1080p|2160p|4320p|1440p|4k|8k|720p|576p|540p|480p|360p|240p|web-?dl|web-?rip|bluray|blu-ray|brrip|bdrip|hdrip|dvdrip|dvdscr|hdtv|cam|telesync|remux|x26[45]|h\.?26[45]|hevc|avc|xvid|divx|aac\d?|ac3|dd[p5]|dts(-hd)?|truehd|atmos|multi|dual|repack|proper|internal|limited|extended|uncut|unrated|imax|hdr|sdr|10bit|remastered|restored|criterion|theatrical|anniversary|directors?.cut|final.cut|special.edition)\b`)
	reSpace     = regexp.MustCompile(`\s+`)
	// A film's Wikipedia description reads "2022 film by …", "1997 television
	// film", "2019 animated film". \bfilm\b and not a substring match, or
	// "American filmmaking duo" — a real search hit for a director — reads as a
	// film.
	reFilmDesc = regexp.MustCompile(`(?i)\bfilms?\b`)
	// A franchise page ("film series", "film trilogy") is not a film, and it
	// outranks the individual films for a bare series name.
	reNotAFilm = regexp.MustCompile(`(?i)\bfilm (series|trilogy|franchise)\b|\blist of\b|\bsoundtrack\b|\bvideo game\b`)
)

// Query is the film title recovered from a release name, plus the year the
// posting carried.
type Query struct {
	Title string
	Year  int
}

// ParseReleaseName recovers a searchable film title from a release name.
//
// Scene naming puts the year immediately after the title and the packaging
// after that ("Blade.Runner.2049.2017.1080p.BluRay.x264-GRP"), which is why the
// year is read BEFORE the noise is cut: it is the boundary marker, and cutting
// first would sometimes take it with the rest.
func ParseReleaseName(raw string) Query {
	s := strings.ReplaceAll(raw, "_", " ")
	s = reBracket.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, ".", " ")

	// The LAST year before the packaging is the release year; an earlier one
	// may belong to the title ("Blade Runner 2049" is a title containing a
	// year, released in 2017).
	var year int
	cut := len(s)
	if loc := reNoiseFrom.FindStringIndex(s); loc != nil {
		cut = loc[0]
	}
	head := s[:cut]
	if ms := reYear.FindAllStringIndex(head, -1); len(ms) > 0 {
		last := ms[len(ms)-1]
		year, _ = strconv.Atoi(head[last[0]:last[1]])
		// Only the TRAILING year is dropped from the title. A leading or
		// embedded one is part of the name ("Blade Runner 2049", "2012").
		//
		// A BRACKETED year is different, and it is how 5,593 of the 24,223
		// movie releases here are named: "Kaali (2023) Tamil 1440p SF WEB-DL".
		// The bracket marks where the title ends, so everything after it goes
		// regardless of what it is — which saves listing every language and
		// streaming-platform tag that can sit between the year and the
		// packaging, and saves guessing whether "English" is a language or the
		// first word of "The English Patient".
		switch {
		case last[0] > 0 && last[1] < len(head) &&
			(head[last[0]-1] == '(' || head[last[0]-1] == '[') &&
			(head[last[1]] == ')' || head[last[1]] == ']'):
			head = head[:last[0]-1]
		case strings.Trim(head[last[1]:], " )]}") == "":
			head = head[:last[0]]
		}
		// Drop a bracket or dash left dangling by the cut.
		head = strings.TrimRight(head, " ([{-–—")
	}
	title := strings.Trim(reSpace.ReplaceAllString(head, " "), " -–—")
	return Query{Title: title, Year: year}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

type searchPage struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type summary struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
	ContentURLs struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
	} `json:"content_urls"`
	Thumbnail struct {
		Source string `json:"source"`
	} `json:"thumbnail"`
	OriginalImage struct {
		Source string `json:"source"`
	} `json:"originalimage"`
}

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

func (s *Source) getJSON(ctx context.Context, endpoint string, out any) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("wikipedia request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wikipedia status %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// Search identifies a film from a release name. ok=false with a nil error means
// "no match" — never an error.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	q := ParseReleaseName(query)
	if q.Title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	// Keyed on title AND year: two releases of the same title from different
	// years are different films, and the year is half of pickFilm's decision.
	key := fmt.Sprintf("%s|%d", strings.ToLower(q.Title), q.Year)
	if c, hit := s.lookup(key); hit {
		return c.entry, c.ok, nil
	}

	var res struct {
		Pages []searchPage `json:"pages"`
	}
	// limit 6: enough that the film is present when the first hits are a
	// soundtrack, an accolades list and the director, which is the usual shape.
	if err := s.getJSON(ctx, fmt.Sprintf("%s/w/rest.php/v1/search/page?q=%s&limit=6",
		s.baseURL, url.QueryEscape(q.Title)), &res); err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	page, ok := pickFilm(res.Pages, q.Title, q.Year)
	if !ok {
		// Wikipedia has an article for almost everything, so "no page that is a
		// film" is the common answer and not a failure. Returning a non-film
		// here is what would put a director's biography on a release page.
		s.remember(key, catalog.CatalogEntry{}, false)
		return catalog.CatalogEntry{}, false, nil
	}

	var sum summary
	if err := s.getJSON(ctx, fmt.Sprintf("%s/api/rest_v1/page/summary/%s",
		s.baseURL, url.PathEscape(page.Key)), &sum); err != nil {
		// The page was identified but its detail is unavailable. Fall back to
		// what search already gave: a title and a description beat nothing, and
		// the match itself is still correct.
		e := entryFromSearch(page)
		s.addCrossIDs(ctx, page.Key, &e)
		s.remember(key, e, true)
		return e, true, nil
	}
	e := toEntry(page, sum)
	s.addCrossIDs(ctx, page.Key, &e)
	s.remember(key, e, true)
	return e, true, nil
}

// wikidataProps are the identifier claims worth carrying, and the namespace
// each becomes. Wikidata holds ~178 claim properties for a well-documented
// film; these are the ones a release page can link to.
var wikidataProps = []struct{ prop, namespace string }{
	{"P345", "imdb"},
	{"P4947", "tmdb"},
	{"P1258", "rottentomatoes"}, // "m/matrix" — already a path
	{"P1712", "metacritic"},     // "movie/the-matrix" — likewise
	// P3302 (Letterboxd film ID) is deliberately absent. Its value is a bare
	// number and letterboxd.com/film/<number>/ 404s for every film checked —
	// the slug is what that route wants. Letterboxd is still linkable, via its
	// /tmdb/<id>/ route off the TMDB id above, so the button is derived at
	// render rather than stored as an id we cannot turn into a URL.
}

// addCrossIDs attaches other databases' ids to a matched film.
//
// Wikipedia's own API does not carry them, but every article is bound to a
// Wikidata item that does, and wbgetentities resolves an enwiki page title
// straight to that item's claims — so this is ONE extra call, not a lookup
// followed by a fetch.
//
// Best-effort, exactly like TVmaze's addWideArt: the match, poster and synopsis
// are already in hand, and a link-out button is not worth losing them over.
// Errors are dropped rather than returned for that reason.
func (s *Source) addCrossIDs(ctx context.Context, pageKey string, e *catalog.CatalogEntry) {
	if pageKey == "" {
		return
	}
	var res struct {
		Entities map[string]struct {
			Missing any `json:"missing"`
			Claims  map[string][]struct {
				Mainsnak struct {
					DataValue struct {
						Value any `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}
	if s.wikidataURL == "" {
		return
	}
	endpoint := fmt.Sprintf(
		"%s/w/api.php?action=wbgetentities&sites=enwiki&titles=%s&props=claims&format=json",
		s.wikidataURL, url.QueryEscape(pageKey))
	if err := s.getJSON(ctx, endpoint, &res); err != nil {
		return
	}
	for _, ent := range res.Entities {
		if ent.Missing != nil {
			continue
		}
		for _, want := range wikidataProps {
			claims, ok := ent.Claims[want.prop]
			if !ok || len(claims) == 0 {
				continue
			}
			// Identifier claims are plain strings. Anything else is a
			// different datatype for this property than expected, and is
			// skipped rather than coerced into a broken URL.
			v, ok := claims[0].Mainsnak.DataValue.Value.(string)
			if !ok || v == "" {
				continue
			}
			e.External = append(e.External, catalog.ExternalID{Namespace: want.namespace, Value: v})
		}
	}
}

// pickFilm chooses the page that is actually a film.
//
// This is the whole disambiguation policy, and it is possible only because
// Wikipedia ships a one-line `description` per page: "2022 film by Daniel Kwan
// and Daniel Scheinert" against "American filmmaking duo" for the directors and
// "" for the accolades list. Without it a title search would return whichever
// article is most linked, which for a well-known film is often not the film.
//
// Two ways to accept, and NEITHER is "the first film in the results":
//
//  1. the year matches the release's — decisive, since remakes share a title
//     exactly and nothing else separates them;
//  2. the title matches after normalisation — for the many releases that carry
//     no year, and for films whose article year differs from the posting's.
//
// Taking the first film regardless is what an earlier version did, and against
// live Wikipedia it turned the release "Spiders 2013" into "Paper Spiders", a
// 2020 film that merely ranked first for the word. A wrong match is worse than
// none here: it puts a confident, plausible, incorrect poster and synopsis on a
// release page, where nothing later disagrees with it. No match just leaves the
// page as it is today.
func pickFilm(pages []searchPage, title string, year int) (searchPage, bool) {
	var titleHit searchPage
	haveTitle := false
	for _, p := range pages {
		if !isFilm(p.Description) {
			continue
		}
		if year > 0 && descYear(p.Description) == year {
			return p, true
		}
		if !haveTitle && sameTitle(title, p.Title) {
			titleHit, haveTitle = p, true
		}
	}
	return titleHit, haveTitle
}

var (
	// A Wikipedia article disambiguates in parentheses — "Spiders (2013 film)",
	// "Harry Potter and the Chamber of Secrets (film)" — and the release name
	// never carries that.
	reParenQualifier = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	reNonAlnum       = regexp.MustCompile(`[^a-z0-9]+`)
)

// sameTitle compares a release's title with an article's, ignoring the
// punctuation the two conventions disagree about: a release name drops the
// colon in "Dune: Part Two" and the apostrophe in "Don't Look Up", and an
// article appends "(film)".
//
// Equality, not containment. "Spiders" is contained in "Paper Spiders", which
// is exactly the wrong match this exists to refuse.
func sameTitle(release, article string) bool {
	return normTitle(release) != "" && normTitle(release) == normTitle(reParenQualifier.ReplaceAllString(article, ""))
}

func normTitle(s string) string {
	return strings.Trim(reNonAlnum.ReplaceAllString(strings.ToLower(s), ""), "")
}

// isFilm reports whether a page description describes a film rather than
// something adjacent to one.
func isFilm(desc string) bool {
	if desc == "" {
		return false // an empty description is a list or a stub, never a film
	}
	if reNotAFilm.MatchString(desc) {
		return false
	}
	return reFilmDesc.MatchString(desc)
}

// descYear reads the year out of a description like "2022 film by …".
func descYear(desc string) int {
	if m := reYear.FindString(desc); m != "" {
		n, _ := strconv.Atoi(m)
		return n
	}
	return 0
}

func entryFromSearch(p searchPage) catalog.CatalogEntry {
	e := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "movie"},
		Title:    p.Title,
		Year:     descYear(p.Description),
		External: []catalog.ExternalID{{Namespace: "wikipedia", Value: p.Key}},
		Fields:   map[string]any{},
	}
	if p.Description != "" {
		e.Fields["tagline"] = p.Description
	}
	return e
}

func toEntry(p searchPage, sum summary) catalog.CatalogEntry {
	e := entryFromSearch(p)
	if sum.Title != "" {
		e.Title = sum.Title
	}
	// originalimage is the full-size poster; thumbnail is a resized copy of the
	// same file. Prefer the original and let the site scale, as with the other
	// sources.
	if sum.OriginalImage.Source != "" {
		e.CoverURL = sum.OriginalImage.Source
	} else if sum.Thumbnail.Source != "" {
		e.CoverURL = sum.Thumbnail.Source
	}
	if sum.Extract != "" {
		e.Fields["overview"] = sum.Extract
	}
	if sum.ContentURLs.Desktop.Page != "" {
		e.Fields["wikipedia_url"] = sum.ContentURLs.Desktop.Page
	}
	if y := descYear(sum.Description); y > 0 {
		e.Year = y
	}
	return e
}
