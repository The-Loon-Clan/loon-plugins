// Package anidb is a local-index catalog.MetadataSource for anime, backed by
// AniDB's HTTP API (http://api.anidb.net:9001/httpapi). It's the id-centric kind
// of source: a titles dump feeds TitleIndex (normalized title → aid) for
// matching, and Fetch(aid) pulls full metadata. Requires a registered AniDB
// client name for live Fetch; TitleIndex + Normalize work offline.
//
// Design mirrors the prod site's anidb_service.go, reduced to the
// catalog.MetadataSource contract. The full anime-titles.xml import is a data
// concern the host injects (titles map) — kept out of this module.
package anidb

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/httpclient"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

var errNoClient = errors.New("anidb: no client name configured (register one at anidb.net)")

// Source implements catalog.MetadataSource for anime.
type Source struct {
	client  string           // registered AniDB client name (required for Fetch)
	titles  map[string]int64 // normalized title → aid (from the titles dump)
	http    *http.Client
	baseURL string
}

// New builds the source. client is the AniDB-registered client name (Fetch
// errors without it). titles is the normalized-title→aid index (nil = empty;
// the host imports anime-titles.xml and passes it here).
// New returns nil when no client name is configured, which is the convention
// every other credentialled source here follows and the reason this one was a
// bug.
//
// AniDB requires a registered client name; without one this source can answer
// nothing. It used to build itself anyway, register at priority 100, take the
// "anime" domain, and serve every lookup from an empty title index — so anime
// releases matched nothing AND could not fall through to a source that would
// have worked. On the reference index that was 1,847 releases at 6.2% cover
// art against television's 59%, with nothing logged, because from the outside
// a source that answers "no match" looks like a source that is working.
//
// Returning nil hands the domain to the keyless anilist source instead. Note
// that main.go must not assign this to an interface before checking it — see
// isNilSource there for the typed-nil trap that once panicked boot.
func New(client string, titles map[string]int64) *Source {
	if strings.TrimSpace(client) == "" {
		return nil
	}
	if titles == nil {
		titles = map[string]int64{}
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
		client:  client,
		titles:  titles,
		http:    httpclient.NewAPI(),
		baseURL: "http://api.anidb.net:9001/httpapi",
	}
}

func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "anime", UnitNoun: "episode", Priority: 100}
}

func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return s.titles, nil
}

var (
	reSeason = regexp.MustCompile(`\bseason\s+\d+\b`)
	reTrailN = regexp.MustCompile(`\s+\d+$`)
	reRoman  = regexp.MustCompile(`\s+(ii|iii|iv|v|vi|vii|viii|ix|x)$`)
)

// Normalize folds anime sequel numbering so "Attack on Titan Season 2",
// "Attack on Titan 2", and "Attack on Titan II" all key to the base title —
// the anime matching policy on top of the domain-neutral cleaner.
func (s *Source) Normalize(raw string) string {
	n := catalog.DefaultNormalize(raw)
	n = reSeason.ReplaceAllString(n, "")
	n = reRoman.ReplaceAllString(n, "")
	n = reTrailN.ReplaceAllString(n, "")
	return strings.TrimSpace(n)
}

// anidbAnime is the subset of the httpapi anime response we use.
type anidbAnime struct {
	ID       int64  `xml:"id,attr"`
	Picture  string `xml:"picture"`
	Start    string `xml:"startdate"`
	Episodes int    `xml:"episodecount"`
	Titles   struct {
		Title []struct {
			Lang string `xml:"lang,attr"`
			Type string `xml:"type,attr"`
			Text string `xml:",chardata"`
		} `xml:"title"`
	} `xml:"titles"`
	Tags struct {
		Tag []struct {
			Name string `xml:"name"`
		} `xml:"tag"`
	} `xml:"tags"`
}

// Fetch pulls full metadata for an aid from the AniDB HTTP API.
func (s *Source) Fetch(ctx context.Context, aid int64) (catalog.CatalogEntry, error) {
	if s.client == "" {
		return catalog.CatalogEntry{}, errNoClient
	}
	endpoint := fmt.Sprintf("%s?request=anime&client=%s&clientver=1&protover=1&aid=%d",
		s.baseURL, s.client, aid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.CatalogEntry{}, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "loon-scraper/1.0")

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, fmt.Errorf("anidb request: %w", pluginapi.RedactURLError(err))
	}
	defer resp.Body.Close()

	var body io.Reader = resp.Body
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return catalog.CatalogEntry{}, fmt.Errorf("anidb gzip: %w", err)
		}
		defer gz.Close()
		body = gz
	}
	raw, _ := io.ReadAll(io.LimitReader(body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return catalog.CatalogEntry{}, fmt.Errorf("anidb status %d", resp.StatusCode)
	}
	// AniDB returns <error>...</error> on rate-limit/ban instead of <anime>.
	if strings.Contains(string(raw[:min(64, len(raw))]), "<error") {
		return catalog.CatalogEntry{}, fmt.Errorf("anidb error response (rate-limited or banned)")
	}

	var a anidbAnime
	if err := xml.Unmarshal(raw, &a); err != nil {
		return catalog.CatalogEntry{}, fmt.Errorf("anidb xml: %w", err)
	}
	return toEntry(a, aid), nil
}

func toEntry(a anidbAnime, aid int64) catalog.CatalogEntry {
	e := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "anime", ID: aid},
		External: []catalog.ExternalID{{Namespace: "anidb", Value: strconv.FormatInt(aid, 10)}},
		Fields:   map[string]any{},
	}
	// main title first, then collect the rest as alternates.
	for _, t := range a.Titles.Title {
		switch {
		case t.Type == "main" && e.Title == "":
			e.Title = t.Text
		case t.Text != "":
			e.AltTitles = append(e.AltTitles, t.Text)
		}
	}
	if e.Title == "" && len(a.Titles.Title) > 0 {
		e.Title = a.Titles.Title[0].Text
	}
	if a.Picture != "" {
		e.CoverURL = "https://cdn-eu.anidb.net/images/main/" + a.Picture
	}
	if len(a.Start) >= 4 {
		if y, err := strconv.Atoi(a.Start[:4]); err == nil {
			e.Year = y
		}
	}
	for _, tag := range a.Tags.Tag {
		if tag.Name != "" {
			e.Genres = append(e.Genres, tag.Name)
		}
	}
	if a.Episodes > 0 {
		e.Fields["episodes"] = a.Episodes
	}
	return e
}
