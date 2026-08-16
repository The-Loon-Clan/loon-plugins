package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// A service over a MemStore with a frozen clock, so window boundaries are not
// decided by when the suite happens to run.
func openFixture(t *testing.T, at time.Time) *Service {
	t.Helper()
	return openFixtureClock(t, func() time.Time { return at })
}

// openFixtureClock is openFixture with the clock supplied, for the test that
// needs a clock which MOVES within one second — see the concurrency test.
func openFixtureClock(t *testing.T, now func() time.Time) *Service {
	t.Helper()
	m := NewMemStore()
	if err := m.UpsertEvent(context.Background(), pluginapi.ScheduledEvent{
		Slug: "site-freeleech", Name: "Site freeleech", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	s := NewService(m)
	s.now = now
	return s
}

// Opening twice while it is running must not extend it.
//
// The callers are webhooks and event handlers, which are retried, and a goal
// met twice in one week does not buy a second week — it lands inside the week
// already running. An implementation that inserted on every call would ratchet
// the end date forward on every retry, quietly making a seven-day freeleech
// permanent for as long as donations kept arriving.
func TestOpenWindowIsIdempotentWhileOpen(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	s := openFixture(t, at)
	ctx := context.Background()

	first, opened, err := s.OpenWindow(ctx, "site-freeleech", 7*24*time.Hour)
	if err != nil || !opened {
		t.Fatalf("first call: opened=%v err=%v", opened, err)
	}
	second, opened, err := s.OpenWindow(ctx, "site-freeleech", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Error("the second call claimed to have opened a window that was already open")
	}
	if !second.Ends.Equal(first.Ends) {
		t.Errorf("the window end moved from %s to %s — a retry extended it",
			first.Ends, second.Ends)
	}
}

// An undefined slug is an ERROR, not a silent no-op.
//
// InsertWindows matches on `WHERE e.slug = $1`, so a slug nobody defined
// inserts nothing and returns 0 — indistinguishable from "already open" unless
// this is checked first. A reward configured against a mistyped slug would then
// never fire and never say why.
func TestOpenWindowRefusesAnUndefinedEvent(t *testing.T) {
	s := openFixture(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	if _, opened, err := s.OpenWindow(context.Background(), "no-such-event", time.Hour); err == nil {
		t.Errorf("opening an undefined event succeeded (opened=%v)", opened)
	}
}

func TestOpenWindowRefusesANonPositiveDuration(t *testing.T) {
	s := openFixture(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	for _, d := range []time.Duration{0, -time.Hour} {
		if _, _, err := s.OpenWindow(context.Background(), "site-freeleech", d); err == nil {
			t.Errorf("duration %s was accepted; it would store ends_at <= starts_at", d)
		}
	}
}

// Exactly one concurrent caller may be told it opened the window.
//
// This is the branch the truncation exists for. Several goroutines can pass the
// "is one already open" check before any of them inserts; what separates them
// is UNIQUE (event_id, starts_at), and that only bites if they all compose the
// SAME start. Without Truncate their microseconds differ, every insert
// succeeds, the event ends up with N overlapping windows, and N callers each
// believe they are the one that should announce it.
func TestOpenWindowLetsExactlyOneCallerWinTheSameSecond(t *testing.T) {
	// A clock that MOVES, which is the whole point: every caller reads a
	// different nanosecond inside the same second, exactly as time.Now would.
	// Under a frozen clock Truncate is a no-op and this test would pass with
	// the truncation deleted — proving the UNIQUE works and nothing about the
	// thing the UNIQUE needs in order to work.
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var tick atomic.Int64
	s := openFixtureClock(t, func() time.Time {
		return base.Add(time.Duration(tick.Add(1)) * time.Nanosecond)
	})
	ctx := context.Background()

	const callers = 8
	var wg sync.WaitGroup
	results := make([]bool, callers)
	ends := make([]time.Time, callers)
	errs := make([]error, callers)
	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			w, opened, err := s.OpenWindow(ctx, "site-freeleech", 7*24*time.Hour)
			results[i], ends[i], errs[i] = opened, w.Ends, err
		}()
	}
	wg.Wait()

	won := 0
	for i, opened := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if opened {
			won++
		}
		// Every caller must be told the same end, winner or not. A caller that
		// got its own composed window back would announce a different end date
		// from the one actually stored.
		if !ends[i].Equal(ends[0]) {
			t.Errorf("caller %d saw the window ending %s, caller 0 saw %s",
				i, ends[i], ends[0])
		}
	}
	if won != 1 {
		t.Errorf("%d of %d concurrent callers were told they opened the window; want exactly 1", won, callers)
	}
}
