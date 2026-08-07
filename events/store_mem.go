package events

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// MemStore is the in-memory Store, for tests and for a host with no database.
//
// It enforces the same two invariants the schema does, because a test double
// that is more permissive than production is a test that passes on code the
// database will reject: (slug, starts_at) is unique, so re-inserting a window is
// a no-op rather than a duplicate, and an event must say when it starts.
type MemStore struct {
	mu      sync.RWMutex
	events  map[string]pluginapi.ScheduledEvent
	windows map[string][]pluginapi.EventWindow
}

func NewMemStore() *MemStore {
	return &MemStore{
		events:  map[string]pluginapi.ScheduledEvent{},
		windows: map[string][]pluginapi.EventWindow{},
	}
}

var _ Store = (*MemStore)(nil)

func (m *MemStore) ListEvents(ctx context.Context) ([]pluginapi.ScheduledEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]pluginapi.ScheduledEvent, 0, len(m.events))
	for _, ev := range m.events {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (m *MemStore) GetEvent(ctx context.Context, slug string) (pluginapi.ScheduledEvent, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ev, ok := m.events[slug]
	return ev, ok, nil
}

func (m *MemStore) UpsertEvent(ctx context.Context, ev pluginapi.ScheduledEvent) error {
	if ev.Timezone == "" {
		ev.Timezone = "UTC"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[ev.Slug] = ev
	return nil
}

func (m *MemStore) DeleteEvent(ctx context.Context, slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.events, slug)
	// The cascade, by hand. Leaving orphaned windows here would let a test see
	// an event as open after it was deleted.
	delete(m.windows, slug)
	return nil
}

func (m *MemStore) OpenWindows(ctx context.Context, slugs []string, at time.Time) (map[string]pluginapi.EventWindow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]pluginapi.EventWindow{}
	for _, slug := range slugs {
		if ev, ok := m.events[slug]; !ok || !ev.Enabled {
			continue
		}
		// Latest-starting open window, matching the PG query's DISTINCT ON.
		var best pluginapi.EventWindow
		var found bool
		for _, w := range m.windows[slug] {
			if w.Contains(at) && (!found || w.Starts.After(best.Starts)) {
				best, found = w, true
			}
		}
		if found {
			out[slug] = best
		}
	}
	return out, nil
}

func (m *MemStore) AllOpen(ctx context.Context, at time.Time) (map[string]bool, error) {
	m.mu.RLock()
	slugs := make([]string, 0, len(m.events))
	for slug := range m.events {
		slugs = append(slugs, slug)
	}
	m.mu.RUnlock()

	open, err := m.OpenWindows(ctx, slugs, at)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(open))
	for slug := range open {
		out[slug] = true
	}
	return out, nil
}

func (m *MemStore) LastWindowEnd(ctx context.Context, slug string) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var last time.Time
	for _, w := range m.windows[slug] {
		if w.Ends.After(last) {
			last = w.Ends
		}
	}
	return last, nil
}

func (m *MemStore) InsertWindows(ctx context.Context, slug string, ws []pluginapi.EventWindow) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.events[slug]; !ok {
		// The FK, by hand: PG inserts nothing when the slug matches no event.
		return 0, nil
	}
	seen := map[time.Time]bool{}
	for _, w := range m.windows[slug] {
		seen[w.Starts] = true
	}
	n := 0
	for _, w := range ws {
		if seen[w.Starts] {
			continue // the UNIQUE (event_id, starts_at) no-op
		}
		seen[w.Starts] = true
		w.Slug = slug
		m.windows[slug] = append(m.windows[slug], w)
		n++
	}
	return n, nil
}

func (m *MemStore) ListWindows(ctx context.Context, slug string, limit int) ([]pluginapi.EventWindow, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ws := make([]pluginapi.EventWindow, len(m.windows[slug]))
	copy(ws, m.windows[slug])
	sort.Slice(ws, func(i, j int) bool { return ws[i].Starts.After(ws[j].Starts) })
	if len(ws) > limit {
		ws = ws[:limit]
	}
	return ws, nil
}
