// Package openlibrary is an API-search catalog.MetadataSource backed by Open
// Library (https://openlibrary.org) — the book domain.
//
// It is the one source that needs NO credential. Open Library is an Internet
// Archive project with an open catalogue, no API key and no signup, which makes
// it the source a fresh install can actually use: TMDB, ThePornDB and AniDB all
// register as "not configured" until an operator goes and gets a key, so a demo
// site's covers stay empty until someone does. New() therefore takes no key and
// never returns nil.
//
//	reg.RegisterSource(openlibrary.New(""))
//
// Two documented constraints shape the implementation, both from
// https://openlibrary.org/dev/docs/api/covers:
//
//   - Cover URLs are built from a cover id, NOT crawled. "Please, do not crawl
//     our cover API" — so a cover is only ever emitted as a URL for the site to
//     render, never fetched here.
//   - Cover lookups by ISBN are rate-limited to 100 requests per IP per 5
//     minutes, while lookups by the internal cover id are not. search.json
//     returns `cover_i` (the cover id) directly, so the b/id/ form is used and
//     the ISBN path is avoided entirely.
package openlibrary

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
)

// ErrNoLocalID is returned by Fetch — this source is query-only, like TMDB.
var ErrNoLocalID = errors.New("openlibrary: no local id space (use Search)")

const (
	defaultBaseURL = "https://openlibrary.org"
	// coverBase builds a cover URL from the cover id search.json returns.
	// -L is the large size; the covers host is the one the docs ask public
	// pages to point at.
	coverBase = "https://covers.openlibrary.org/b/id/"
	// userAgent identifies this client. Open Library does not require a key,
	// but an anonymous flood is how a free service ends up blocking a whole
	// class of client, so the request says who it is.
	userAgent = "loon-scraper/1.0 (+https://github.com/the-loon-clan)"
)

// Source is the Open Library metadata source for the "book" domain.
type Source struct {
	baseURL string
	http    *http.Client
}

var _ catalog.MetadataSource = (*Source)(nil)

