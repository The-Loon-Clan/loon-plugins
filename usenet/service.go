package usenet

import (
	"context"
	"encoding/xml"
	"errors"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

var errNoServer = errors.New("usenet: no server configured")

// service implements pluginapi.UsenetIndex + pluginapi.UsenetAdmin over the
// store + NNTP helpers. One instance is published on the core extension registry
// under both names in the web/all process.
type service struct {
	store           Store
	retentionDays   int               // for the Newznab caps <retention> element
	catalog         pluginapi.Catalog // optional — enabled categories + name resolution
	triggerCrawl    func()            // set by the plugin in the worker/all process
	triggerBackfill func()            // set by the plugin in the worker/all process
}

// withCategories fills the display Category name on each release from the
// catalog (no-op when the catalog isn't installed).
func (s *service) withCategories(rs []pluginapi.Release) []pluginapi.Release {
	if s.catalog != nil {
		for i := range rs {
			rs[i].Category = s.catalog.Name(rs[i].CategoryID)
		}
	}
	return rs
}

var (
	_ pluginapi.UsenetIndex     = (*service)(nil)
	_ pluginapi.SeriesIndex     = (*service)(nil)
	_ pluginapi.UsenetAdmin     = (*service)(nil)
	_ pluginapi.StatContributor = (statHook)(statHook{})
)

// statHook implements pluginapi.StatContributor on its own type — it can't
// live on service because UsenetAdmin already claims the Stats method name
// with a different signature. The indexer's totals feed the stats plugin's
// snapshot (and through it the host's site-stats page).
type statHook struct{ store Store }

func (h statHook) StatsName() string { return "usenet" }

func (h statHook) Stats(ctx context.Context) ([]pluginapi.Stat, error) {
	// statsTotalsEXACT, not statsTotals and not stats().
	//
	// Not stats(): that scans nzbs and articles four ways for what this hook
	// reduces to three scalars, and sourcing from the same place as status.json
	// keeps the surfaces agreeing on what "Active newsgroups" means — active
	// newsgroups, not per-(backbone, group) state rows, which was the old
	// len(st.Groups) and differed on multi-backbone installs.
	//
	// Not statsTotals: its row counts are the PLANNER's estimate, which is
	// right for the 5-second poll it was written for and wrong here. This hook
	// runs once an hour from a background job, and its number is published on a
	// page beside the host's own COUNT(*). They disagreed by 288 and both read
	// as facts. A scan an hour is affordable; a scan every five seconds is not.
	st, err := h.store.statsTotalsExact(ctx)
	if err != nil {
		return nil, err
	}
	return []pluginapi.Stat{
		{Key: "usenet.nzbs", Label: "NZBs indexed", Value: int64(st.TotalNZBs)},
		{Key: "usenet.staged", Label: "Articles staged", Value: int64(st.TotalStaged)},
		{Key: "usenet.groups", Label: "Active newsgroups", Value: int64(st.Groups)},
	}, nil
}

func (s *service) Search(ctx context.Context, q string, limit int) ([]pluginapi.Release, error) {
	rs, err := s.store.searchNzbs(ctx, q, limit)
	return s.withCategories(rs), err
}

func (s *service) Feed(ctx context.Context, cats []int, limit, offset int) ([]pluginapi.Release, int, error) {
	rs, total, err := s.store.feedReleases(ctx, "", cats, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return s.withCategories(rs), total, nil
}

func (s *service) Browse(ctx context.Context, group string, limit int) ([]pluginapi.Release, error) {
	rs, err := s.store.browseNzbs(ctx, group, limit)
	return s.withCategories(rs), err
}

func (s *service) Groups(ctx context.Context) ([]pluginapi.GroupInfo, error) {
	return s.store.groups(ctx)
}

func (s *service) NZB(ctx context.Context, id int64) ([]byte, string, error) {
	raw, filename, err := s.store.nzbData(ctx, id)
	if err != nil {
		return nil, "", err
	}
	data, err := gunzipBytes(raw)
	if err != nil {
		return nil, "", err
	}
	return data, filename, nil
}

// ReleaseByID loads one release and parses its stored NZB into a file list.
func (s *service) ReleaseByID(ctx context.Context, id int64) (pluginapi.ReleaseDetail, bool, error) {
	row, err := s.store.releaseByID(ctx, id)
	if err != nil {
		return pluginapi.ReleaseDetail{}, false, err
	}
	if row == nil {
		return pluginapi.ReleaseDetail{}, false, nil
	}
	// Through releaseRow.toAPI rather than field-by-field here. The hand-written
	// version was a second place that had to learn every new column, and it did
	// not: the series fields reached every listing and stopped at the detail
	// page, which is the one page that most wants to link to its show.
	d := pluginapi.ReleaseDetail{Release: row.toAPI()}
	if s.catalog != nil {
		d.Release.Category = s.catalog.Name(row.CategoryID)
	}
	// Parse the gzipped NZB for the poster + per-file sizes.
	if xmlBytes, err := gunzipBytes(row.Data); err == nil {
		var doc nzbDoc
		if xml.Unmarshal(xmlBytes, &doc) == nil {
			for i, f := range doc.Files {
				if i == 0 {
					d.Poster = f.Poster
				}
				var bytes int64
				for _, seg := range f.Segments.Segment {
					bytes += seg.Bytes
				}
				d.Files = append(d.Files, pluginapi.ReleaseFile{
					Filename: fileNameFromSubject(f.Subject),
					Bytes:    bytes,
					Segments: len(f.Segments.Segment),
				})
			}
		}
	}
	return d, true, nil
}

func (s *service) Server(ctx context.Context) (pluginapi.Server, error) {
	srv, _, err := s.store.getServer(ctx)
	// The password is write-only through this capability: a host wizard that
	// round-trips Server() into a form would otherwise ship the secret to the
	// browser (the plugin's own settings page never does). SetServer with a
	// blank password keeps the stored one, so the round-trip still works.
	srv.Password = ""
	return srv, err
}

func (s *service) SetServer(ctx context.Context, srv pluginapi.Server) error {
	return s.store.saveServer(ctx, srv)
}

func (s *service) TestConnect(_ context.Context, srv pluginapi.Server) error {
	return testConnect(srv)
}

func (s *service) FetchGroups(ctx context.Context) (int, error) {
	srv, ok, err := s.store.getServer(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errNoServer
	}
	// listGroups reports skipped names as a NON-fatal error alongside the
	// usable ones: dropping a malformed entry must not look like a failed
	// fetch, but it must not be silent either — the operator asked for every
	// group the server has.
	names, skipErr := listGroups(srv)
	if _, partial := skipErr.(errSkippedGroups); skipErr != nil && !partial {
		return 0, skipErr
	}
	added, err := s.store.upsertGroups(ctx, names)
	if err != nil {
		return added, err
	}
	return added, skipErr
}

func (s *service) AllGroups(ctx context.Context, query string, limit int) ([]pluginapi.GroupInfo, error) {
	return s.store.allGroups(ctx, query, limit)
}

func (s *service) GroupCount(ctx context.Context) (int, error) {
	return s.store.groupCount(ctx)
}

func (s *service) Stats(ctx context.Context) (pluginapi.IndexStats, error) {
	return s.store.stats(ctx)
}

func (s *service) SetGroupActive(ctx context.Context, name string, active bool) error {
	return s.store.setGroupActive(ctx, name, active)
}

func (s *service) TriggerCrawl() {
	if s.triggerCrawl != nil {
		s.triggerCrawl()
	}
}

func (s *service) TriggerBackfill() {
	if s.triggerBackfill != nil {
		s.triggerBackfill()
	}
}

func (s *service) ResetBackfill(ctx context.Context, name string) error {
	return s.store.resetBackfillForGroup(ctx, name)
}

// ── the series index (pluginapi.SeriesIndex) ────────────────────────────
//
// A separate contract from UsenetIndex rather than three more methods on it:
// widening an interface breaks every implementer for a capability most hosts
// will not use, and a host without this simply has no series pages.

func (s *service) Series(ctx context.Context, query string, limit, offset int) ([]pluginapi.SeriesRow, int, error) {
	return s.store.seriesList(ctx, query, limit, offset)
}

func (s *service) SeriesByKey(ctx context.Context, key string) (string, bool, error) {
	return s.store.seriesName(ctx, key)
}

func (s *service) Seasons(ctx context.Context, key string) ([]pluginapi.SeriesSeason, error) {
	return s.store.seriesSeasons(ctx, key)
}

func (s *service) SeasonPresence(ctx context.Context, key string, season int) (map[int]bool, bool, error) {
	return s.store.seasonPresence(ctx, key, season)
}

func (s *service) Releases(ctx context.Context, key string, season, episode, limit int) ([]pluginapi.Release, error) {
	rs, err := s.store.seriesReleases(ctx, key, season, episode, limit)
	if err != nil {
		return nil, err
	}
	// Category names resolved the same way every other listing does, so a
	// series page's rows read identically to a browse page's.
	return s.withCategories(rs), nil
}
