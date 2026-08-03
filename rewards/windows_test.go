package rewards

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func str(s string) *string               { return &s }
func dur(d time.Duration) *time.Duration { return &d }

// A contiguous reset must leave NO gap between windows: every instant belongs
// to exactly one. A gap is a moment where a daily reward is not earnable at
// all, and an overlap is a moment where it is earnable twice.
func TestGenerateWindowsResetIsContiguous(t *testing.T) {
	ev := Event{ID: 1, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC"}
	got, err := GenerateWindows(ev, mustTime(t, "2026-03-01T00:00:00Z"), mustTime(t, "2026-03-05T00:00:00Z"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("windows = %d, want 5 (Mar 1..5 inclusive of the from-instant firing)", len(got))
	}
	for i, w := range got {
		if w.EndsAt.Sub(w.StartsAt) != 24*time.Hour {
			t.Errorf("window %d spans %v, want 24h", i, w.EndsAt.Sub(w.StartsAt))
		}
		if i > 0 && !got[i-1].EndsAt.Equal(w.StartsAt) {
			t.Errorf("gap between window %d (ends %s) and %d (starts %s)",
				i-1, got[i-1].EndsAt, i, w.StartsAt)
		}
	}
}

// The from-instant landing exactly on a firing must produce that firing. Cron
// Next() is strictly-after, so the naive version silently loses the first day
// -- and "the daily reward did not exist today" is not a loud failure.
func TestGenerateWindowsIncludesFiringAtFrom(t *testing.T) {
	ev := Event{ID: 1, Slug: "daily", Cron: str("0 0 * * *"), Timezone: "UTC"}
	got, err := GenerateWindows(ev, mustTime(t, "2026-03-01T00:00:00Z"), mustTime(t, "2026-03-01T12:00:00Z"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("windows = %d, want 1", len(got))
	}
	if want := mustTime(t, "2026-03-01T00:00:00Z"); !got[0].StartsAt.Equal(want) {
		t.Errorf("first window starts %s, want %s", got[0].StartsAt, want)
	}
}

// A season closes after its duration and leaves a gap until the next one --
// which is exactly what makes "only during summer" mean something.
func TestGenerateWindowsSeasonHasGaps(t *testing.T) {
	ev := Event{ID: 2, Slug: "summer", Cron: str("0 0 1 7 *"), Duration: dur(64 * 24 * time.Hour), Timezone: "UTC"}
	got, err := GenerateWindows(ev, mustTime(t, "2026-01-01T00:00:00Z"), mustTime(t, "2028-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("windows = %d, want 2 (2026 and 2027)", len(got))
	}
	if want := mustTime(t, "2026-07-01T00:00:00Z"); !got[0].StartsAt.Equal(want) {
		t.Errorf("first summer starts %s, want %s", got[0].StartsAt, want)
	}
	if want := mustTime(t, "2026-09-03T00:00:00Z"); !got[0].EndsAt.Equal(want) {
		t.Errorf("first summer ends %s, want %s (64 days)", got[0].EndsAt, want)
	}
	if !got[0].EndsAt.Before(got[1].StartsAt) {
		t.Error("no gap between consecutive summers; a season must close")
	}
}

// "Weekends only" and "1st of every month" were the asks that put cron back in
// the schema. If these do not work the column is not earning its cost.
func TestGenerateWindowsCalendarShapes(t *testing.T) {
	for _, tc := range []struct {
		name, expr    string
		from, until   string
		want          int
		wantFirstDate string
	}{
		{"weekends", "0 0 * * 6,0", "2026-03-01T00:00:00Z", "2026-03-15T00:00:00Z", 5, "2026-03-01"},
		{"first of month", "0 0 1 * *", "2026-01-01T00:00:00Z", "2026-06-15T00:00:00Z", 6, "2026-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := Event{ID: 3, Slug: tc.name, Cron: str(tc.expr), Duration: dur(24 * time.Hour), Timezone: "UTC"}
			got, err := GenerateWindows(ev, mustTime(t, tc.from), mustTime(t, tc.until))
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("windows = %d, want %d", len(got), tc.want)
			}
			if d := got[0].StartsAt.Format("2006-01-02"); d != tc.wantFirstDate {
				t.Errorf("first window on %s, want %s", d, tc.wantFirstDate)
			}
		})
	}
}

// The timezone is load-bearing: a daily reset at "midnight" fires at a
// different absolute instant depending on where the members are, and getting
// it wrong shifts every reward boundary by hours.
func TestGenerateWindowsHonoursTimezone(t *testing.T) {
	ev := Event{ID: 4, Slug: "daily-ny", Cron: str("0 0 * * *"), Timezone: "America/New_York"}
	got, err := GenerateWindows(ev, mustTime(t, "2026-03-01T00:00:00Z"), mustTime(t, "2026-03-02T00:00:00Z"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("windows = %d, want 1", len(got))
	}
	// Midnight in New York on 1 March 2026 is 05:00 UTC (EST, UTC-5).
	if want := mustTime(t, "2026-03-01T05:00:00Z"); !got[0].StartsAt.UTC().Equal(want) {
		t.Errorf("window starts %s, want %s", got[0].StartsAt.UTC(), want)
	}
}

// A DST spring-forward day is 23 hours long in local terms. A contiguous reset
// must still hand over cleanly across it -- no gap, no overlap -- because the
// end of one window is defined as the next firing rather than start+24h.
func TestGenerateWindowsAcrossDST(t *testing.T) {
	ev := Event{ID: 5, Slug: "daily-ny", Cron: str("0 0 * * *"), Timezone: "America/New_York"}
	// US DST begins 8 March 2026.
	got, err := GenerateWindows(ev, mustTime(t, "2026-03-07T05:00:00Z"), mustTime(t, "2026-03-10T05:00:00Z"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("windows = %d, want at least 3", len(got))
	}
	var found23h bool
	for i, w := range got {
		if i > 0 && !got[i-1].EndsAt.Equal(w.StartsAt) {
			t.Errorf("gap across DST between window %d and %d", i-1, i)
		}
		if w.EndsAt.Sub(w.StartsAt) == 23*time.Hour {
			found23h = true
		}
	}
	if !found23h {
		t.Error("no 23-hour window across the spring-forward day; the duration was not derived from the next firing")
	}
}

// A one-off event's windows are authored by hand. The generator runs across
// every event and must ignore these rather than fail on them.
func TestGenerateWindowsSkipsOneOff(t *testing.T) {
	got, err := GenerateWindows(Event{ID: 6, Slug: "launch"}, time.Now(), time.Now().Add(time.Hour))
	if err != nil || got != nil {
		t.Errorf("one-off event: got %d windows, err %v; want 0/nil", len(got), err)
	}
}

// A bad expression must be caught where someone can read the message.
func TestValidateCron(t *testing.T) {
	if err := ValidateCron("0 0 * * *"); err != nil {
		t.Errorf("valid expression rejected: %v", err)
	}
	if err := ValidateCron("not a cron"); err == nil {
		t.Error("garbage expression accepted")
	}
	// Six fields is the trap this parser choice exists to avoid: a
	// seconds-optional parser would accept it and shift every field.
	if err := ValidateCron("0 0 0 * * *"); err == nil {
		t.Error("six-field expression accepted; the parser must be five-field only")
	}
}

// An unbounded generation request must be refused rather than eating memory.
func TestGenerateWindowsRefusesRunaway(t *testing.T) {
	ev := Event{ID: 7, Slug: "per-minute", Cron: str("* * * * *"), Duration: dur(time.Minute), Timezone: "UTC"}
	_, err := GenerateWindows(ev, mustTime(t, "2026-01-01T00:00:00Z"), mustTime(t, "2126-01-01T00:00:00Z"))
	if err == nil {
		t.Error("a century of a per-minute cron was accepted; it must be refused")
	}
}
