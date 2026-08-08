//go:build integration

// Integration tests for the events PGStore. Gated behind the `integration` tag;
// set EVENTS_TEST_DSN to a throwaway database.
//
// This plugin shipped with none, which meant every line of its SQL reached
// production having never run: the duration_seconds round trip, the unnest batch
// insert, the DISTINCT ON open-window pick, and a Coverage query built on lead()
// over a LEFT JOIN. A MemStore reproduces whatever its author believed the SQL
// did, and that belief is exactly what is under test.

package events

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("EVENTS_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTS_TEST_DSN not set; skipping. Point it at a throwaway database.")
	}
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS events CASCADE; CREATE SCHEMA events`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// EVERY migration in order, applied INSIDE the schema, with the session put
	// straight back to public afterwards.
	//
	// Both halves matter, and the rewards harness learned each the hard way. All
	// files, because naming one meant a later migration's columns were missing
	// here and present in production — this plugin's 002 turns duration into
	// duration_seconds, so a harness stopping at 001 would test a column that no
	// longer exists. And back to public, because a store that had lost its
	// SchemaDB scoping would still pass if the SESSION were scoped, which is how
	// a suite goes green against code that fails on its first real request.
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("found %d migration(s); the harness would test an incomplete schema", len(files))
	}
	sort.Strings(files)
	if _, err := db.Exec("SET search_path = events"); err != nil {
		t.Fatalf("scope for migration: %v", err)
	}
	for _, f := range files {
		schema, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if _, err := db.Exec("SET search_path = public"); err != nil {
		t.Fatalf("reset search_path: %v", err)
	}
	var leaked *string
	if err := db.Get(&leaked, "SELECT to_regclass('public.event_windows')::text"); err != nil {
		t.Fatalf("check schema isolation: %v", err)
	}
	if leaked != nil {
		t.Fatalf("events tables are visible in public (%s) — the harness is not scoped, so it cannot test scoping", *leaked)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testStore wraps the pool the way Provision does, so every test exercises the
// same scoping path production uses.
func testStore(t *testing.T, db *sqlx.DB) *PGStore {
	t.Helper()
	return NewPGStore(core.NewStorage(db).SchemaDB("events"))
}

func pgAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// duration_seconds is an INTEGER, and NULL is not zero.
//
// Migration 002 changed the column from INTERVAL for exactly this reason: the
// old shape went out through EXTRACT(EPOCH …) and back in as a "%d seconds"
// string for Postgres to re-parse. Two grammars to move one number, and a
// '1 month' interval that is not a fixed length of time.
func TestPGDurationRoundTrip(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	start := pgAt("2026-09-01T00:00:00Z")
	for _, ev := range []pluginapi.ScheduledEvent{
		{Slug: "bounded", Cron: "0 0 1 7 *", Duration: 64 * 24 * time.Hour, Timezone: "Asia/Tokyo", Enabled: true},
		{Slug: "contiguous", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "one-off", StartsAt: &start, Duration: 7 * 24 * time.Hour, Timezone: "UTC", Enabled: false},
	} {
		if err := store.UpsertEvent(ctx, ev); err != nil {
			t.Fatalf("upsert %s: %v", ev.Slug, err)
		}
	}

	got, err := store.ListEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("listed %d events, want 3", len(got))
	}
	by := map[string]pluginapi.ScheduledEvent{}
	for _, ev := range got {
		by[ev.Slug] = ev
	}
	if d := by["bounded"].Duration; d != 64*24*time.Hour {
		t.Errorf("bounded duration = %v, want 1536h", d)
	}
	if tz := by["bounded"].Timezone; tz != "Asia/Tokyo" {
		t.Errorf("timezone = %q, want Asia/Tokyo", tz)
	}
	// NULL means "no duration", which is NOT a zero-length window. If these ever
	// come back the same the contiguous rule silently becomes a zero-length one.
	if d := by["contiguous"].Duration; d != 0 {
		t.Errorf("contiguous duration = %v, want 0 (NULL in the column)", d)
	}
	if by["one-off"].StartsAt == nil || !by["one-off"].StartsAt.Equal(start) {
		t.Errorf("one-off start = %v, want %s", by["one-off"].StartsAt, start)
	}
	if by["one-off"].Enabled {
		t.Error("enabled did not round-trip false")
	}

	// The upsert is an upsert: same slug, new values, no duplicate.
	if err := store.UpsertEvent(ctx, pluginapi.ScheduledEvent{
		Slug: "bounded", Cron: "0 0 1 8 *", Duration: time.Hour, Timezone: "UTC", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.ListEvents(ctx)
	if len(got) != 3 {
		t.Fatalf("after re-upsert there are %d events, want 3", len(got))
	}
	ev, ok, err := store.GetEvent(ctx, "bounded")
	if err != nil || !ok {
		t.Fatalf("get bounded: ok=%v err=%v", ok, err)
	}
	if ev.Duration != time.Hour {
		t.Errorf("after upsert duration = %v, want 1h", ev.Duration)
	}

	// Absence is not an error — a consumer holding a deleted slug must be able
	// to tell "gone" from "the query failed".
	if _, ok, err := store.GetEvent(ctx, "no-such-thing"); err != nil || ok {
		t.Errorf("missing event: ok=%v err=%v, want false/nil", ok, err)
	}
}

// The batch insert, its idempotency, and the CHECK that a duration must be
// positive.
func TestPGInsertWindowsIsIdempotent(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	ev := pluginapi.ScheduledEvent{Slug: "daily", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true}
	if err := store.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	from := pgAt("2026-03-01T00:00:00Z")
	ws, err := GenerateWindows(ev, from, from.Add(5*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) < 4 {
		t.Fatalf("generated %d windows over 5 days, want at least 4", len(ws))
	}

	n, err := store.InsertWindows(ctx, "daily", ws)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if n != len(ws) {
		t.Fatalf("inserted %d of %d", n, len(ws))
	}
	// The UNIQUE (event_id, starts_at) no-op the generator depends on: it resumes
	// from the last window's END and re-covers ground every pass.
	if n, err := store.InsertWindows(ctx, "daily", ws); err != nil || n != 0 {
		t.Fatalf("re-insert added %d window(s) (err=%v); generation is not idempotent", n, err)
	}
	// An overlapping range inserts only what is new.
	ws2, _ := GenerateWindows(ev, from.Add(3*24*time.Hour), from.Add(8*24*time.Hour))
	n, err = store.InsertWindows(ctx, "daily", ws2)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || n == len(ws2) {
		t.Errorf("overlapping insert added %d of %d; want some but not all", n, len(ws2))
	}
	// The FK, for real: no event, no windows, and no error either.
	if n, err := store.InsertWindows(ctx, "no-such-event", ws); err != nil || n != 0 {
		t.Errorf("insert for an unknown slug: n=%d err=%v, want 0/nil", n, err)
	}
}

// The half-open comparison, in Postgres rather than in Go.
//
// The boundary instant must belong to exactly ONE window. If both matched, a
// contiguous reset would hand out a second free claim every midnight — and the
// SQL is the only place that can be checked, because a mock reproduces whatever
// its author believed `starts_at <= $1 AND ends_at > $1` meant.
func TestPGOpenWindowIsHalfOpen(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	ev := pluginapi.ScheduledEvent{Slug: "daily", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true}
	if err := store.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	day := pgAt("2026-03-01T00:00:00Z")
	ws, _ := GenerateWindows(ev, day, day.Add(3*24*time.Hour))
	if _, err := store.InsertWindows(ctx, "daily", ws); err != nil {
		t.Fatal(err)
	}
	boundary := day.Add(24 * time.Hour)

	for _, tc := range []struct {
		name      string
		at        time.Time
		wantStart time.Time
	}{
		{"just after open", day.Add(time.Second), day},
		{"a microsecond before the boundary", boundary.Add(-time.Microsecond), day},
		{"exactly on the boundary", boundary, boundary},
		{"just after the boundary", boundary.Add(time.Second), boundary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.OpenWindows(ctx, []string{"daily"}, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("events open at %s = %d, want exactly 1", tc.at, len(got))
			}
			if !got["daily"].Starts.Equal(tc.wantStart) {
				t.Errorf("window starting %s, want %s", got["daily"].Starts, tc.wantStart)
			}
		})
	}

	// A disabled event reports nothing open, because every query joins on
	// enabled. Disabling is how an operator stops an event without deleting the
	// history consumers may have keyed work on.
	ev.Enabled = false
	if err := store.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.OpenWindows(ctx, []string{"daily"}, day.Add(time.Hour)); len(got) != 0 {
		t.Errorf("a disabled event reported %d open window(s)", len(got))
	}
	if got, _ := store.AllOpen(ctx, day.Add(time.Hour)); len(got) != 0 {
		t.Errorf("AllOpen included a disabled event: %v", got)
	}
}

// The Coverage query: lead() over a LEFT JOIN, which is the shape with the most
// ways to be quietly wrong.
func TestPGCoverage(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	for _, ev := range []pluginapi.ScheduledEvent{
		{Slug: "empty", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "holed", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true},
		{Slug: "season", Cron: "0 0 1 7 *", Duration: 24 * time.Hour, Timezone: "UTC", Enabled: true},
	} {
		if err := store.UpsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	day := pgAt("2026-03-01T00:00:00Z")
	// Contiguous for two days, a missing day, then one more. Exactly one gap.
	// Inserted OUT of chronological order on purpose: lead() is defined by an
	// ORDER BY inside the window, not by insertion order, and getting that wrong
	// counts phantom gaps.
	if _, err := store.InsertWindows(ctx, "holed", []pluginapi.EventWindow{
		{Starts: day.Add(72 * time.Hour), Ends: day.Add(96 * time.Hour)},
		{Starts: day, Ends: day.Add(24 * time.Hour)},
		{Starts: day.Add(24 * time.Hour), Ends: day.Add(48 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWindows(ctx, "season", []pluginapi.EventWindow{
		{Starts: day, Ends: day.Add(24 * time.Hour)},
		{Starts: day.Add(365 * 24 * time.Hour), Ends: day.Add(366 * 24 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}

	cov, err := store.Coverage(ctx)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}

	// THE case the LEFT JOIN exists for. An event with no windows is what the
	// validator most needs to hear about, and an inner join would drop it
	// silently — the validator would then report nothing and look healthy.
	c, ok := cov["empty"]
	if !ok {
		t.Fatal("an event with no windows is absent from coverage; the validator can never report it")
	}
	if c.Windows != 0 || c.Gaps != 0 {
		t.Errorf("empty event: windows=%d gaps=%d, want 0/0", c.Windows, c.Gaps)
	}

	c = cov["holed"]
	if c.Windows != 3 {
		t.Errorf("holed windows = %d, want 3", c.Windows)
	}
	if c.Gaps != 1 {
		t.Errorf("holed gaps = %d, want 1 — insertion order must not change the answer", c.Gaps)
	}
	if !c.LastEnd.UTC().Equal(day.Add(96 * time.Hour)) {
		t.Errorf("holed last end = %s, want day+96h", c.LastEnd)
	}

	// A bounded event's gap is counted too — the validator, not the query,
	// decides that a season's gaps are the design.
	if c := cov["season"]; c.Gaps != 1 {
		t.Errorf("season gaps = %d, want 1 (counted here, ignored by the validator)", c.Gaps)
	}

	// And the whole point, end to end: a stalled generator produces findings.
	p := &Plugin{store: store}
	fs, err := p.Validate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findingFor(fs, "event empty", "no windows at all") == nil {
		t.Errorf("the validator did not report the window-less event: %+v", fs)
	}
	if findingFor(fs, "event holed", "gap(s)") == nil {
		t.Errorf("the validator did not report the gap: %+v", fs)
	}
	if findingFor(fs, "event season", "gap(s)") != nil {
		t.Error("the validator reported a season's gaps, which are what a season IS")
	}
}

// Deleting an event takes its windows with it, and the cascade is the schema's
// job rather than the store's.
func TestPGDeleteCascadesWindows(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	ev := pluginapi.ScheduledEvent{Slug: "doomed", Cron: "0 0 * * *", Timezone: "UTC", Enabled: true}
	if err := store.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	day := pgAt("2026-03-01T00:00:00Z")
	ws, _ := GenerateWindows(ev, day, day.Add(3*24*time.Hour))
	if _, err := store.InsertWindows(ctx, "doomed", ws); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteEvent(ctx, "doomed"); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := db.Get(&orphans, "SELECT count(*) FROM events.event_windows"); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned window(s) survived the delete; anything asking would still see the event as open", orphans)
	}
}

// A perpetual window survives the round trip, and the sentinel is what makes the
// existing ends_at > starts_at CHECK still meaningful.
func TestPGPerpetualWindowRoundTrip(t *testing.T) {
	db := testDB(t)
	store := testStore(t, db)
	ctx := context.Background()

	start := pgAt("2026-03-01T00:00:00Z")
	ev := pluginapi.ScheduledEvent{Slug: "site-launch", StartsAt: &start, Timezone: "UTC", Enabled: true}
	if err := store.UpsertEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	ws, err := GenerateWindows(ev, start.Add(-time.Hour), start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWindows(ctx, "site-launch", ws); err != nil {
		t.Fatal(err)
	}

	// Open a century later, which is what "never closes" has to mean once the
	// value has been through the column.
	got, err := store.OpenWindows(ctx, []string{"site-launch"}, start.Add(100*365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("a perpetual window was not open a century on: %v", got)
	}
	if !got["site-launch"].Perpetual() {
		t.Errorf("window ends %s, which does not read as perpetual after the round trip", got["site-launch"].Ends)
	}
}
