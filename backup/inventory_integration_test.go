//go:build integration

package backup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

func testStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("BACKUP_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKUP_TEST_DSN not set")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema := fmt.Sprintf("backup_t%d", time.Now().UnixNano()%1e9)
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		body, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`SET search_path TO ` + schema + `; ` + string(body)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return NewPGStore(core.NewStorage(db).SchemaDB(schema))
}

// A tiny asset tree, so the pass is exercised end to end rather than in pieces.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole point of the inventory: a second pass over an unchanged tree must
// hash nothing. If it re-hashes everything, a daily index over 131 GB is not
// affordable and the design collapses back to a weekly full copy.
func TestSecondPassHashesNothing(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	// VALID image bytes, not placeholder text. Fake bodies get flagged by the
	// completeness check, and a flagged file is deliberately re-read every pass
	// so a corrected detector can retire its own false positives — which would
	// make this test measure the suspect path rather than the stat gate.
	writeTree(t, root, map[string]string{
		"web/static/mascots/a.png": pngBody("a"),
		"web/static/mascots/b.png": pngBody("b"),
		"web/static/covers/1.jpg":  jpegBody("one"),
		"web/static/covers/2.jpg":  jpegBody("two"),
	})
	classes := []AssetClass{
		{Slug: "mascots", Dir: "web/static/mascots", Order: 10},
		{Slug: "covers", Dir: "web/static/covers", Order: 50},
	}
	p := &Plugin{st: s}

	// Denominator 0 disables the rolling re-hash so this measures the stat gate
	// alone; the rolling re-hash has its own unit test.
	first, err := p.indexPass(context.Background(), root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if first.Files != 4 || first.Hashed != 4 {
		t.Fatalf("first pass: files=%d hashed=%d, want 4/4", first.Files, first.Hashed)
	}

	second, err := p.indexPass(context.Background(), root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if second.Files != 4 {
		t.Errorf("second pass saw %d files, want 4", second.Files)
	}
	if second.Hashed != 0 {
		t.Errorf("second pass re-hashed %d unchanged files; the stat gate is not "+
			"working and a daily index over 131 GB would be unaffordable", second.Hashed)
	}
	if second.Skipped != 4 {
		t.Errorf("second pass carried forward %d, want 4", second.Skipped)
	}
}

// A file edited in place must be noticed and must produce a NEW row, so the
// inventory is a history rather than a snapshot — the old content stays
// addressable for a restore of an earlier generation.
func TestEditedFileGetsANewRow(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	rel := "web/static/site/logo.png"
	writeTree(t, root, map[string]string{rel: pngBody("original")})
	classes := []AssetClass{{Slug: "site", Dir: "web/static/site", Order: 10}}
	p := &Plugin{st: s}

	if _, err := p.indexPass(context.Background(), root, classes, 1<<30); err != nil {
		t.Fatal(err)
	}

	// Rewrite with different content AND a different length, then force a
	// distinct mtime so the gate has something to see.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, rel), []byte(pngBody("replaced")), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.indexPass(context.Background(), root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hashed != 1 {
		t.Errorf("an edited file was not re-hashed (hashed=%d)", res.Hashed)
	}

	// Both contents must survive: one path, two rows.
	var n int
	if err := s.db.WithTx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT count(*) FROM files WHERE path = $1`, rel).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("got %d rows for the edited path, want 2 — the inventory must keep "+
			"the previous content addressable for an older generation's restore", n)
	}
}

// A failed walk must NOT seal. An unsealed generation is the difference between
// "the corpus shrank" and "we could not read it", and only the second is
// recoverable by trying again.
func TestFailedWalkLeavesTheGenerationUnsealed(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"web/static/mascots/a.png": pngBody("a")})
	p := &Plugin{st: s}

	// A class whose directory is a FILE: Walk reports an error rather than an
	// empty listing.
	bad := filepath.Join(root, "web/static/broken")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := s.lastSealedGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.indexPass(context.Background(), root, []AssetClass{
		{Slug: "mascots", Dir: "web/static/mascots", Order: 10},
		{Slug: "broken", Dir: "web/static/broken", Order: 20},
	}, 1<<30)

	after, err := s.lastSealedGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a generation sealed despite a failed walk (%d -> %d); a partial "+
			"walk is indistinguishable from a shrinking corpus and must never be "+
			"treated as authoritative", before, after)
	}
}

// The end-to-end version of the shrink guard, against real per-class totals.
func TestClassTotalsFeedTheShrinkGate(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"web/static/mascots/a.png": pngBody("a"),
		"web/static/mascots/b.png": pngBody("b"),
		"web/static/mascots/c.png": pngBody("c"),
	})
	classes := []AssetClass{{Slug: "mascots", Dir: "web/static/mascots", Order: 10}}
	p := &Plugin{st: s}

	if _, err := p.indexPass(context.Background(), root, classes, 1<<30); err != nil {
		t.Fatal(err)
	}
	gen1, err := s.lastSealedGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prev, err := s.classTotals(context.Background(), gen1)
	if err != nil {
		t.Fatal(err)
	}
	if prev["mascots"].Files != 3 {
		t.Fatalf("class totals not recorded: %+v", prev)
	}

	// Simulate the disaster: the mount vanishes, so the directory reads empty.
	if err := os.RemoveAll(filepath.Join(root, "web/static/mascots")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "web/static/mascots"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := p.indexPass(context.Background(), root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	shrunk := detectShrink(prev, res.PerClass, maxClassShrinkPct)
	if len(shrunk) != 1 || shrunk[0].Class != "mascots" {
		t.Fatalf("an emptied class did not trip the shrink gate: %+v", shrunk)
	}
}

// A suspect row must clear when the file passes. Without this the table is
// append-only: the stat gate skips a flagged file forever, so nothing
// re-examines it, and the count converges on the worst historical moment rather
// than the truth. It hid a corrected detector in production — 4,163 stale rows
// survived a pass that no longer objected to them.
func TestSuspectClearsWhenTheFileIsFine(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	ctx := context.Background()
	rel := "web/static/covers/1.jpg"

	// A zero-byte JPEG: damaged by definition.
	writeTree(t, root, map[string]string{rel: ""})
	classes := []AssetClass{{Slug: "covers", Dir: "web/static/covers", Order: 10}}
	p := &Plugin{st: s}

	res, err := p.indexPass(ctx, root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Suspect != 1 {
		t.Fatalf("a zero-byte image was not flagged (suspect=%d)", res.Suspect)
	}
	paths, err := s.suspectPaths(ctx)
	if err != nil || len(paths) != 1 {
		t.Fatalf("suspect rows = %v (err %v), want 1", paths, err)
	}

	// Repair it with a complete JPEG.
	body := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 0xFF, 0xD9)
	if err := os.WriteFile(filepath.Join(root, rel), body, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err = p.indexPass(ctx, root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 1 {
		t.Errorf("cleared=%d, want 1 — a repaired file must lose its flag", res.Cleared)
	}
	if paths, err := s.suspectPaths(ctx); err != nil || len(paths) != 0 {
		t.Errorf("suspect rows after repair = %v (err %v), want none", paths, err)
	}
}

// A flagged file must be re-read even when its stat is unchanged. This is what
// lets a CORRECTED DETECTOR retire its own false positives: without it the
// stat gate skips the file and the stale verdict is permanent.
func TestFlaggedFilesAreReVerifiedDespiteTheStatGate(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	ctx := context.Background()
	rel := "web/static/covers/2.jpg"
	writeTree(t, root, map[string]string{rel: ""}) // zero-byte, flagged
	classes := []AssetClass{{Slug: "covers", Dir: "web/static/covers", Order: 10}}
	p := &Plugin{st: s}

	if _, err := p.indexPass(ctx, root, classes, 1<<30); err != nil {
		t.Fatal(err)
	}

	// Second pass with the rolling re-hash effectively disabled and the file
	// untouched: only the suspect re-verification can cause a read.
	res, err := p.indexPass(ctx, root, classes, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hashed != 1 {
		t.Errorf("hashed=%d — a flagged file was skipped by the stat gate, so its "+
			"verdict could never be revisited", res.Hashed)
	}
}

// hashed_at must advance only on a genuine re-read. If a carried-forward row
// advanced it, "last verified" would silently become "last seen" and the
// rolling re-hash would be measuring nothing.
func TestHashedAtTracksRealReadsOnly(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	ctx := context.Background()
	rel := "web/static/covers/3.jpg"
	body := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 0xFF, 0xD9)
	if err := os.MkdirAll(filepath.Join(root, "web/static/covers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), body, 0o644); err != nil {
		t.Fatal(err)
	}
	classes := []AssetClass{{Slug: "covers", Dir: "web/static/covers", Order: 10}}
	p := &Plugin{st: s}

	if _, err := p.indexPass(ctx, root, classes, 1<<30); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT hashed_at FROM files WHERE path=$1`, rel).Scan(&first)
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)
	// Carried forward: unchanged stat, re-hash disabled, not suspect.
	if res, err := p.indexPass(ctx, root, classes, 1<<30); err != nil {
		t.Fatal(err)
	} else if res.Skipped != 1 {
		t.Fatalf("expected the file to be carried forward, got skipped=%d", res.Skipped)
	}
	var second time.Time
	if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT hashed_at FROM files WHERE path=$1`, rel).Scan(&second)
	}); err != nil {
		t.Fatal(err)
	}
	if !second.Equal(first) {
		t.Errorf("hashed_at moved on a carried-forward row (%v -> %v); it would then "+
			"mean 'last seen' rather than 'last verified'", first, second)
	}

	// A forced re-hash must advance it.
	if _, err := p.indexPass(ctx, root, classes, 1); err != nil {
		t.Fatal(err)
	}
	var third time.Time
	if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT hashed_at FROM files WHERE path=$1`, rel).Scan(&third)
	}); err != nil {
		t.Fatal(err)
	}
	if !third.After(first) {
		t.Errorf("hashed_at did not advance on a real re-read (%v -> %v)", first, third)
	}
}

// Minimal bodies that satisfy the completeness check, so a fixture is not
// mistaken for damage. Distinct payloads keep the hashes different.
func pngBody(seed string) string  { return "\x89PNG\r\n\x1a\n" + seed + "IEND\xae\x42\x60\x82" }
func jpegBody(seed string) string { return "\xff\xd8\xff\xe0" + seed + "\xff\xd9" }
