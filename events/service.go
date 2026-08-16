package events

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Service is the published capability.
//
// Thin over the store on purpose. The one thing it adds is `now`, injectable so
// window-boundary tests are not flaky — the same trick the rewards engine uses,
// and for the same reason: a test asserting "open at 00:00" against the real
// clock passes or fails depending on when it runs.
type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

var _ pluginapi.ScheduledEvents = (*Service)(nil)

func (s *Service) Events(ctx context.Context) ([]pluginapi.ScheduledEvent, error) {
	return s.store.ListEvents(ctx)
}

func (s *Service) Event(ctx context.Context, slug string) (pluginapi.ScheduledEvent, bool, error) {
	return s.store.GetEvent(ctx, slug)
}

func (s *Service) OpenNow(ctx context.Context) (map[string]bool, error) {
	return s.store.AllOpen(ctx, s.now())
}

func (s *Service) OpenWindows(ctx context.Context, slugs []string) (map[string]pluginapi.EventWindow, error) {
	return s.store.OpenWindows(ctx, slugs, s.now())
}

// NextOpen answers from the DEFINITION, not from the window table.
//
// The table only reaches as far as the generation horizon, so reading "next"
// from it would report "never" for an event whose next firing is a year out —
// which is precisely the event an operator most wants a date for. Re-evaluating
// the cron is cheap and always right.
func (s *Service) NextOpen(ctx context.Context, slug string) (time.Time, error) {
	ev, ok, err := s.store.GetEvent(ctx, slug)
	if err != nil || !ok {
		return time.Time{}, err
	}
	return NextStart(ev, s.now())
}

// OpenWindow opens a window on an existing event now. See
// pluginapi.ScheduledEvents.OpenWindow for the contract.
//
// The race is settled by the STORE, not here. Truncating the start to the
// second is what makes that possible: event_windows carries
// UNIQUE (event_id, starts_at), so two callers arriving in the same second
// produce the same key and one of the two inserts nothing. Without the
// truncation their microseconds would differ, both would insert, and the event
// would have two overlapping windows — which OpenWindows would then answer
// arbitrarily from.
//
// A read-then-write would have looked simpler and been wrong in the case that
// matters: a settled payment webhook retried twice inside a second.
func (s *Service) OpenWindow(ctx context.Context, slug string, dur time.Duration) (pluginapi.EventWindow, bool, error) {
	if dur <= 0 {
		return pluginapi.EventWindow{}, false, fmt.Errorf("events: open %q: duration must be positive, got %s", slug, dur)
	}
	// Defined, not merely named. An unknown slug means somebody configured a
	// trigger against an event that does not exist, and silently doing nothing
	// is how that stays unnoticed until the day the reward should have fired.
	if _, ok, err := s.store.GetEvent(ctx, slug); err != nil {
		return pluginapi.EventWindow{}, false, err
	} else if !ok {
		return pluginapi.EventWindow{}, false, fmt.Errorf("events: open %q: no such event — define it before triggering it", slug)
	}

	now := s.now()
	if open, err := s.store.OpenWindows(ctx, []string{slug}, now); err != nil {
		return pluginapi.EventWindow{}, false, err
	} else if w, ok := open[slug]; ok {
		// Already running. Not an error and not an extension: a goal met twice
		// in one week does not buy a second week, it lands inside the week that
		// is already running.
		return w, false, nil
	}

	start := now.Truncate(time.Second)
	w := pluginapi.EventWindow{Slug: slug, Starts: start, Ends: start.Add(dur)}
	n, err := s.store.InsertWindows(ctx, slug, []pluginapi.EventWindow{w})
	if err != nil {
		return pluginapi.EventWindow{}, false, err
	}
	if n == 0 {
		// Somebody else won the same second. Report THEIR window rather than
		// the one this call composed, so two callers never disagree about when
		// it ends.
		open, err := s.store.OpenWindows(ctx, []string{slug}, now)
		if err != nil {
			return pluginapi.EventWindow{}, false, err
		}
		return open[slug], false, nil
	}
	return w, true, nil
}
