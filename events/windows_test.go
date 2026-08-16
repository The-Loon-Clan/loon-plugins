package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

// The two cases rewards could not express, which are the reason this plugin
// exists as more than a move.
//
// rewards had CHECK (duration IS NULL OR cron IS NOT NULL), so a duration
// required a recurrence — "launch week: starts 1 Sep, runs 7 days" was
// unwritable. And its generator returned nil for any cron-less event ("windows
// authored by hand"), so a plain announcement date produced nothing at all.
func TestOneOffWindows(t *testing.T) {
	from, until := at("2026-08-01T00:00:00Z"), at("2026-10-01T00:00:00Z")

	t.Run("with a duration, closes at start+duration", func(t *testing.T) {
		ev := pluginapi.ScheduledEvent{
			Slug: "launch-week", Enabled: true,
			StartsAt: ptr(at("2026-09-01T00:00:00Z")),
			Duration: 7 * 24 * time.Hour,
		}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) != 1 {
			t.Fatalf("got %d windows, want 1", len(ws))
		}
		if !ws[0].Ends.Equal(at("2026-09-08T00:00:00Z")) {
			t.Errorf("ends at %s, want 2026-09-08", ws[0].Ends)
		}
		if ws[0].Perpetual() {
			t.Error("a bounded one-off reported itself perpetual")
		}
	})

	t.Run("with no duration, never closes", func(t *testing.T) {
		// The operator's rule: "if the date has no duration, it's always
		// displayed after that date".
		ev := pluginapi.ScheduledEvent{
			Slug: "anniversary", Enabled: true,
			StartsAt: ptr(at("2026-09-01T00:00:00Z")),
		}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) != 1 {
			t.Fatalf("got %d windows, want 1", len(ws))
		}
		if !ws[0].Perpetual() {
			t.Errorf("ends at %s; a one-off with no duration must never close", ws[0].Ends)
		}
		// Open a decade later, which is the whole point.
		if !ws[0].Contains(at("2036-01-01T00:00:00Z")) {
			t.Error("not open ten years on")
		}
		if ws[0].Contains(at("2026-08-31T23:59:59Z")) {
			t.Error("open BEFORE its start date")
		}
	})

	t.Run("beyond the horizon, generated later not now", func(t *testing.T) {
		ev := pluginapi.ScheduledEvent{
			Slug: "far-future", Enabled: true,
			StartsAt: ptr(at("2027-01-01T00:00:00Z")),
		}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) != 0 {
			t.Errorf("got %d windows for a start past `until`, want 0", len(ws))
		}
	})

	// This used to assert the opposite — "a one-off with no start date is an
	// error, not a silent no-op" — and that was right while the generator was
	// the only thing that could open a window. An event with neither a cron nor
	// a start could never open, so accepting one silently was a definition an
	// operator would later swear they had configured.
	//
	// OpenWindow changed what the combination MEANS. It is now the way to say
	// "this opens when something happens": a fundraising goal met, a milestone
	// crossed, a switch thrown. The generator must leave it alone rather than
	// invent a window on a date nobody chose — and must not error either, since
	// it runs across every event and one triggered definition would otherwise
	// fail the whole pass.
	t.Run("a triggered event generates nothing, and that is not an error", func(t *testing.T) {
		ev := pluginapi.ScheduledEvent{Slug: "site-freeleech", Enabled: true}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatalf("the generator refused a triggered event: %v", err)
		}
		if len(ws) != 0 {
			t.Errorf("the generator invented %d window(s) for an event with no schedule", len(ws))
		}
	})

	// A one-off that DOES name a start is unchanged: it is scheduled, and a
	// missing start on something otherwise scheduled is still a fault. Kept so
	// relaxing the rule above cannot be read as relaxing it everywhere.
	t.Run("a one-off is still generated from its start date", func(t *testing.T) {
		at := from.Add(2 * time.Hour)
		ev := pluginapi.ScheduledEvent{Slug: "launch", Enabled: true, StartsAt: &at, Duration: time.Hour}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) != 1 || !ws[0].Starts.Equal(at) {
			t.Errorf("got %d window(s) starting %v, want 1 starting %v", len(ws), ws, at)
		}
	})
}

