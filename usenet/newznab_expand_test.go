package usenet

import (
	"context"
	"reflect"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// stubExpandCatalog serves a fixed taxonomy for the expansion tests.
type stubExpandCatalog struct{ cats []pluginapi.Category }

func (s stubExpandCatalog) All(context.Context) ([]pluginapi.Category, error)     { return s.cats, nil }
func (s stubExpandCatalog) Enabled(context.Context) ([]pluginapi.Category, error) { return s.cats, nil }
func (s stubExpandCatalog) IsEnabled(context.Context, int) (bool, error)          { return true, nil }
func (s stubExpandCatalog) Categorize(string, string) int                         { return 0 }
func (s stubExpandCatalog) Name(int) string                                       { return "" }

// Caps advertises PARENT ids and categorize files releases under SUBCATS, so
// the exact IN(cat) match answered an empty feed for an indexer full of TV
// the moment a caps-following client asked cat=5000 — which is what
// Prowlarr's connectivity test does.
func TestExpandCatsWidensParentsToSubcats(t *testing.T) {
	ctx := context.Background()
	cat := stubExpandCatalog{cats: []pluginapi.Category{
		{ID: 5000, Name: "TV", Subcats: []pluginapi.Subcategory{
			{ID: 5030}, {ID: 5040}, {ID: 5070}}},
		{ID: 2000, Name: "Movies", Subcats: []pluginapi.Subcategory{{ID: 2040}}},
		{ID: 1000, Name: "Console"},
	}}
	s := &service{catalog: cat}

	if got := s.expandCats(ctx, []int{5000}); !reflect.DeepEqual(got, []int{5000, 5030, 5040, 5070}) {
		t.Errorf("parent 5000 expanded to %v", got)
	}
	// A subcat passes through unchanged.
	if got := s.expandCats(ctx, []int{5040}); !reflect.DeepEqual(got, []int{5040}) {
		t.Errorf("subcat 5040 became %v", got)
	}
	// Mixed input: deduped union, order preserved.
	if got := s.expandCats(ctx, []int{5000, 5040, 2000}); !reflect.DeepEqual(got, []int{5000, 5030, 5040, 5070, 2000, 2040}) {
		t.Errorf("mixed list expanded to %v", got)
	}
	// Unknown ids pass through.
	if got := s.expandCats(ctx, []int{9999}); !reflect.DeepEqual(got, []int{9999}) {
		t.Errorf("unknown id became %v", got)
	}
	// The bare parent stays in the output — Console files releases at 1000.
	if got := s.expandCats(ctx, []int{1000}); !reflect.DeepEqual(got, []int{1000}) {
		t.Errorf("childless parent became %v", got)
	}
	// Idempotence: the demo host pre-expands, so double expansion is a
	// deployed shape and must return the same set.
	once := s.expandCats(ctx, []int{5000})
	if got := s.expandCats(ctx, once); !reflect.DeepEqual(got, once) {
		t.Errorf("re-expansion changed the list: %v -> %v", once, got)
	}
	// Empty stays empty — the no-filter fast path must not grow a filter.
	if got := s.expandCats(ctx, nil); len(got) != 0 {
		t.Errorf("nil input expanded to %v", got)
	}
}

// Without the catalog plugin, fallbackCats answers — a no-catalog install
// still serves cat=8000 with its 8010-filed rows.
func TestExpandCatsFallsBackWithoutCatalog(t *testing.T) {
	ctx := context.Background()
	s := &service{}
	if got := s.expandCats(ctx, []int{8000}); !reflect.DeepEqual(got, []int{8000, 8010}) {
		t.Errorf("8000 expanded to %v via fallbackCats", got)
	}
	if got := s.expandCats(ctx, []int{5000}); !reflect.DeepEqual(got, []int{5000, 5070}) {
		t.Errorf("5000 expanded to %v via fallbackCats", got)
	}
}
