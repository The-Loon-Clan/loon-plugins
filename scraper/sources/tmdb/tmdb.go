// Package tmdb is an API-search catalog.MetadataSource backed by The Movie
// Database (https://api.themoviedb.org/3) — the movie and tv domains. Like
// theporndb it has NO local id space: it identifies an entry from a free-text
// query (the scraper.Searcher capability), so TitleIndex is empty and Fetch(id)
// returns ErrNoLocalID. The framework treats it as a degenerate
// MetadataSource and matches through Search.
//
// # One instance per domain, by construction
//
// scraper.domainForCategory maps Newznab 2xxx → "movie" and 5xxx → "tv" as two
// SEPARATE domain keys; catalog.Registry keys a source by exactly one key and
// rejects duplicates; catalog.MetadataSource.Domain returns exactly one
// DomainInfo. A single TMDB Source therefore cannot serve both domains. Rather
// than bolt a multi-domain concept onto the framework for one source, the Kind
// is a construction parameter and the host registers two instances off the same
// API key:
//
//	reg.RegisterSource(tmdb.New(key, tmdb.KindMovie, ""))
//	reg.RegisterSource(tmdb.New(key, tmdb.KindTV, ""))
//
// Kind is a string type because it doubles as the domain key AND the TMDB
// search path segment (/search/movie, /search/tv). Those two vocabularies
// happen to agree exactly, and pinning them together keeps a third mapping
// table out of this file.
//
// # Release-name cleanup
//
// A Usenet subject is not a title — it is
// "Some.Movie.Name.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP". ParseReleaseName
// is the pure function that recovers a searchable title, a year hint, and (for
// tv) the season/episode from one; it is the make-or-break part of matching and
// is tested independently of the network.
package tmdb

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
	"time"

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/httpclient"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ErrNoLocalID is returned by Fetch — this source is query-only.
var ErrNoLocalID = errors.New("tmdb: no local id space (use Search)")

// Kind selects which TMDB endpoint (and therefore which catalog domain) a
// Source serves. See the package comment for why it is a construction
// parameter and not a per-call argument.
type Kind string

const (
	// KindMovie serves domain "movie" via /search/movie (Newznab 2xxx).
	KindMovie Kind = "movie"
	// KindTV serves domain "tv" via /search/tv (Newznab 5xxx except 5070,
	// which domainForCategory routes to the anime source).
	KindTV Kind = "tv"
)

const (
	defaultBaseURL = "https://api.themoviedb.org/3"
	// TMDB serves images off its own CDN; the API returns bare paths like
	// "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg" that only become URLs with a size
	// prefix. w500 is the standard poster width for card-sized covers.
	posterBase   = "https://image.tmdb.org/t/p/w500"
	backdropBase = "https://image.tmdb.org/t/p/w780"
)

// Source is the TMDB metadata source for one Kind. Construct with New; an
// empty API key (or an unknown Kind) makes New return nil, so the host
// registers it only when configured.
type Source struct {
	apiKey string
	// bearer says the credential is a v4 read access token, which authenticates
	// through an Authorization header. A v3 api_key has no header form and has
	// to travel in the query string.
	bearer  bool
	kind    Kind
	baseURL string
	http    *http.Client
}

// looksLikeV4Token reports whether a TMDB credential is a v4 read access
// token rather than a v3 api_key.
//
// A v4 token is a JWT: three base64url segments separated by dots, and in
// practice always beginning "eyJ" (the encoding of `{"`). A v3 key is 32 hex
// characters and contains no dot at all, so the two cannot be confused.
//
// Getting this wrong fails CLOSED in the useful direction: a v4 token
// mistaken for a v3 key would go in the query and simply be rejected by TMDB
// with a 401, which is visible immediately. The reverse cannot happen, since
// a v3 key has no dots.
func looksLikeV4Token(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return strings.HasPrefix(s, "eyJ")
}

var _ catalog.MetadataSource = (*Source)(nil)