// New builds the source. Unlike the keyed sources it never returns nil —
// there is nothing to configure, so a host that registers it always gets a
// working book domain. baseURL defaults to the public API; tests point it at
// an httptest server.
func New(baseURL string) *Source {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Source{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// Domain returns the book domain. Priority sits below movie/tv because those
// are the categories a release is most often mis-filed INTO — a book domain
// outranking them would let an audiobook title claim a film. Nothing routes to
// two domains today, so the number is a tie-break policy rather than a live
// contest.
func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "book", UnitNoun: "book", Priority: 40}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// Fetch is unsupported: Open Library work ids are strings ("OL45804W") carried
// on CatalogEntry.External, not int64 local ids the host can enumerate.
func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

// Normalize keeps the domain-neutral cleaner.
func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// ---------------------------------------------------------------------------
// Release-name parsing
// ---------------------------------------------------------------------------

var (
	// Indexer noise: bracketed/braced tags, and the scene-style decoration that
	// surrounds a book posting.
	reBracketed = regexp.MustCompile(`[\[\{\(][^\]\}\)]*[\]\}\)]`)
	// Format and quality words that follow the title in a posting. Anchored to
	// word boundaries so "Retail" the word is stripped and "Retailer" is not.
	reFormatWords = regexp.MustCompile(`(?i)\b(retail|epub|mobi|azw3?|pdf|djvu|cbz|cbr|ebook|e-book|audiobook|unabridged|abridged|read by|m4b|mp3|64k|128k|scan|hq|repack|v\d+)\b`)
	// A trailing edition/volume marker; the search is better without it.
	reEdition = regexp.MustCompile(`(?i)\b(\d+(st|nd|rd|th)\s+edition|vol(ume)?\.?\s*\d+|book\s+\d+)\b`)
	reYear    = regexp.MustCompile(`\b(1[5-9]\d{2}|20\d{2})\b`)
	reSpace   = regexp.MustCompile(`\s+`)

	// ── Audiobook postings ──────────────────────────────────────────────────
	//
	// Books arrive from two very different worlds. An ebook posting is close to
	// "Author - Title"; an audiobook posting is a request thread's subject line,
	// and carries whatever the poster and the requester said to each other.
	//
	// Measured over the 5,117 audiobook releases on the reference index: 79%
	// carry an "Author - Title" core, 44% of those also carry a part marker,
	// and 7% are not a book at all — a genre list, or a bare word like
	// "hardboiled mystery".

	// The conversation in front of the title: "per req - ", "NR ", "RC_16-36 - ",
	// "Re: REQ:", "New 2025 ", "2026 CD ". 30% of postings have one.
	reAudioPrefix = regexp.MustCompile(`(?i)^\s*(re:\s*)?(per\s+req(uest)?|req(uested|ed)?|nr|rc[_\s]?[\d-]*|new\s+\d{4}|\d{4}\s+cd|tia|thanks?)\b[\s:,-]*`)

	// The bookkeeping after it: "03of16", "08-01", "05 of 12", "Part05",
	// "Deel 2", "CD 06", "64K", "304.73 MB", "NMR".
	reAudioTail = regexp.MustCompile(`(?i)\s*[-–—]?\s*\b(part\s?\d+|deel\s?\d+|cd[-\s]?\d+|\d{1,3}\s?(of|-)\s?\d{1,3}|\d{2,3}\s?k(bps)?|[\d.]+\s?[kmg]b|nmr|mr|vbr|ch\s?\d+)\b[\s.]*$`)

	// A posting that names a genre rather than a book. These are the dangerous
	// ones: "hardboiled mystery" is a perfectly good Open Library query and
	// will return a real book with a real cover, which would then be attached
	// to a release it has nothing to do with.
	// Matches only when the WHOLE posting is genre words, so a real book whose
	// title contains one ("A Caribbean Mystery", "The Romance of the Forest")
	// is untouched — the anchors are what make this safe to be generous with.
	// Plurals are explicit: "historical mysteries" is a genre list and
	// "mystery" alone would not have caught it.
	// The -y words are spelled out rather than suffixed: "mysteries" is not
	// "mystery" plus an s, and that one word is most of this corpus's genre
	// lines.
	reGenreOnly = regexp.MustCompile(`(?i)^\s*(genres?\s*:\s*|` +
		`(myster(y|ies)|biograph(y|ies)|thriller|romance|fantasy|horror|sci-?fi|` +
		`science\s+fiction|litrpg|non-?fiction|fiction|historical|hardboiled|` +
		`young\s+adult|children'?s?|adventure|crime|suspense|memoir|western|` +
		`paranormal|dystopian|urban|epic|literary|general|classic|humou?r|` +
		`poetry|erotica|self-?help)` +
		`(s|es)?\b[\s,&/-]*)+$`)
)

// Query is what ParseReleaseName recovers: the text to search for, plus an
// author hint when the posting used the common "Author - Title" form.
type Query struct {
	Title  string
	Author string
	Year   int
}

// ParseReleaseName recovers a searchable book title from a release name.
//
// Book postings are not scene-named. The dominant shape is "Author Name -
// Title" (which is how the real deletion this work started from was named:
// "Blackthorn - J.T. Geissinger" — note the AUTHOR IS SECOND there, which is
// exactly why the halves are not assumed and both are searched).
func ParseReleaseName(raw string) Query {
	// An audiobook posting is a request thread's subject line. Strip the
	// conversation off both ends before the shared cleanup runs, or the title
	// searched is "per req - Brad Meltzer - The Inner Circle 04-12 NMR".
	raw = reAudioPrefix.ReplaceAllString(raw, "")
	// Repeatedly, because a posting commonly carries two: "... 03of16 NMR".
	for i := 0; i < 3; i++ {
		trimmed := reAudioTail.ReplaceAllString(raw, "")
		if trimmed == raw {
			break
		}
		raw = trimmed
	}
	// A genre word is a searchable phrase that is not a book, and Open Library
	// will happily answer it. Refusing here is the difference between no cover
	// and a confident wrong one — 361 of the 5,117 audiobook postings on this
	// index are exactly this.
	if reGenreOnly.MatchString(raw) {
		return Query{}
	}

	// Underscores become spaces FIRST. reYear is \b-anchored and an underscore
	// is a word character, so "The_Hobbit_1937" has no boundary before the
	// year and the hint was silently lost on every underscore-separated
	// posting — which is a common shape for books.
	s := strings.ReplaceAll(raw, "_", " ")
	var year int
	if m := reYear.FindString(s); m != "" {
		year, _ = strconv.Atoi(m)
	}
	s = reBracketed.ReplaceAllString(s, " ")
	// A dotted name is a scene habit ("Some.Book.Title"); a dot between letters
	// is a separator, but one inside an initial ("J.T.") is not — so only dots
	// with a non-space on both sides AND at least two letters after are cut.
	s = reFormatWords.ReplaceAllString(s, " ")
	s = reEdition.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	s = strings.Trim(reSpace.ReplaceAllString(s, " "), " -_.")

	q := Query{Year: year}
	// "A - B": search the whole thing. Open Library's search ranks a combined
	// author+title query well, and guessing which half is the author is a coin
	// flip on real postings.
	if i := strings.Index(s, " - "); i > 0 {
		q.Author = strings.TrimSpace(s[:i])
		q.Title = strings.TrimSpace(s)
	} else {
		q.Title = s
	}
	return q
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

type doc struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	CoverID          int64    `json:"cover_i"`
	Subject          []string `json:"subject"`
	ISBN             []string `json:"isbn"`
	Language         []string `json:"language"`
	EditionCount     int      `json:"edition_count"`
}

// Search identifies a book from a release name. ok=false with a nil error means
// "no match" — never an error, matching the TMDB source's contract.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	q := ParseReleaseName(query)
	if q.Title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	// fields= keeps the response small: search.json returns a very large
	// document per doc otherwise, and this runs per release.
	endpoint := fmt.Sprintf(
		"%s/search.json?q=%s&limit=5&fields=key,title,author_name,first_publish_year,cover_i,subject,isbn,language,edition_count",
		s.baseURL, url.QueryEscape(q.Title))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("openlibrary request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return catalog.CatalogEntry{}, false, fmt.Errorf("openlibrary status %d", resp.StatusCode)
	}

	var out struct {
		Docs []doc `json:"docs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("openlibrary json: %w", err)
	}
	if len(out.Docs) == 0 {
		return catalog.CatalogEntry{}, false, nil
	}
	return s.toEntry(pick(out.Docs, q)), true, nil
}

// pick prefers a result that CORROBORATES the release name — an author whose
// name appears in it, then a matching year — and otherwise takes Open Library's
// own ranking. A book title alone is far more ambiguous than a film's ("Blood
// Ties" is thirty different books), so the author is the strongest hint the
// posting carries.
func pick(ds []doc, q Query) doc {
	hay := foldName(q.Title)
	for _, d := range ds {
		for _, a := range d.AuthorName {
			if a == "" {
				continue
			}
			if n := foldName(a); n != "" && strings.Contains(hay, n) {
				return d
			}
		}
	}
	if q.Year > 0 {
		for _, d := range ds {
			if d.FirstPublishYear == q.Year {
				return d
			}
		}
	}
	return ds[0]
}

// foldName reduces a personal name to letters and digits only, lowercased, so
// the same author matches across the punctuation habits of a cataloguer and a
// poster. Checked against the live API: Open Library holds "J. T. Geissinger"
// while the release that started this work is named "Blackthorn - J.T.
// Geissinger" — spaced initials versus tight ones, which a literal compare
// misses and which is the single most common form of author name there is.
func foldName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Source) toEntry(d doc) catalog.CatalogEntry {
	e := catalog.CatalogEntry{
		Ref:    catalog.EntityRef{Kind: "book"},
		Title:  d.Title,
		Year:   d.FirstPublishYear,
		Fields: map[string]any{},
	}
	// The work key ("/works/OL45804W") is the stable external id. Stored bare,
	// so the namespace+value pair is what identifies it.
	if key := strings.TrimPrefix(d.Key, "/works/"); key != "" && key != d.Key {
		e.External = []catalog.ExternalID{{Namespace: "openlibrary", Value: key}}
	}
	if d.CoverID > 0 {
		e.CoverURL = coverBase + strconv.FormatInt(d.CoverID, 10) + "-L.jpg"
	}
	// Subjects are Open Library's loose tagging and run to hundreds per work;
	// the first few are the useful ones and the rest are cataloguing minutiae.
	for i, sub := range d.Subject {
		if i >= 8 {
			break
		}
		e.Genres = append(e.Genres, sub)
	}
	if len(d.AuthorName) > 0 {
		e.Fields["authors"] = d.AuthorName
		// The author reads as a subtitle on a release page, and it is the one
		// field a book row is useless without.
		e.Fields["author"] = d.AuthorName[0]
	}
	if len(d.ISBN) > 0 {
		e.Fields["isbn"] = d.ISBN[0]
	}
	if d.EditionCount > 0 {
		e.Fields["edition_count"] = d.EditionCount
	}
	if len(d.Language) > 0 {
		e.Fields["language"] = d.Language[0]
	}
	return e
}
