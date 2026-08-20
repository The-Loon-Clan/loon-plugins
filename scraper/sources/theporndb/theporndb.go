// Package theporndb is an API-search catalog.MetadataSource backed by
// https://api.theporndb.net — for the XXX category. It has no local id space,
// so it identifies scenes by free-text query (the scraper.Searcher capability),
// ported from the emp-pipeline ThePornDB provider. TitleIndex is empty and
// Fetch(id) is unsupported — the framework treats it as a degenerate
// MetadataSource and uses Search for matching.
package theporndb

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

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/httpclient"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ErrNoLocalID is returned by Fetch — this source is query-only.
var ErrNoLocalID = errors.New("theporndb: no local id space (use Search)")

// javCode matches a JAV product code like RKI-395 (letters-digits) → /jav.
var javCode = regexp.MustCompile(`(?i)^[a-z]{2,6}-?\d{2,6}$`)

// Source is the ThePornDB metadata source. Construct with New; a zero APIKey
// makes New return nil (register only when a key is configured).
type Source struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New builds the source, or nil when apiKey is empty (so the host registers it
// only when configured). baseURL defaults to the public API.
func New(apiKey, baseURL string) *Source {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = "https://api.theporndb.net"
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
	return &Source{apiKey: apiKey, baseURL: baseURL, http: httpclient.NewAPI()}
}

func (s *Source) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: "xxx", UnitNoun: "scene", Priority: 50}
}

// TitleIndex is empty — no local id space; matching goes through Search.
func (s *Source) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (s *Source) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, ErrNoLocalID
}

func (s *Source) Normalize(raw string) string { return catalog.DefaultNormalize(raw) }

// tpdbScene mirrors the subset of the ThePornDB scene object we use.
type tpdbScene struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Site        struct {
		Name string `json:"name"`
	} `json:"site"`
	Performers []struct {
		Name string `json:"name"`
	} `json:"performers"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Posters struct {
		Large string `json:"large"`
	} `json:"posters"`
	Background struct {
		Large string `json:"large"`
	} `json:"background"`
	Image string `json:"image"`
}

// Search identifies a scene by title. ok=false (nil err) = no match.
func (s *Source) Search(ctx context.Context, query string) (catalog.CatalogEntry, bool, error) {
	title := strings.TrimSpace(query)
	if title == "" {
		return catalog.CatalogEntry{}, false, nil
	}
	// JAV product codes live under /jav; everything else under /scenes.
	path := "scenes"
	if javCode.MatchString(title) {
		path = "jav"
	}
	endpoint := fmt.Sprintf("%s/%s?q=%s&per_page=10", s.baseURL, path, url.QueryEscape(title))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.CatalogEntry{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loon-scraper/1.0")

	resp, err := s.http.Do(req)
	if err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("theporndb request: %w", pluginapi.RedactURLError(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return catalog.CatalogEntry{}, false, fmt.Errorf("theporndb status %d", resp.StatusCode)
	}

	var doc struct {
		Data []tpdbScene `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return catalog.CatalogEntry{}, false, fmt.Errorf("theporndb json: %w", err)
	}
	if len(doc.Data) == 0 {
		return catalog.CatalogEntry{}, false, nil
	}
	return toEntry(doc.Data[0], path == "jav"), true, nil
}

func toEntry(s tpdbScene, jav bool) catalog.CatalogEntry {
	cover := s.Posters.Large
	if jav && s.Image != "" {
		cover = s.Image // JAV: the DMM "pl" full front+back wrap
	}
	if cover == "" {
		cover = firstNonEmpty(s.Image, s.Background.Large)
	}
	genres := make([]string, 0, len(s.Tags))
	for _, t := range s.Tags {
		if t.Name != "" {
			genres = append(genres, t.Name)
		}
	}
	performers := make([]string, 0, len(s.Performers))
	for _, p := range s.Performers {
		if p.Name != "" {
			performers = append(performers, p.Name)
		}
	}
	e := catalog.CatalogEntry{
		Ref:      catalog.EntityRef{Kind: "xxx"},
		Title:    s.Title,
		CoverURL: cover,
		Genres:   genres,
		External: []catalog.ExternalID{{Namespace: "tpdb", Value: s.ID}},
		Fields: map[string]any{
			"studio":      s.Site.Name,
			"date":        s.Date,
			"description": s.Description,
			"performers":  performers,
		},
	}
	if y := yearOf(s.Date); y > 0 {
		e.Year = y
	}
	return e
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