// Recurring behaviour is a straight port and must not have drifted: the
// contiguous case is what a daily reset depends on.
func TestRecurringWindowsUnchanged(t *testing.T) {
	from, until := at("2026-08-01T00:00:00Z"), at("2026-08-04T00:00:00Z")

	t.Run("no duration is contiguous, not perpetual", func(t *testing.T) {
		ev := pluginapi.ScheduledEvent{Slug: "daily", Cron: "0 0 * * *", Enabled: true, Timezone: "UTC"}
		ws, err := GenerateWindows(ev, from, until)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) < 3 {
			t.Fatalf("got %d windows over 3 days, want at least 3", len(ws))
		}
		// Each window must END exactly where the next BEGINS. A gap is a day the
		// event does not exist; an overlap double-counts.
		for i := 1; i < len(ws); i++ {
			if !ws[i-1].Ends.Equal(ws[i].Starts) {
				t.Fatalf("window %d ends %s but %d starts %s — contiguity broken",
					i-1, ws[i-1].Ends, i, ws[i].Starts)
			}
		}
		if ws[0].Perpetual() {
			t.Error("a contiguous recurring window reported itself perpetual; only a one-off can be")
		}
	})

	t.Run("a from landing exactly on a firing keeps that firing", func(t *testing.T) {
		// The off-by-one the -1ns cursor exists to prevent: generating from
		// midnight for a midnight-daily event must not lose the first day.
		ev := pluginapi.ScheduledEvent{Slug: "daily", Cron: "0 0 * * *", Enabled: true}
		ws, err := GenerateWindows(ev, at("2026-08-01T00:00:00Z"), at("2026-08-02T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) == 0 || !ws[0].Starts.Equal(at("2026-08-01T00:00:00Z")) {
			t.Fatalf("first window starts %v, want exactly the `from` instant", ws)
		}
	})

	t.Run("a bad cron names itself", func(t *testing.T) {
		ev := pluginapi.ScheduledEvent{Slug: "broken", Cron: "not a cron", Enabled: true}
		if _, err := GenerateWindows(ev, from, until); err == nil {
			t.Error("accepted a malformed cron")
		}
	})
}

