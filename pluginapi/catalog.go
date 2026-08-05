package pluginapi

import "context"

// Catalog is the content-taxonomy capability: the standard Newznab category
// tree (Console/Movies/Audio/PC/TV/XXX/Books/Other), which top-level categories
// an admin has enabled ("list everything, pick what to index"), and a heuristic
// to categorize a release by its group + title. Published by the catalog
// plugin; consumed by indexer plugins (usenet) for Newznab caps + filtering.

// Category is a Newznab-standard content category. Top-level ids are thousands
// (1000 Console … 8000 Other); Subcats sit under them (e.g. 5070 TV/Anime).
type Category struct {
	ID      int
	Name    string
	Subcats []Subcategory
}

// Subcategory is a child category (e.g. {5070, "Anime"} under TV).
type Subcategory struct {
	ID   int
	Name string
}

// Catalog is looked up off the extension registry under CatalogName.
type Catalog interface {
	// All returns the full taxonomy (every top-level category + subcats).
	All(ctx context.Context) ([]Category, error)
	// Enabled returns only the admin-enabled top-level categories (+ subcats).
	Enabled(ctx context.Context) ([]Category, error)
	// IsEnabled reports whether a top-level category (or a subcat's parent) is on.
	IsEnabled(ctx context.Context, categoryID int) (bool, error)
	// Categorize maps a release to its best-fit Newznab category id from its
	// group + title. Pure heuristic; does not consult the enabled set.
	Categorize(group, title string) int
	// Name returns a "Parent/Sub" display name for a category id.
	Name(id int) string
}

// CatalogCovers is the release↔cover-art store: the scraper's match job records
// a cover for a release id, and the release page reads it back. Implemented by
// the catalog plugin's service (same object as Catalog); look it up by type-
// asserting the Catalog capability.
type CatalogCovers interface {
	SetReleaseCover(ctx context.Context, releaseID int64, coverURL string) error
	ReleaseCover(ctx context.Context, releaseID int64) (url string, ok bool, err error)
}

// CatalogCoverBatch is the OPTIONAL bulk read side of CatalogCovers: one call
// for many release ids, so a page that renders a poster strip + a listing +
// a sidebar (30+ covers) costs one round trip instead of 30. Release ids with
// no cover are simply absent from the returned map — a short map is the normal
// case, not an error, and the caller falls back to its placeholder per id.
//
// It is a SEPARATE interface rather than a third method on CatalogCovers so
// adding it does not break existing implementors: consumers feature-detect it
// by type assertion (`if b, ok := covers.(pluginapi.CatalogCoverBatch); ok`)
// and loop over ReleaseCover when the assertion fails. Same convention as
// pluginapi.Fillable, scraper.Searcher, and catalog.CrossIDResolver.
//
// The catalog plugin's service implements both, so a host that type-asserts
// the Catalog capability gets the batch path for free.
type CatalogCoverBatch interface {
	ReleaseCovers(ctx context.Context, releaseIDs []int64) (map[int64]string, error)
}

const CatalogName = "catalog.taxonomy"
