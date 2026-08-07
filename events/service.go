package events

import (
	"context"
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