// A disabled event generates nothing, and generation is idempotent. Both matter
// to the job, which runs over every event every tick.
func TestGeneratorIsInertWhereItShouldBe(t *testing.T) {
	from, until := at("2026-08-01T00:00:00Z"), at("2026-09-01T00:00:00Z")
	ev := pluginapi.ScheduledEvent{Slug: "off", Cron: "0 0 * * *", Enabled: false}
	ws, err := GenerateWindows(ev, from, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 0 {
		t.Errorf("a disabled event generated %d windows", len(ws))
	}
}

// The MemStore must enforce what the schema enforces, or a test passes on code
// Postgres would reject.
func TestMemStoreKeepsTheSchemaInvariants(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	ev := pluginapi.ScheduledEvent{Slug: "season", Cron: "0 0 1 * *", Enabled: true}
	if err := m.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}

	ws := []pluginapi.EventWindow{
		{Starts: at("2026-08-01T00:00:00Z"), Ends: at("2026-09-01T00:00:00Z")},
		{Starts: at("2026-09-01T00:00:00Z"), Ends: at("2026-10-01T00:00:00Z")},
	}
	n, err := m.InsertWindows(ctx, "season", ws)
	if err != nil || n != 2 {
		t.Fatalf("first insert: n=%d err=%v", n, err)
	}
	// The UNIQUE (event_id, starts_at) no-op the generator relies on to be able
	// to re-run over a range it already covered.
	n, err = m.InsertWindows(ctx, "season", ws)
	if err != nil || n != 0 {
		t.Fatalf("re-insert added %d window(s); generation is not idempotent", n)
	}
	// The FK: no event, no windows.
	if n, _ := m.InsertWindows(ctx, "no-such-event", ws); n != 0 {
		t.Errorf("inserted %d window(s) for an event that does not exist", n)
	}

	open, err := m.OpenWindows(ctx, []string{"season"}, at("2026-08-15T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := open["season"]; !ok {
		t.Error("mid-window and not open")
	}
	// Half-open boundary: the instant a window ends belongs to the NEXT one.
	open, _ = m.OpenWindows(ctx, []string{"season"}, at("2026-09-01T00:00:00Z"))
	if w, ok := open["season"]; !ok || !w.Starts.Equal(at("2026-09-01T00:00:00Z")) {
		t.Errorf("at the boundary got %+v; the later window must win", w)
	}

	// Deleting the event must take its windows, or it stays open after removal.
	if err := m.DeleteEvent(ctx, "season"); err != nil {
		t.Fatal(err)
	}
	if all, _ := m.AllOpen(ctx, at("2026-08-15T00:00:00Z")); len(all) != 0 {
		t.Errorf("a deleted event is still open: %v", all)
	}
}

// NextOpen has to answer from the definition, because the window table only
// reaches to the horizon — and the event an operator most wants a date for is
// the yearly one whose next firing is beyond it.
func TestNextStartLooksPastTheHorizon(t *testing.T) {
	ev := pluginapi.ScheduledEvent{Slug: "yearly", Cron: "0 0 1 1 *", Enabled: true}
	next, err := NextStart(ev, at("2026-08-07T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(at("2027-01-01T00:00:00Z")) {
		t.Errorf("next = %s, want 2027-01-01 — five months out, well past the 45-day horizon", next)
	}

	past := pluginapi.ScheduledEvent{Slug: "done", Enabled: true, StartsAt: ptr(at("2020-01-01T00:00:00Z"))}
	if next, _ := NextStart(past, at("2026-08-07T00:00:00Z")); !next.IsZero() {
		t.Errorf("a past one-off reports next=%s, want never", next)
	}
	off := pluginapi.ScheduledEvent{Slug: "off", Cron: "0 0 * * *", Enabled: false}
	if next, _ := NextStart(off, at("2026-08-07T00:00:00Z")); !next.IsZero() {
		t.Errorf("a disabled event reports next=%s, want never", next)
	}
}

// The occurrence key is what other plugins store, so its properties matter more
// than its format: stable for one occurrence, different between occurrences, and
// derived from the slug rather than from a row id.
//
// The operator rejected a bare timestamp for exactly this reason — an identifier
// several systems share should carry the name, so a row in some other plugin's
// table says WHICH event it belongs to without a join.
func TestOccurrenceKeyIsSlugQualifiedAndStable(t *testing.T) {
	ev := pluginapi.ScheduledEvent{Slug: "summer-2026", Cron: "0 0 1 * *", Enabled: true, Timezone: "UTC"}
	ws, err := GenerateWindows(ev, at("2026-06-01T00:00:00Z"), at("2026-09-02T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) < 2 {
		t.Fatalf("got %d windows, want at least 2 to compare occurrences", len(ws))
	}

	// Carries the name, so a stored key is self-describing.
	if !strings.HasPrefix(ws[0].Key(), "summer-2026@") {
		t.Errorf("key %q does not name its event", ws[0].Key())
	}
	// Distinct per occurrence — the property a recurring payout depends on. Keyed
	// on the event alone it would pay once ever.
	if ws[0].Key() == ws[1].Key() {
		t.Errorf("two occurrences share the key %q", ws[0].Key())
	}
	// Stable: regenerating the same range must reproduce the same keys, or a
	// consumer's stored reference stops matching and pays again.
	again, err := GenerateWindows(ev, at("2026-06-01T00:00:00Z"), at("2026-09-02T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range ws {
		if ws[i].Key() != again[i].Key() {
			t.Fatalf("regeneration changed key %d: %q became %q", i, ws[i].Key(), again[i].Key())
		}
	}
	// Timezone-independent: the same instant in a different zone is the same
	// occurrence, so the key must not shift with the operator's zone.
	tokyo := pluginapi.EventWindow{Slug: "x", Starts: at("2026-08-01T00:00:00Z").In(time.FixedZone("JST", 9*3600))}
	utc := pluginapi.EventWindow{Slug: "x", Starts: at("2026-08-01T00:00:00Z")}
	if tokyo.Key() != utc.Key() {
		t.Errorf("the same instant keyed two ways: %q vs %q", tokyo.Key(), utc.Key())
	}
}
