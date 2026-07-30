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
	// statsTotals, not stats(): the full stats() scans nzbs and articles four
	// ways for what this hook reduces to three scalars, and sourcing from the
	// same place as status.json keeps the surfaces agreeing on what "Active
	// newsgroups" means — active newsgroups, not per-(backbone, group) state
	// rows, which was the old len(st.Groups) and differed on multi-backbone
	// installs.
	st, err := h.store.statsTotals(ctx)
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
	d := pluginapi.ReleaseDetail{
		Release: pluginapi.Release{
			ID: row.ID, Title: row.Title, Size: row.Size, Group: row.Group,
			Resolution: row.Resolution, Source: row.Source, Codec: row.Codec,
			Audio: row.Audio, Language: row.Language, CategoryID: row.CategoryID,
		},
	}
	if row.Posted.Valid {
		d.Release.Posted = row.Posted.Time
	}
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
	names, err := listGroups(srv)
	if err != nil {
		return 0, err
	}
	return s.store.upsertGroups(ctx, names)
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
