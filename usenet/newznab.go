package usenet

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Newznab/Torznab API. The plugin owns the whole XML contract (caps + search
// feed + get); the host mounts /api + /rss and delegates here. Ported from the
// prod site's api_handler.go, trimmed to what the lean indexer exposes (no
// per-user apikey/limit accounting — that stays the host's concern).

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

var newznabFuncs = template.FuncMap{"xmlesc": xmlEscaper.Replace}

var capsTmpl = template.Must(template.New("caps").Funcs(newznabFuncs).Parse(
	`<?xml version="1.0" encoding="UTF-8"?><caps>
  <server appversion="1.0" version="0.1" title="{{xmlesc .Title}}" strapline="loon demo indexer" email="" url="{{xmlesc .BaseURL}}" image=""/>
  <limits max="100" default="50"/>
  <retention days="{{.Retention}}"/>
  <registration available="no" open="no"/>
  <searching>
    <search available="yes" supportedParams="q,cat,limit,offset"/>
    {{/* season/ep are advertised because NewznabRequest carries them and this
         plugin filters on them. A host that serves tvsearch must populate
         them from season=/ep=; one that does not will over-return, and that
         is a host bug rather than a lie told here.

         tvdbid/imdbid are NOT advertised: nothing in this schema stores an
         external id, so a client filtering by one would get everything back
         while believing it had narrowed. */}}
    <tv-search available="yes" supportedParams="q,cat,limit,offset,season,ep"/>
    <movie-search available="yes" supportedParams="q,cat,limit,offset"/>
    <audio-search available="no" supportedParams=""/>
    <book-search available="no" supportedParams=""/>
  </searching>
  <categories>
    {{- range .Categories}}
    <category id="{{.ID}}" name="{{xmlesc .Name}}">
      {{- range .Subcats}}
      <subcat id="{{.ID}}" name="{{xmlesc .Name}}"/>
      {{- end}}
    </category>
    {{- end}}
  </categories>
</caps>`))

// fallbackCats is the caps taxonomy when the catalog plugin isn't installed.
var fallbackCats = []pluginapi.Category{
	{ID: 5000, Name: "TV", Subcats: []pluginapi.Subcategory{{ID: 5070, Name: "Anime"}}},
	{ID: 8000, Name: "Other", Subcats: []pluginapi.Subcategory{{ID: 8010, Name: "Misc"}}},
}

var feedTmpl = template.Must(template.New("feed").Funcs(newznabFuncs).Parse(
	`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>{{xmlesc .Title}}</title>
    <description>{{xmlesc .Title}} NZB Search</description>
    <link>{{xmlesc .BaseURL}}</link>
    <newznab:response offset="{{.Offset}}" total="{{.Total}}"/>
    {{- range .Items}}
    <item>
      <title>{{xmlesc .Title}}</title>
      <guid isPermaLink="true">{{xmlesc .DownloadURL}}</guid>
      <link>{{xmlesc .DownloadURL}}</link>
      <pubDate>{{.PubDate}}</pubDate>
      <enclosure url="{{xmlesc .DownloadURL}}" length="{{.Size}}" type="application/x-nzb"/>
      <newznab:attr name="category" value="{{.Category}}"/>
      <newznab:attr name="size" value="{{.Size}}"/>
      <newznab:attr name="guid" value="{{.ID}}"/>
      <newznab:attr name="grabs" value="0"/>
      <newznab:attr name="files" value="1"/>
      <newznab:attr name="nzbname" value="{{xmlesc .Filename}}"/>
      {{- if .Resolution}}
      <newznab:attr name="resolution" value="{{xmlesc .Resolution}}"/>
      {{- end}}
      {{- if .Source}}
      <newznab:attr name="source" value="{{xmlesc .Source}}"/>
      {{- end}}
      {{- if .Language}}
      <newznab:attr name="language" value="{{xmlesc .Language}}"/>
      {{- end}}
      {{- if .Codec}}
      <newznab:attr name="video" value="{{xmlesc .Codec}}"/>
      {{- end}}
    </item>
    {{- end}}
  </channel>
</rss>`))

type feedItem struct {
	ID          int64
	Title       string
	Filename    string
	DownloadURL string
	PubDate     string
	Size        int64
	Category    string
	Resolution  string
	Source      string
	Language    string
	Codec       string
}

var _ pluginapi.UsenetNewznab = (*service)(nil)

// Newznab dispatches on the t= function, mirroring prod's Handle().
func (s *service) Newznab(ctx context.Context, req pluginapi.NewznabRequest) (pluginapi.NewznabResult, error) {
	switch req.Function {
	case "caps":
		return s.newznabCaps(ctx, req)
	case "get", "details":
		return s.newznabGet(ctx, req)
	case "search", "tvsearch", "movie", "rss", "":
		return s.newznabFeed(ctx, req)
	default:
		return apiError(202, "No such function"), nil
	}
}

func (s *service) newznabCaps(ctx context.Context, req pluginapi.NewznabRequest) (pluginapi.NewznabResult, error) {
	retention := 3
	if s.retentionDays > 0 {
		retention = s.retentionDays
	}
	// Advertise the admin-enabled categories from the catalog plugin; fall back
	// to a minimal set when it isn't installed.
	cats := fallbackCats
	if s.catalog != nil {
		if enabled, err := s.catalog.Enabled(ctx); err == nil && len(enabled) > 0 {
			cats = enabled
		}
	}
	var buf bytes.Buffer
	if err := capsTmpl.Execute(&buf, map[string]any{
		"Title": title(req), "BaseURL": req.BaseURL, "Retention": retention, "Categories": cats,
	}); err != nil {
		return pluginapi.NewznabResult{}, err
	}
	return xmlResult(buf.Bytes()), nil
}

func (s *service) newznabGet(ctx context.Context, req pluginapi.NewznabRequest) (pluginapi.NewznabResult, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(req.ID), 10, 64)
	if err != nil {
		return apiError(200, "Missing or invalid parameter (id)"), nil
	}
	data, filename, err := s.NZB(ctx, id)
	if err != nil || len(data) == 0 {
		return apiError(300, "No such item"), nil
	}
	if filename == "" {
		filename = "download.nzb"
	}
	return pluginapi.NewznabResult{Body: data, ContentType: "application/x-nzb", Filename: filename}, nil
}

