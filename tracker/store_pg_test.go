//go:build integration

// Integration tests for the tracker PGStore. Gated behind the `integration` tag;
// set TRACKER_TEST_DSN to a throwaway database.
//
// The plugin shipped with a MemStore and no integration tests, which means every
// line of its SQL was going to reach production having never run. That is the
// worse half of the arrangement, because MemStore reproduces whatever its author
// believed the SQL did — and this store's three most important behaviours live
// entirely in ON CONFLICT clauses:
//
//   - byte counters ADD, left_bytes REPLACES, completed is STICKY
//   - rotated_at is stamped only when a passkey actually CHANGES (a CASE)
//   - seeding/leeching depend on a `last_seen > now() - interval '1 hour'` window
//
// Every one of those is wrong in a way that looks fine until somebody checks a
// member's ratio.

package tracker

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
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TRACKER_TEST_DSN")
	if dsn == "" {
		t.Skip("TRACKER_TEST_DSN not set; skipping. Point it at a throwaway database.")
	}
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS tracker CASCADE; CREATE SCHEMA tracker`); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Every migration, in order, applied INSIDE the schema — then the session put
	// straight back to public. Both halves matter and the events harness learned
	// each the hard way: naming one file means a later migration's columns are
	// missing here and present in production, and leaving the SESSION scoped means
	// a store that had lost its SchemaDB scoping still passes, which is how a
	// suite goes green against code that fails on its first real request.
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found; the harness would test an empty schema")
	}
	sort.Strings(files)
	if _, err := db.Exec("SET search_path = tracker"); err != nil {
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

	// Nothing may have landed in public. A migration missing its schema
	// qualification is invisible until it collides with a host table of the same
	// name, and "torrents" is exactly the kind of name that collides.
	for _, tbl := range []string{"torrents", "passkeys", "user_stats"} {
		var leaked *string
		if err := db.Get(&leaked, "SELECT to_regclass('public."+tbl+"')::text"); err != nil {
			t.Fatalf("leak check %s: %v", tbl, err)
		}
		if leaked != nil {
			t.Fatalf("migration created public.%s; it must live in the plugin schema", tbl)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// pgStore returns the store plus the raw pool, because a few assertions have to
// reach past the store to set up state the store deliberately cannot (aging a
// last_seen that the upsert stamps with now()).
//
// The raw pool is NOT search_path-scoped — that is the point of the harness
// resetting it — so direct queries here qualify the schema explicitly.
func pgStore(t *testing.T) (*PGStore, *sqlx.DB) {
	t.Helper()
	db := testDB(t)
	return NewPGStore(core.NewStorage(db).SchemaDB("tracker")), db
}

const ih1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func seedTorrent(t *testing.T, s *PGStore, hash, name string) {
	t.Helper()
	if err := s.UpsertTorrent(context.Background(), &Torrent{
		InfoHash: hash, Name: name, Size: 1 << 20, FileCount: 3,
		InfoBytes: []byte("original-info-bytes"),
	}); err != nil {
		t.Fatalf("seed %s: %v", hash, err)
	}
}

// The three announce behaviours, against the real ON CONFLICT rather than a
// reimplementation of it.
func TestPGApplyAnnounceDeltaSemantics(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	seedTorrent(t, s, ih1, "Release")

	if err := s.ApplyAnnounceDelta(ctx, 7, ih1, 100, 200, 800, false); err != nil {
		t.Fatalf("first announce: %v", err)
	}
	if err := s.ApplyAnnounceDelta(ctx, 7, ih1, 50, 300, 0, true); err != nil {
		t.Fatalf("second announce: %v", err)
	}

	rows, err := s.ListUserStats(ctx, 7, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — the upsert inserted instead of updating", len(rows))
	}
	got := rows[0]
	if got.Uploaded != 150 || got.Downloaded != 500 {
		t.Errorf("up=%d down=%d, want 150/500 — deltas must ADD", got.Uploaded, got.Downloaded)
	}
	if got.LeftBytes != 0 {
		t.Errorf("left=%d, want 0 — left_bytes REPLACES", got.LeftBytes)
	}
	if !got.Completed {
		t.Error("completed did not stick")
	}
	// The join the "my stats" page needs.
	if got.Name != "Release" {
		t.Errorf("name=%q, want the joined torrent name", got.Name)
	}

	// A later announce with completed=false must not undo the snatch. A client
	// that keeps announcing after finishing sends exactly this.
	if err := s.ApplyAnnounceDelta(ctx, 7, ih1, 10, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListUserStats(ctx, 7, 10)
	if !rows[0].Completed {
		t.Error("a later completed=false announce cleared the snatch")
	}
	if rows[0].Uploaded != 160 {
		t.Errorf("up=%d, want 160 — seeding after completion still credits upload", rows[0].Uploaded)
	}
}

// The foreign key is real, not a MemStore courtesy.
func TestPGAnnounceForUnknownTorrentIsRefused(t *testing.T) {
	s := mustStore(t)
	if err := s.ApplyAnnounceDelta(context.Background(), 7, "deadbeef", 1, 1, 0, false); err == nil {
		t.Error("accepted a stat row for an unregistered torrent")
	}
}

// rotated_at is stamped only when the passkey actually changes — the CASE in the
// upsert. Re-storing the same key must not look like a rotation, because a member
// reading "rotated 5 seconds ago" concludes their torrents just broke.
func TestPGSetPasskeyStampsRotatedAtOnlyOnChange(t *testing.T) {
	ctx := context.Background()
	s, raw := pgStore(t)

	if err := s.SetPasskey(ctx, 7, "key-one"); err != nil {
		t.Fatal(err)
	}
	var first *time.Time
	if err := raw.Get(&first, `SELECT rotated_at FROM tracker.passkeys WHERE user_id = 7`); err != nil {
		t.Fatalf("read rotated_at: %v", err)
	}

	// Same key again: not a rotation.
	if err := s.SetPasskey(ctx, 7, "key-one"); err != nil {
		t.Fatal(err)
	}
	var same *time.Time
	if err := raw.Get(&same, `SELECT rotated_at FROM tracker.passkeys WHERE user_id = 7`); err != nil {
		t.Fatal(err)
	}
	if (first == nil) != (same == nil) || (first != nil && !first.Equal(*same)) {
		t.Errorf("re-storing the same passkey moved rotated_at (%v -> %v)", first, same)
	}

	// A different key IS a rotation.
	if err := s.SetPasskey(ctx, 7, "key-two"); err != nil {
		t.Fatal(err)
	}
	var after *time.Time
	if err := raw.Get(&after, `SELECT rotated_at FROM tracker.passkeys WHERE user_id = 7`); err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("rotated_at still null after a real rotation")
	}
	if first != nil && !after.After(*first) {
		t.Errorf("rotated_at did not advance on a real rotation (%v -> %v)", first, after)
	}

	// And the old key stops resolving.
	if _, ok, _ := s.UserByPasskey(ctx, "key-one"); ok {
		t.Error("the rotated-away passkey still resolves")
	}
	if id, ok, _ := s.UserByPasskey(ctx, "key-two"); !ok || id != 7 {
		t.Errorf("new passkey resolved to %d/%v, want 7/true", id, ok)
	}
}

// UNIQUE across members: an announce carries nothing else to say who it is from.
func TestPGPasskeyIsUniqueAcrossMembers(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	if err := s.SetPasskey(ctx, 7, "shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasskey(ctx, 8, "shared"); err == nil {
		t.Error("two members share a passkey; every announce from it is ambiguous")
	}
}

// Totals' seeding/leeching read a one-hour activity window, so a peer that
// stopped announcing is neither — however empty its left_bytes.
func TestPGTotalsUseTheActivityWindow(t *testing.T) {
	ctx := context.Background()
	s, raw := pgStore(t)
	const ihStale = "cccccccccccccccccccccccccccccccccccccccc"
	seedTorrent(t, s, ih1, "seeding-now")
	seedTorrent(t, s, ihStale, "finished-but-gone")

	if err := s.ApplyAnnounceDelta(ctx, 7, ih1, 10, 20, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAnnounceDelta(ctx, 7, ihStale, 10, 20, 0, true); err != nil {
		t.Fatal(err)
	}
	// Age the second one past the window. Done in SQL because last_seen is set by
	// now() inside the upsert — there is no clock to inject here, which is itself
	// why this needs a real database to test.
	if _, err := raw.Exec(
		`UPDATE tracker.user_stats SET last_seen = now() - interval '3 hours' WHERE info_hash = $1`, ihStale); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	tot, err := s.Totals(ctx, 7)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if tot.Seeding != 1 {
		t.Errorf("seeding=%d, want 1 — a stale complete peer is not seeding", tot.Seeding)
	}
	if tot.Snatched != 2 {
		t.Errorf("snatched=%d, want 2 — completion is history and does not expire", tot.Snatched)
	}
	if tot.Uploaded != 20 || tot.Downloaded != 40 {
		t.Errorf("up=%d down=%d, want 20/40", tot.Uploaded, tot.Downloaded)
	}
}

// UpsertTorrent on conflict refreshes the name, fills provenance only when
// absent, and NEVER rewrites info_bytes — the info_hash is their hash, so a
// conflict is the same torrent by definition.
func TestPGUpsertTorrentPreservesInfoBytes(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	seedTorrent(t, s, ih1, "Original Name")

	owner := int64(42)
	if err := s.UpsertTorrent(ctx, &Torrent{
		InfoHash: ih1, Name: "Renamed", Size: 1 << 20,
		InfoBytes: []byte("DIFFERENT-BYTES"), UploadedBy: &owner,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := s.Torrent(ctx, ih1)
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Name != "Renamed" {
		t.Errorf("name=%q, want the refreshed name", got.Name)
	}
	if string(got.InfoBytes) != "original-info-bytes" {
		t.Errorf("info_bytes were rewritten to %q; the info_hash no longer describes them", got.InfoBytes)
	}
	if got.UploadedBy == nil || *got.UploadedBy != 42 {
		t.Error("provenance was not filled in when it had been absent")
	}
}

// Absence is nil, nil — not an error. An announce for an unknown hash is an
// ordinary event the caller answers with a bencoded failure.
func TestPGUnknownTorrentIsAbsentNotAnError(t *testing.T) {
	got, err := mustStore(t).Torrent(context.Background(), ih1)
	if err != nil {
		t.Errorf("err=%v, want nil for a missing row", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// ListTorrents deliberately omits info_bytes: it is the largest column in the
// table and a listing that selected it would drag every torrent's info dict
// through the page render.
func TestPGListTorrentsOmitsInfoBytes(t *testing.T) {
	ctx := context.Background()
	s := mustStore(t)
	seedTorrent(t, s, ih1, "Release")

	rows, total, err := s.ListTorrents(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d rows=%d, want 1/1", total, len(rows))
	}
	if len(rows[0].InfoBytes) != 0 {
		t.Errorf("info_bytes came back in a listing (%d bytes)", len(rows[0].InfoBytes))
	}
	if rows[0].Name != "Release" || rows[0].FileCount != 3 {
		t.Errorf("listing lost scalar columns: %+v", rows[0])
	}
}

// sortBy is interpolated into ORDER BY, so it must be an allowlist and an unknown
// value must fall back rather than error or inject.
func TestPGListAggregatesSortAllowlist(t *testing.T) {
	ctx := context.Background()
	s, raw := pgStore(t)
	seedTorrent(t, s, ih1, "Release")
	if err := s.ApplyAnnounceDelta(ctx, 7, ih1, 10, 20, 0, true); err != nil {
		t.Fatal(err)
	}

	for _, sortBy := range []string{"uploaded", "downloaded", "torrents", "", "nonsense", "uploaded; DROP TABLE user_stats"} {
		rows, total, err := s.ListAggregates(ctx, sortBy, 10, 0)
		if err != nil {
			t.Fatalf("sortBy=%q: %v", sortBy, err)
		}
		if total != 1 || len(rows) != 1 {
			t.Errorf("sortBy=%q: total=%d rows=%d, want 1/1", sortBy, total, len(rows))
		}
	}
	// The table is still there, which is the point of the last vector.
	var n int
	if err := raw.Get(&n, `SELECT count(*) FROM tracker.user_stats`); err != nil {
		t.Fatalf("user_stats is gone: %v", err)
	}
}

// mustStore is pgStore for the tests that need no raw access.
func mustStore(t *testing.T) *PGStore {
	t.Helper()
	s, _ := pgStore(t)
	return s
}
