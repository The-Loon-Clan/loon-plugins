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
	"sync"
	"time"
	"unicode"

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

	// mu guards next, the earliest time the next request may leave. See pace.
	mu   sync.Mutex
	next time.Time
}

// minInterval paces outbound requests to roughly 10/second.
//
// Open Library is free, unkeyed and run by a non-profit, and a release now
// costs up to three lookups. A full sweep of this index's 5,117 audiobooks is
// therefore ~10,000 requests, which without pacing leave as fast as the network
// allows. Getting the site's IP blocked would not even look like a failure:
// blocked requests come back as errors, errors mean no match, and no match
// means the covers quietly stop appearing.
const minInterval = 100 * time.Millisecond

// pace blocks until this source's next request slot, or until ctx ends. The
// slot is reserved under the lock before sleeping, so concurrent callers
// queue behind each other instead of all waking at once.
func (s *Source) pace(ctx context.Context) error {
	s.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if s.next.After(now) {
		wait = s.next.Sub(now)
	}
	s.next = now.Add(wait + minInterval)
	s.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
	// "new" and "cd" are qualified rather than bare: a lone `\bnew\b` strips the
	// front off "New Moon", and the source medium is announced in several
	// shapes — "New CD rip", "CD 2026", "from CD", "CD rip".
	//
	// This one is applied in a LOOP, because postings stack them: "NR from CD
	// William Bernhardt - ..." carries two, and stripping only the first left
	// "from CD William Bernhardt" as the author, which is a query Open Library
	// answers with nothing.
	reAudioPrefix = regexp.MustCompile(`(?i)^\s*(re:\s*)?(per\s+req(uest)?|req(uested|ed)?|nr|rc[_\s]?[\d-]*|` +
		`new\s+(cd(\s+rip)?|rip|\d{4})|from\s+cd(\s+rip)?|cd(\s+rip)?(\s+\d{4})?|\d{4}\s+cd|tia|thanks?)\b[\s:,-]*`)

	// A series marker standing as its own segment: "Rizzoli #3 - Tess Gerritsen
	// - The Sinner", "Bernie Gunther #6 - Philip Kerr - ...", "Alex Rider - #8
	// - ...". Anchored to END with the number so "Georgina Kincaid #3 Richelle
	// Mead" — where the author trails the marker in the SAME segment — is kept.
	reSeriesSegment = regexp.MustCompile(`^\S.*#\s*\d+\s*$|^#\s*\d+\s*$`)

	// Segment separator. Not a plain " - ": a poster writes "Phil Rickman- The
	// Bones of Avalon" as often as "Ken Follett - The Pillars", and requiring a
	// space on both sides missed the first. Whitespace is required on ONE side
	// so a hyphenated name ("Jean-Luc") and a part tag ("61-Hours-CD-03") stay
	// whole.
	reSegment = regexp.MustCompile(`\s+[-–—]\s*|\s*[-–—]\s+`)

	// The bookkeeping after it: "03of16", "08-01", "05 of 12", "Part05",
	// "Deel 2", "CD 06", "64K", "304.73 MB", "NMR".
	reAudioTail = regexp.MustCompile(`(?i)\s*[-–—]?\s*\b(part\s?\d+|deel\s?\d+|cd[-\s]?\d+|\d{1,3}\s?(of|-)\s?\d{1,3}|\d{2,3}\s?k(bps)?|[\d.]+\s?[kmg]b|nmr|mr|vbr|ch\s?\d+)\b[\s.]*$`)

	// A bare disc/part number left at the end once the labelled forms above are
	// gone: "Fire And Bones 5-4", "Overkill 1-1", "The Burning Wire 02-02",
	// "The Pillars Of The Earth 02".
	//
	// Deliberately NOT `\d+$`. A number is part of plenty of real titles —
	// "Fahrenheit 451", "Slaughterhouse 5", "1984" — so only two shapes are
	// taken: a hyphenated pair, which is never a title, and a ZERO-PADDED
	// number, which a title never uses but a poster numbering discs always
	// does. "Catch 22" and "Fahrenheit 451" both survive this; "Deadlock 01-10"
	// does not.
	reAudioSeq = regexp.MustCompile(`\s*[-–—]?\s*\b(\d{1,3}\s*[-–—]\s*\d{1,3}|0\d{1,2})\s*$`)

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
	// parts are the " - "-separated segments of the cleaned posting, kept
	// because which segment is the author is not decidable here.
	parts []string
	// bracket is the parenthesised text, when it survived cleanup as something
	// title-shaped rather than a format tag or a year.
	bracket string
}

