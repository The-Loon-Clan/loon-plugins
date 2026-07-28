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
	writeTree(t, root, map[string]string{
		"web/static/mascots/a.png": "aaaa",
		"web/static/mascots/b.png": "bbbb",
		"web/static/covers/1.jpg":  "cover one",
		"web/static/covers/2.jpg":  "cover two",
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
	writeTree(t, root, map[string]string{rel: "original"})
	classes := []AssetClass{{Slug: "site", Dir: "web/static/site", Order: 10}}
	p := &Plugin{st: s}

	if _, err := p.indexPass(context.Background(), root, classes, 1<<30); err != nil {
		t.Fatal(err)
	}

	// Rewrite with different content AND a different length, then force a
	// distinct mtime so the gate has something to see.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, rel), []byte("replaced content"), 0o644); err != nil {
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
	writeTree(t, root, map[string]string{"web/static/mascots/a.png": "aaaa"})
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
		"web/static/mascots/a.png": "a",
		"web/static/mascots/b.png": "b",
		"web/static/mascots/c.png": "c",
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
