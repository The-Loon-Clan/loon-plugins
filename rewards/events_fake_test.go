package rewards

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// fakeEvents is a ScheduledEvents stand-in for the engine tests.
//
// The engine used to read windows out of its own MemStore; the events plugin
// owns them now, so the tests need the capability rather than the table. Kept
// faithful to the two properties the engine actually depends on:
//
//   - windows are HALF-OPEN (starts <= t < ends), because the boundary instant
//     belonging to both windows is a second free claim every midnight;
//   - the latest-starting open window wins, matching the real store's
//     DISTINCT ON … ORDER BY starts_at DESC.
//
// A double that is looser than the thing it stands for is a test that passes on
// code production rejects, which is why both are implemented rather than assumed.
type fakeEvents struct {
	defs    map[string]pluginapi.ScheduledEvent
	windows map[string][]pluginapi.EventWindow
	now     func() time.Time
	// calls counts OpenWindows invocations, so a test can prove the engine asks
	// ONCE for a whole trigger rather than once per reward — the N+1 the plural
	// signature exists to prevent.
	//
	// Guarded, because the real capability is safe to call from any process and
	// the concurrent-claims test hammers this one from twenty-five goroutines. An
	// unguarded counter here reported a data race in the ENGINE's stack, which is
	// exactly the kind of false lead a sloppy double creates.
	mu    sync.Mutex
	calls int
}

// callCount is the guarded read.
func (f *fakeEvents) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newFakeEvents(now time.Time) *fakeEvents {
	return &fakeEvents{
		defs:    map[string]pluginapi.ScheduledEvent{},
		windows: map[string][]pluginapi.EventWindow{},
		now:     func() time.Time { return now },
	}
}

// add registers an enabled event with the given windows.
func (f *fakeEvents) add(slug string, ws ...pluginapi.EventWindow) *fakeEvents {
	f.defs[slug] = pluginapi.ScheduledEvent{Slug: slug, Enabled: true, Timezone: "UTC"}
	for _, w := range ws {
		w.Slug = slug
		f.windows[slug] = append(f.windows[slug], w)
	}
	return f
}

func (f *fakeEvents) disable(slug string) *fakeEvents {
	ev := f.defs[slug]
	ev.Enabled = false
	f.defs[slug] = ev
	return f
}

func (f *fakeEvents) Events(ctx context.Context) ([]pluginapi.ScheduledEvent, error) {
	out := make([]pluginapi.ScheduledEvent, 0, len(f.defs))
	for _, ev := range f.defs {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (f *fakeEvents) Event(ctx context.Context, slug string) (pluginapi.ScheduledEvent, bool, error) {
	ev, ok := f.defs[slug]
	return ev, ok, nil
}

func (f *fakeEvents) OpenNow(ctx context.Context) (map[string]bool, error) {
	open, err := f.OpenWindows(ctx, f.allSlugs())
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(open))
	for slug := range open {
		out[slug] = true
	}
	return out, nil
}

func (f *fakeEvents) OpenWindows(ctx context.Context, slugs []string) (map[string]pluginapi.EventWindow, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	at := f.now()
	out := map[string]pluginapi.EventWindow{}
	for _, slug := range slugs {
		if ev, ok := f.defs[slug]; !ok || !ev.Enabled {
			continue
		}
		var best pluginapi.EventWindow
		var found bool
		for _, w := range f.windows[slug] {
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

func (f *fakeEvents) NextOpen(ctx context.Context, slug string) (time.Time, error) {
	at := f.now()
	var next time.Time
	for _, w := range f.windows[slug] {
		if w.Starts.After(at) && (next.IsZero() || w.Starts.Before(next)) {
			next = w.Starts
		}
	}
	return next, nil
}

// OpenWindow satisfies the interface. rewards only ever READS windows — it
// gates a recurring payout on whether one is open — so a double that could open
// one would let a test set up a state the plugin cannot reach in production.
// Failing loudly is the honest stub: if rewards ever starts opening windows,
// this fails rather than quietly returning a window nothing wrote.
func (f *fakeEvents) OpenWindow(ctx context.Context, slug string, dur time.Duration) (pluginapi.EventWindow, bool, error) {
	return pluginapi.EventWindow{}, false, errors.New("fakeEvents: rewards does not open windows")
}

func (f *fakeEvents) allSlugs() []string {
	out := make([]string, 0, len(f.defs))
	for slug := range f.defs {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

var _ pluginapi.ScheduledEvents = (*fakeEvents)(nil)

// win is a terse window literal for the tests.
func win(starts, ends time.Time) pluginapi.EventWindow {
	return pluginapi.EventWindow{Starts: starts, Ends: ends}
}
