package catalog

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon/catalog"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// mockStore is an in-memory Store for testing without a database.
type mockStore struct {
	entries  []catalog.CatalogEntry
	covers   map[int64]string
	disabled map[int]bool
}

func newMock() *mockStore {
	return &mockStore{covers: map[int64]string{}, disabled: map[int]bool{}}
}

var _ Store = (*mockStore)(nil)

func (m *mockStore) UpsertEntry(_ context.Context, e catalog.CatalogEntry) error {
	m.entries = append(m.entries, e)
	return nil
}
func (m *mockStore) SetReleaseCover(_ context.Context, id int64, url string) error {
	m.covers[id] = url
	return nil
}
func (m *mockStore) ReleaseCover(_ context.Context, id int64) (string, bool, error) {
	u, ok := m.covers[id]
	return u, ok, nil
}
func (m *mockStore) ReleaseCovers(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		if u, ok := m.covers[id]; ok && u != "" {
			out[id] = u
		}
	}
	return out, nil
}
func (m *mockStore) DisabledSet(_ context.Context) (map[int]bool, error) {
	out := map[int]bool{}
	for k, v := range m.disabled {
		if v {
			out[k] = true
		}
	}
	return out, nil
}
func (m *mockStore) SetEnabled(_ context.Context, id int, enabled bool) error {
	if enabled {
		delete(m.disabled, id)
	} else {
		m.disabled[id] = true
	}
	return nil
}

func TestCategoryToggle(t *testing.T) {
	ctx := context.Background()
	m := newMock()

	// disable category 6000, enable it back
	_ = m.SetEnabled(ctx, 6000, false)
	if d, _ := m.DisabledSet(ctx); !d[6000] {
		t.Fatal("6000 should be disabled")
	}
	_ = m.SetEnabled(ctx, 6000, true)
	if d, _ := m.DisabledSet(ctx); d[6000] {
		t.Fatal("6000 should be enabled again")
	}
}

func TestReleaseCoverRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := newMock()
	if _, ok, _ := m.ReleaseCover(ctx, 42); ok {
		t.Fatal("no cover expected yet")
	}
	_ = m.SetReleaseCover(ctx, 42, "http://x/c.jpg")
	if u, ok, _ := m.ReleaseCover(ctx, 42); !ok || u != "http://x/c.jpg" {
		t.Fatalf("cover = %q ok=%v", u, ok)
	}
}

// TestReleaseCoversBatch drives the batch path through the service — the object
// the host actually type-asserts pluginapi.CatalogCoverBatch on.
func TestReleaseCoversBatch(t *testing.T) {
	ctx := context.Background()
	m := newMock()
	svc := &service{store: m}

	// The host feature-detects the batch capability off the Catalog capability;
	// if this assertion ever stops holding, every consumer silently falls back
	// to the N+1 loop, so assert it here.
	var cat pluginapi.Catalog = svc
	batch, ok := cat.(pluginapi.CatalogCoverBatch)
	if !ok {
		t.Fatal("service should satisfy pluginapi.CatalogCoverBatch")
	}

	// Empty input: an empty map, no error, no store work.
	got, err := batch.ReleaseCovers(ctx, nil)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty input should yield an empty map, got %v", got)
	}

	_ = svc.SetReleaseCover(ctx, 1, "http://x/1.jpg")
	_ = svc.SetReleaseCover(ctx, 3, "http://x/3.jpg")

	// Partial hit + duplicate id: 2 has no cover and is simply absent; 1 asked
	// for twice comes back once.
	got, err = batch.ReleaseCovers(ctx, []int64{1, 2, 3, 1})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 covers, got %d (%v)", len(got), got)
	}
	if got[1] != "http://x/1.jpg" || got[3] != "http://x/3.jpg" {
		t.Fatalf("covers = %v", got)
	}
	if u, present := got[2]; present {
		t.Fatalf("id 2 has no cover, should be absent; got %q", u)
	}

	// All-miss: still an empty map, not an error.
	got, err = batch.ReleaseCovers(ctx, []int64{99, 100})
	if err != nil {
		t.Fatalf("all-miss: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("all-miss should yield an empty map, got %v", got)
	}
}
