package catalog

import (
	"context"

	"github.com/the-loon-clan/loon/catalog"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// service implements pluginapi.Catalog (taxonomy) over the static tree + the
// disabled set, AND the metadata-store seams — pluginapi.CatalogSink (the
// scraper's write side) and pluginapi.CatalogCovers (release→cover). Published
// on the extension registry; consumers type-assert the extra interfaces off the
// Catalog capability.
type service struct{ store Store }

var (
	_ pluginapi.Catalog           = (*service)(nil)
	_ pluginapi.CatalogSink       = (*service)(nil)
	_ pluginapi.CatalogCovers     = (*service)(nil)
	_ pluginapi.CatalogCoverBatch = (*service)(nil)
)

// Upsert (CatalogSink) persists a scraped entry.
func (s *service) Upsert(ctx context.Context, e catalog.CatalogEntry) error {
	return s.store.UpsertEntry(ctx, e)
}

// SetReleaseCover / ReleaseCover (CatalogCovers) link a release to cover art.
func (s *service) SetReleaseCover(ctx context.Context, releaseID int64, coverURL string) error {
	return s.store.SetReleaseCover(ctx, releaseID, coverURL)
}

func (s *service) ReleaseCover(ctx context.Context, releaseID int64) (string, bool, error) {
	return s.store.ReleaseCover(ctx, releaseID)
}

// ReleaseCovers (CatalogCoverBatch) is the bulk read: one query for a whole
// page's worth of ids instead of one per poster. Ids without a cover are absent
// from the map — callers fall back to their placeholder per id.
func (s *service) ReleaseCovers(ctx context.Context, releaseIDs []int64) (map[int64]string, error) {
	return s.store.ReleaseCovers(ctx, releaseIDs)
}

func (s *service) All(_ context.Context) ([]pluginapi.Category, error) {
	return taxonomy, nil
}

func (s *service) Enabled(ctx context.Context) ([]pluginapi.Category, error) {
	disabled, err := s.store.DisabledSet(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.Category, 0, len(taxonomy))
	for _, c := range taxonomy {
		if !disabled[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *service) IsEnabled(ctx context.Context, categoryID int) (bool, error) {
	disabled, err := s.store.DisabledSet(ctx)
	if err != nil {
		return false, err
	}
	return !disabled[topLevelOf(categoryID)], nil
}

func (s *service) Categorize(group, title string) int {
	return categorize(group, title)
}

func (s *service) Name(id int) string {
	return categoryName(id)
}