// bracketTitle returns the text inside the first bracket when it looks like a
// title rather than decoration — i.e. once format words, editions and years are
// removed, at least one word of two or more letters is left. "(2019)",
// "[retail epub]" and "(NMR 32 kbps)" all reduce to nothing and are discarded;
// "( Return To Sender )" survives.
func bracketTitle(s string) string {
	m := reBracketed.FindString(s)
	if m == "" {
		return ""
	}
	inner := strings.Trim(m, "[]{}()")
	inner = reFormatWords.ReplaceAllString(inner, " ")
	inner = reEdition.ReplaceAllString(inner, " ")
	inner = reYear.ReplaceAllString(inner, " ")
	inner = strings.Trim(reSpace.ReplaceAllString(inner, " "), " -_.")

	for _, w := range strings.Fields(inner) {
		n := 0
		for _, r := range w {
			if unicode.IsLetter(r) {
				n++
			}
		}
		if n >= 2 {
			return inner
		}
	}
	return ""
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
	for i := 0; i < 3; i++ {
		stripped := reAudioPrefix.ReplaceAllString(raw, "")
		if stripped == raw {
			break
		}
		raw = stripped
	}
	// Repeatedly, because a posting commonly carries two: "... 03of16 NMR".
	for i := 0; i < 3; i++ {
		trimmed := reAudioSeq.ReplaceAllString(reAudioTail.ReplaceAllString(raw, ""), "")
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
	// Keep what was in the brackets before discarding them. On ebook postings a
	// bracket is decoration ("[retail epub]", "(2019)"), but on audiobook
	// postings it is very often the TITLE — "Lee Child ( 61 Hours )",
	// "Mike Thompson ( Wolf Point )", "Fern Michaels ( Return To Sender )".
	// Stripping it unconditionally left the author alone as the whole query,
	// and Open Library answered that with a series bundle by that author.
	bracket := bracketTitle(s)
	s = reBracketed.ReplaceAllString(s, " ")
	// A dotted name is a scene habit ("Some.Book.Title"); a dot between letters
	// is a separator, but one inside an initial ("J.T.") is not — so only dots
	// with a non-space on both sides AND at least two letters after are cut.
	s = reFormatWords.ReplaceAllString(s, " ")
	s = reEdition.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	s = strings.Trim(reSpace.ReplaceAllString(s, " "), " -_.")

	q := Query{Year: year, bracket: bracket}
	// Keep the dash-separated segments. Which one is the author is NOT knowable
	// from the string — this index holds both "Ken Follett - The Pillars Of The
	// Earth" and "Blackthorn - J.T. Geissinger" — so the halves are recorded and
	// attempts() tries the orientations rather than guessing one.
	for _, p := range reSegment.Split(s, -1) {
		p = strings.TrimSpace(p)
		// A series marker is not a person and not a book; leaving it in place
		// made it the "author" and the real author the "title".
		if p == "" || reSeriesSegment.MatchString(p) {
			continue
		}
		q.parts = append(q.parts, p)
	}
	switch len(q.parts) {
	case 0:
		q.Title = s
	case 1:
		q.Title = q.parts[0]
	default:
		// Reported fields describe the most common shape ("Author - Title") so a
		// caller reading Query gets the likely reading; attempts() is what the
		// search actually walks.
		q.Author = q.parts[0]
		q.Title = strings.Join(q.parts[1:], " - ")
	}
	return q
}

// attempts lists the (title, author) pairs to put to the API, most-likely
// first. Capped at three so one release can never cost more than three
// requests against a free, unkeyed, politely-used catalogue.
func (q Query) attempts() [][2]string {
	// "Author ( Title )" — the strongest reading there is, because the poster
	// bracketed the title themselves. Tried first and, when the rest of the
	// posting is a bare name, tried INSTEAD of searching that name alone: a
	// lone author name is exactly the query that returns a series bundle.
	if q.bracket != "" && len(q.parts) < 2 {
		// The bare-title fallback matters because the text left outside the
		// bracket is not always a clean name: "Lee Child ( 61 Hours )
		// 61-Hours-CD-03" leaves "Lee Child 61-Hours", which as an author
		// returns nothing. Safe to try unauthored now that pick() requires the
		// title itself to agree.
		return [][2]string{{q.bracket, q.Title}, {q.bracket, ""}}
	}
	if len(q.parts) < 2 {
		return [][2]string{{q.Title, ""}}
	}
	first := q.parts[0]
	last := q.parts[len(q.parts)-1]
	rest := strings.Join(q.parts[1:], " ")

	out := [][2]string{{rest, first}} // "Author - Title", the dominant shape
	if last != rest {
		// "Raymond Khoury - Last Templar 02 - The Templar Salvation": the book
		// is the LAST segment, and the middle is a series marker that no
		// catalogue title contains.
		out = append(out, [2]string{last, first})
	}
	// "Blackthorn - J.T. Geissinger": author second. Tried last because it is
	// the minority shape, and only when a swap would actually differ.
	if first != rest {
		out = append(out, [2]string{first, last})
	}
	return out
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
	// Structured title=/author= rather than free-text q=. Measured against the
	// live API: q="Ken Follett - The Pillars Of The Earth 02" returns ZERO
	// results, while title="The Pillars Of The Earth"&author="Ken Follett"
	// returns the book at rank 1. A general-search query is scored over the
	// whole document and a posting's leftovers drag it below the threshold.
	var firstErr error
	for _, a := range q.attempts() {
		title, author := a[0], a[1]
		if title == "" {
			continue
		}
		docs, err := s.searchFields(ctx, title, author)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if d, ok := pick(docs, title, author, q.Year); ok {
			return s.toEntry(d), true, nil
		}
	}
	// A failed lookup is only an error if EVERY attempt failed to reach the
	// API; otherwise the catalogue simply does not have this book, which is the
	// ordinary case for a 2025 audiobook.
	if firstErr != nil {
		return catalog.CatalogEntry{}, false, firstErr
	}
	return catalog.CatalogEntry{}, false, nil
}

// searchFields runs one structured query. fields= keeps the response small:
// search.json returns a very large document per doc otherwise, and this runs
// once per release.
func (s *Source) searchFields(ctx context.Context, title, author string) ([]doc, error) {
	if err := s.pace(ctx); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("title", title)
	if author != "" {
		v.Set("author", author)
	}
	v.Set("limit", "5")
	v.Set("fields", "key,title,author_name,first_publish_year,cover_i,subject,isbn,language,edition_count")
	endpoint := s.baseURL + "/search.json?" + v.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openlibrary request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openlibrary status %d", resp.StatusCode)
	}
	var out struct {
		Docs []doc `json:"docs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("openlibrary json: %w", err)
	}
	return out.Docs, nil
}

// pick returns the result that CORROBORATES the release name, or ok=false.
//
// It used to fall back to Open Library's top hit. That is what put "Home Free"
// on "Fern Michaels ( Return To Sender )" and an audiobook bundle on "Lee Child
// ( 61 Hours )": the author matched, so any book by that author was accepted.
// A book title is far more ambiguous than a film's — "Blood Ties" is thirty
// different books — so the TITLE has to agree too, and a release with no cover
// is better than a release wearing the wrong one.
func pick(ds []doc, title, author string, year int) (doc, bool) {
	var yearHit doc
	var haveYear bool
	for _, d := range ds {
		if !titleAgrees(d.Title, title) {
			continue
		}
		if author == "" || authorAgrees(d.AuthorName, author) {
			return d, true
		}
		// Title agrees but the author does not: keep it only as a year-backed
		// second choice, since a poster's author field is often a series name.
		if year > 0 && d.FirstPublishYear == year && !haveYear {
			yearHit, haveYear = d, true
		}
	}
	return yearHit, haveYear
}

// titleAgrees compares a catalogue title to the one recovered from a posting,
// tolerating the edition decoration Open Library carries ("The Pillars of the
// Earth. 1/2") and the subtitle a poster drops.
func titleAgrees(catalogTitle, want string) bool {
	a, b := foldName(catalogTitle), foldName(want)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// Too-short folded titles ("it", "us") match inside almost anything.
	if len(a) < 4 || len(b) < 4 {
		return false
	}
	// The two directions are NOT equally safe.
	//
	// Catalogue title inside the posting is the benign case — the poster added
	// a subtitle the catalogue omits ("God Is Not Great: How Religion Poisons
	// Everything" over "God is not great"). Accept it.
	if strings.Contains(b, a) {
		return true
	}
	// Posting inside a longer CATALOGUE title is how a bundle wins: searching
	// "Fern Michaels" returns "Fern Michaels Sisterhood Series : Books 12-13",
	// which contains it and is not the book. Only accept when the posting is
	// most of the catalogue title rather than a fragment of it.
	return strings.Contains(a, b) && len(b)*2 >= len(a)
}

// authorAgrees allows either direction of containment, so "Terry Goodkind"
// agrees with a posting's "Goodkind, Terry" once folded, and a posting naming
// two co-authors agrees with a catalogue naming one.
func authorAgrees(names []string, want string) bool {
	w := foldName(want)
	if w == "" {
		return false
	}
	for _, n := range names {
		if f := foldName(n); f != "" && (strings.Contains(w, f) || strings.Contains(f, w)) {
			return true
		}
	}
	return false
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