// maxFeedOffset bounds how deep API pagination may reach. OFFSET N costs a
// scan of N rows before the page, so unbounded paging is a free heavy-query
// primitive for any API key holder. 10,000 rows = 100 pages of the max page
// size — far past any real feed walker; indexers that want the whole catalog
// mirror it, they don't paginate a million deep.
const maxFeedOffset = 10000

func (s *service) newznabFeed(ctx context.Context, req pluginapi.NewznabRequest) (pluginapi.NewznabResult, error) {
	// Clamp the client-supplied paging BEFORE it reaches SQL: caps advertises
	// <limits max="100"> and this is where that promise is enforced (hosts
	// differ in what they clamp), and a negative offset is a Postgres error.
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	// An upper cap too: OFFSET is scan-and-discard, so a client-supplied
	// offset of 10^9 forced Postgres to walk and throw away a billion rows on
	// a PUBLIC request path. A request past the cap is answered as PAST THE
	// END — an empty page, offset echoed back, total implied at the offset —
	// never clamped down to the cap: a clamped request re-serves the same full
	// page at the cap with the true total, so a walking client's termination
	// condition (offset+len >= total) never fires and it re-requests the same
	// rows forever, re-ingesting duplicates. An empty page ends every client's
	// walk, no query paid.
	var releases []pluginapi.Release
	total := offset
	if offset <= maxFeedOffset {
		var err error
		f := feedFilter{
			Query: strings.TrimSpace(req.Query),
			Cats:  s.expandCats(ctx, req.Categories),
		}
		// Only tvsearch narrows by episode. The Newznab spec puts season/ep on
		// that function alone, and honouring them on t=search would quietly
		// change what a movie or generic search returns for a client that sent
		// them by habit.
		if req.Function == "tvsearch" {
			f.Season, f.Episode = req.Season, req.Episode
		}
		releases, total, err = s.store.feedReleases(ctx, f, limit, offset)
		if err != nil {
			return pluginapi.NewznabResult{}, err
		}
	}
	items := make([]feedItem, len(releases))
	for i, r := range releases {
		dl := fmt.Sprintf("%s/api?t=get&id=%d", req.BaseURL, r.ID)
		if req.APIKey != "" {
			// QueryEscape, not raw: the apikey is a client-supplied query param
			// and metacharacters would corrupt every download link we emit.
			dl += "&apikey=" + url.QueryEscape(req.APIKey)
		}
		pub := ""
		if !r.Posted.IsZero() {
			pub = r.Posted.UTC().Format(time.RFC1123Z)
		} else {
			pub = time.Now().UTC().Format(time.RFC1123Z)
		}
		items[i] = feedItem{
			ID: r.ID, Title: r.Title, Filename: r.Title + ".nzb",
			DownloadURL: dl, PubDate: pub, Size: r.Size, Category: newznabCategory(r),
			Resolution: r.Resolution, Source: r.Source, Language: r.Language, Codec: r.Codec,
		}
	}
	var buf bytes.Buffer
	if err := feedTmpl.Execute(&buf, map[string]any{
		"Title": title(req), "BaseURL": req.BaseURL,
		"Offset": offset, "Total": total, "Items": items,
	}); err != nil {
		return pluginapi.NewznabResult{}, err
	}
	return xmlResult(buf.Bytes()), nil
}

// newznabCategory returns the release's assigned Newznab category id (from the
// catalog plugin at build time), defaulting to Other/Misc.
func newznabCategory(r pluginapi.Release) string {
	if r.CategoryID > 0 {
		return strconv.Itoa(r.CategoryID)
	}
	return "8010"
}

func title(req pluginapi.NewznabRequest) string {
	if req.Title != "" {
		return req.Title
	}
	return "loon demo indexer"
}

func xmlResult(body []byte) pluginapi.NewznabResult {
	return pluginapi.NewznabResult{Body: body, ContentType: "application/xml; charset=UTF-8"}
}

func apiError(code int, desc string) pluginapi.NewznabResult {
	return xmlResult([]byte(fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?><error code="%d" description="%s"/>`, code, desc)))
}