// New builds the source for one domain, or nil when apiKey is empty or kind is
// not one of KindMovie/KindTV (so the host can register unconditionally and
// simply get nothing when TMDB_API_KEY is unset). baseURL defaults to the
// public API; tests point it at an httptest server.
func New(apiKey string, kind Kind, baseURL string) *Source {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	if kind != KindMovie && kind != KindTV {
		return nil
	}
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
		apiKey:  apiKey,
		bearer:  looksLikeV4Token(apiKey),
		kind:    kind,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    httpclient.NewAPI(),
	}
}

// Domain returns the one domain this instance serves. Priorities sit between
// anime (100) and xxx (50): anime must outrank tv because Newznab 5070 is a
// subcategory of 5xxx and an ambiguous title should stay an anime match.
func (s *Source) Domain() catalog.DomainInfo {
	if s.kind == KindTV {
		return catalog.DomainInfo{Key: "tv", UnitNoun: "series", Priority: 55}
	}
	return catalog.DomainInfo{Key: "movie", UnitNoun: "movie", Priority: 60}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// Fetch is unsupported: TMDB ids are carried on CatalogEntry.External, not as
// local ids the host can enumerate.
func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

// Normalize keeps the domain-neutral cleaner: movies and series carry no
// sequel-folding policy the way anime does.
func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// ---------------------------------------------------------------------------
// Release-name parsing
// ---------------------------------------------------------------------------

// Query is what ParseReleaseName recovers from a release name: the text to send
// to TMDB, plus the hints used to disambiguate the results. Season/Episode are
// only ever filled for KindTV and are informational — the search itself uses
// the series name alone.
type Query struct {
	Title   string
	Year    int
	Season  int
	Episode int
}

var (
	// bracketed/braced segments are indexer noise ("[REQ]", "[1/42]").
	reBracketed = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	reExt       = regexp.MustCompile(`(?i)\.(nzb|mkv|mp4|avi|m2ts|ts|iso|img|rar|zip|par2|srt|sub|idx)$`)
	reYear      = regexp.MustCompile(`^(?:19|20)\d{2}$`)
	reEpisode   = regexp.MustCompile(`(?i)^s(\d{1,2})e(\d{1,3})`)
	reSeason    = regexp.MustCompile(`(?i)^s(\d{1,2})(?:-s?\d{1,2})?$`)
	reNxNN      = regexp.MustCompile(`^(\d{1,2})x(\d{2,3})$`)

	// separators strips scene punctuation. '-' survives so hyphenated titles
	// ("Spider-Man", "Ant-Man") stay intact; a trailing release group is
	// handled separately by trimGroupTail.
	separators = strings.NewReplacer(".", " ", "_", " ", "(", " ", ")", " ")

	junkPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^\d{3,4}[pi]$`),  // 720p, 1080i, 2160p
		regexp.MustCompile(`^[xh]-?26[45]$`), // x264, h265
		regexp.MustCompile(`^\d{1,2}bits?$`), // 10bit
		regexp.MustCompile(`^(dd|ddp|dts|dtshd|eac3|ac3|aac|truehd|atmos|flac|opus|mp3|lpcm|pcm)[0-9.+x]*$`), // DDP5, AAC2
		regexp.MustCompile(`^(hdr|dv|dovi|hlg|sdr)\d*\+?$`),                                                  // HDR10+, DV
		regexp.MustCompile(`^v\d$`), // v2 repacks
	}
)

// junkTokens are scene tags that can never be part of a real title. Ambiguous
// words are deliberately absent — bare language names ("French", "Italian",
// "Danish") and words like "Complete" are real title words often enough
// ("The French Connection", "The Danish Girl") that cutting on them costs more
// than it saves. They sit after a resolution/source tag in practice, so the
// tail cut removes them anyway.
var junkTokens = map[string]bool{
	// resolution / disc
	"4k": true, "uhd": true, "fullhd": true, "hi10p": true,
	// source
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true,
	// NB: bare "web" and bare "dvd" are NOT here, by the rule above — they are
	// real title words ("Charlotte's Web", "The Web") and cutting on them
	// truncates the query to "Charlottes"/"The", which pick() then answers with
	// results[0] and links a confidently wrong poster. The compound scene tags
	// below cover every shape that actually appears.
	"remux": true, "webrip": true, "web-dl": true, "webdl": true,
	"hdtv": true, "pdtv": true, "dvdrip": true, "dvdr": true, "dvd5": true,
	"dvd9": true, "hdrip": true, "tvrip": true, "satrip": true,
	"vhsrip": true, "bdmv": true, "bdiso": true, "hddvd": true,
	// streaming services
	"amzn": true, "nf": true, "dsnp": true, "atvp": true, "hulu": true,
	"hmax": true, "pcok": true, "stan": true, "crav": true, "itunes": true,
	// codecs
	"hevc": true, "avc": true, "xvid": true, "divx": true, "vp9": true,
	"av1": true, "mpeg2": true, "mpeg4": true, "264": true, "265": true,
	// picture tags
	"imax": true, "hybrid": true, "open-matte": true, "remastered": true,
	"restored": true, "dts-hd": true, "dts-x": true,
	// edition / status
	"proper": true, "repack": true, "rerip": true, "internal": true,
	"limited": true, "readnfo": true, "dirfix": true, "nfofix": true,
	"subfix": true, "extended": true, "unrated": true, "uncut": true,
	"uncensored": true, "theatrical": true, "directors": true, "criterion": true,
	// language / subtitle tags that are never title words
	"multi": true, "multisub": true, "multisubs": true, "truefrench": true,
	"vostfr": true, "dublado": true, "legendado": true, "dubbed": true,
	"subbed": true, "hardsub": true, "hardsubs": true, "dual-audio": true,
	// container / posting noise
	"mkv": true, "mp4": true, "avi": true, "iso": true, "m2ts": true,
	"img": true, "rar": true, "zip": true, "nzb": true, "par2": true,
	"sample": true, "yenc": true, "untouched": true,
}

func isJunk(tok string) bool {
	t := strings.ToLower(tok)
	if junkTokens[t] {
		return true
	}
	for _, re := range junkPatterns {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

// yearToken reports whether tok is a plausible release year. The upper bound
// matters: without it "Blade.Runner.2049.2160p.UHD" would cut at "2049" and
// search for "Blade Runner".
func yearToken(tok string) (int, bool) {
	if !reYear.MatchString(tok) {
		return 0, false
	}
	y, err := strconv.Atoi(tok)
	if err != nil || y < 1888 || y > time.Now().Year()+1 {
		return 0, false
	}
	return y, true
}

// ParseReleaseName turns a Usenet release name into a TMDB query. It is pure
// and side-effect free; Search is a thin wrapper over it plus one HTTP call.
//
// The algorithm is "find the earliest point where the title stops and the
// scene tags start, then throw away the tail":
//
//  1. drop bracketed noise, a file extension, and scene punctuation;
//  2. find the first index (never index 0 — "1917", "2012" and "Uncut Gems"
//     are real titles) of a season/episode marker (tv only), a plausible year,
//     or a known scene token;
//  3. the title is everything before the earliest of those;
//  4. a year only becomes a hint if it precedes the season marker —
//     "Doctor.Who.2005.S01E01" carries the series' first-air year, whereas
//     "Some.Show.S01E01.2024" carries the episode's air date, which is not
//     what first_air_date_year means.
func ParseReleaseName(raw string, kind Kind) Query {
	s := reBracketed.ReplaceAllString(strings.TrimSpace(raw), " ")
	s = reExt.ReplaceAllString(strings.TrimSpace(s), "")
	tokens := strings.Fields(separators.Replace(s))
	if len(tokens) == 0 {
		return Query{}
	}

	q := Query{}
	seasonAt, yearAt, junkAt := -1, -1, -1
	year := 0

	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if kind == KindTV && seasonAt < 0 {
			if m := reEpisode.FindStringSubmatch(tok); m != nil {
				q.Season, _ = strconv.Atoi(m[1])
				q.Episode, _ = strconv.Atoi(m[2])
				seasonAt = i
			} else if m := reNxNN.FindStringSubmatch(tok); m != nil {
				q.Season, _ = strconv.Atoi(m[1])
				q.Episode, _ = strconv.Atoi(m[2])
				seasonAt = i
			} else if m := reSeason.FindStringSubmatch(tok); m != nil {
				q.Season, _ = strconv.Atoi(m[1])
				seasonAt = i
			} else if strings.EqualFold(tok, "season") {
				if i+1 < len(tokens) {
					if n, err := strconv.Atoi(tokens[i+1]); err == nil && n > 0 && n < 100 {
						q.Season = n
					}
				}
				seasonAt = i
			}
			if seasonAt == i {
				continue
			}
		}
		if yearAt < 0 {
			if y, ok := yearToken(tok); ok {
				year, yearAt = y, i
				continue
			}
		}
		if junkAt < 0 && isJunk(tok) {
			// Record it but keep scanning: the year often sits behind an
			// edition tag ("Some.Movie.EXTENDED.2024.1080p") and it is still a
			// usable hint even though the title is cut before it.
			junkAt = i
		}
	}

	cut := len(tokens)
	for _, at := range []int{seasonAt, yearAt, junkAt} {
		if at >= 0 && at < cut {
			cut = at
		}
	}
	if yearAt >= 0 && (seasonAt < 0 || yearAt < seasonAt) {
		q.Year = year
	}

	q.Title = cleanTitle(strings.Join(tokens[:cut], " "))
	return q
}

// cleanTitle trims what the tail cut leaves behind: a dangling codec initial
// ("H" from "H.264"), a release-group suffix, and edge punctuation.
func cleanTitle(title string) string {
	f := strings.Fields(title)
	for len(f) > 1 {
		switch strings.ToLower(f[len(f)-1]) {
		case "h", "x", "v":
			f = f[:len(f)-1]
			continue
		}
		break
	}
	return strings.Trim(trimGroupTail(strings.Join(f, " ")), " -:,;&+")
}

// trimGroupTail drops an ALL-CAPS "-GROUP" suffix ("Some Movie-SPARKS"). The
// all-caps + multi-token guard is what keeps "Spider-Man" and "WALL-E" intact;
// mixed-case groups ("-FraMeSToR") always sit behind a scene tag, so the tail
// cut has already removed them by the time this runs.
func trimGroupTail(title string) string {
	f := strings.Fields(title)
	if len(f) < 2 {
		return title
	}
	last := f[len(f)-1]
	i := strings.LastIndex(last, "-")
	if i <= 0 {
		return title
	}
	tail := last[i+1:]
	if len(tail) < 2 || tail != strings.ToUpper(tail) || !strings.ContainsFunc(tail, isASCIILetter) {
		return title
	}
	f[len(f)-1] = last[:i]
	return strings.Join(f, " ")
}

func isASCIILetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// result is the subset of a TMDB search hit we use. Movies and series differ
// only in field names (title/name, release_date/first_air_date), so one struct
// covers both endpoints.
type result struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	Overview      string  `json:"overview"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	GenreIDs      []int   `json:"genre_ids"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	VoteAverage   float64 `json:"vote_average"`
}

func (r result) displayTitle() string  { return firstNonEmpty(r.Title, r.Name) }
func (r result) originalTitle() string { return firstNonEmpty(r.OriginalTitle, r.OriginalName) }
func (r result) year() int             { return yearOf(firstNonEmpty(r.ReleaseDate, r.FirstAirDate)) }

// Search identifies a movie/series from a release name. ok=false with a nil
// error means "no match" — never an error.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	q := ParseReleaseName(query, s.kind)
	if q.Title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	// The year hint is applied to the RESULTS, not sent as a `year=` filter:
	// a wrong hint (a series-disambiguation year, a mis-parsed subject) would
	// otherwise turn a good match into zero results. Ranking locally degrades
	// to "best-effort first hit" instead.
	// The credential goes in a HEADER when it can, and in the query only when
	// TMDB leaves no choice.
	//
	// A key in a query string is a key in every place a URL goes, and a URL
	// goes further than it looks: net/http embeds it in the *url.Error it
	// returns from any transport failure, so `fmt.Errorf("%w", err)` writes the
	// operator's key into this site's error_logs table on a DNS blip. That is
	// covered below by RedactURLError either way, but not putting it there is
	// better than scrubbing it afterwards.
	//
	// TMDB's v4 read access token is a JWT and authenticates v3 endpoints
	// through `Authorization: Bearer`. A v3 api_key is 32 hex characters and
	// has no header form at all — it must stay in the query. So the shape of
	// the configured credential decides, and an operator who has not migrated
	// keeps working.
	endpoint := fmt.Sprintf("%s/search/%s?query=%s&include_adult=false&page=1",
		s.baseURL, s.kind, url.QueryEscape(q.Title))
	if !s.bearer {
		endpoint += "&api_key=" + url.QueryEscape(s.apiKey)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	if s.bearer {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loon-scraper/1.0")

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("tmdb request: %w", pluginapi.RedactURLError(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return catalog.CatalogEntry{}, false, fmt.Errorf("tmdb status %d", resp.StatusCode)
	}

	var doc struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("tmdb json: %w", err)
	}
	if len(doc.Results) == 0 {
		return catalog.CatalogEntry{}, false, nil
	}
	return s.toEntry(pick(doc.Results, q.Year)), true, nil
}

// pick prefers the result whose year matches the hint parsed off the release
// name — TMDB relevance alone puts the 2005 US "The Office" ahead of the 2001
// UK original — and falls back to the first result.
func pick(rs []result, year int) result {
	if year > 0 {
		for _, r := range rs {
			if r.year() == year {
				return r
			}
		}
	}
	return rs[0]
}

func (s *Source) toEntry(r result) catalog.CatalogEntry {
	e := catalog.CatalogEntry{
		// No local id space, so Ref carries the domain only; the catalog store
		// dedupes on (kind, external namespace, external id).
		Ref:      catalog.EntityRef{Kind: string(s.kind)},
		Title:    r.displayTitle(),
		Year:     r.year(),
		External: []catalog.ExternalID{{Namespace: "tmdb", Value: strconv.FormatInt(r.ID, 10)}},
		Fields:   map[string]any{},
	}
	if orig := r.originalTitle(); orig != "" && orig != e.Title {
		e.AltTitles = []string{orig}
	}
	if r.PosterPath != "" {
		e.CoverURL = posterBase + r.PosterPath
	}
	names := movieGenres
	if s.kind == KindTV {
		names = tvGenres
	}
	for _, id := range r.GenreIDs {
		if n := names[id]; n != "" {
			e.Genres = append(e.Genres, n)
		}
	}
	// Only real values land in Fields — an empty key is a UI slot with nothing
	// behind it.
	if r.Overview != "" {
		e.Fields["overview"] = r.Overview
	}
	if r.VoteAverage > 0 {
		e.Fields["vote_average"] = r.VoteAverage
	}
	if r.BackdropPath != "" {
		e.Fields["backdrop"] = backdropBase + r.BackdropPath
	}
	return e
}

// movieGenres / tvGenres are TMDB's /genre/{movie,tv}/list tables. Search
// results carry genre_ids only, and the tables have been stable for years, so
// they are hardcoded rather than fetched on every boot.
var movieGenres = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 14: "Fantasy",
	36: "History", 27: "Horror", 10402: "Music", 9648: "Mystery",
	10749: "Romance", 878: "Science Fiction", 10770: "TV Movie",
	53: "Thriller", 10752: "War", 37: "Western",
}

var tvGenres = map[int]string{
	10759: "Action & Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 10762: "Kids",
	9648: "Mystery", 10763: "News", 10764: "Reality",
	10765: "Sci-Fi & Fantasy", 10766: "Soap", 10767: "Talk",
	10768: "War & Politics", 37: "Western",
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func yearOf(date string) int {
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil {
			return y
		}
	}
	return 0
}
