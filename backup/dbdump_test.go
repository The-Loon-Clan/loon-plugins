package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// publishedDumps decides what retention DELETES, so its two refusals matter more
// than its ordering: an in-flight dump must not look publishable, and a
// directory this plugin did not create must never be a deletion candidate.
// BackupDir's own prune learned that lesson already — this is the same rule on
// a second directory.
func TestPublishedDumps(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"20260701T010000Z",
		"20260715T010000Z",
		"20260730T010000Z",
		".incoming-20260731T010000Z", // in flight
		"notes",                      // an operator's own directory
		"2026-07-30",                 // not our stamp format
	} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "20260716T010000Z"), nil, 0o644); err != nil {
		t.Fatal(err) // a FILE named like a stamp is not a dump
	}

	got := publishedDumps(dir)
	want := []string{"20260730T010000Z", "20260715T010000Z", "20260701T010000Z"}
	if len(got) != len(want) {
		t.Fatalf("publishedDumps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("publishedDumps = %v, want %v (newest first)", got, want)
		}
	}
}

// The stamp format must sort lexically in time order, because retention trusts
// a string sort to decide what is oldest. A format that sorted wrongly would
// delete the NEWEST dump and keep the stale ones — silently, and only visibly
// during a restore.
func TestDumpStampSortsChronologically(t *testing.T) {
	older := time.Date(2026, 7, 9, 23, 59, 59, 0, time.UTC).Format(dumpStampFormat)
	newer := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC).Format(dumpStampFormat)
	if !(older < newer) {
		t.Fatalf("%q must sort before %q", older, newer)
	}
	if _, err := time.Parse(dumpStampFormat, newer); err != nil {
		t.Fatalf("stamps must round-trip through the parser retention uses: %v", err)
	}
}

// The exclusion list reaches pg_dump's argv from an admin setting. Only plain
// (optionally schema-qualified) identifiers may pass.
func TestPGIdentifierGate(t *testing.T) {
	for _, ok := range []string{"api_requests", "public.job_runs", "_x", "t$1", "search_terms"} {
		if !pgIdentifier.MatchString(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{
		"", "api requests", "--jobs=99", "a;b", "a.b.c", "*", "public.", ".t", "t'x",
	} {
		if pgIdentifier.MatchString(bad) {
			t.Errorf("%q should be rejected before it reaches pg_dump argv", bad)
		}
	}
}

// A bounded stderr buffer must keep the FIRST bytes: the first pg_dump error is
// the cause, everything after it is consequence, and a tail-keeping buffer
// would report the consequence.
func TestBoundedBufferKeepsTheFirstError(t *testing.T) {
	w := &boundedBuffer{max: 16}
	if _, err := w.Write([]byte("connection refused")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("...and 400 more lines")); err != nil {
		t.Fatal(err)
	}
	if got := w.String(); got != "connection refus" {
		t.Errorf("buffer holds %q, want the first 16 bytes", got)
	}
}

// dirTotals reports what the log line claims about a published dump.
func TestDirTotals(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, size := range map[string]int{
		"toc.dat":      100,
		"3001.dat.gz":  2048,
		"sub/more.dat": 12,
	} {
		if err := os.WriteFile(filepath.Join(dir, path), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, bytes := dirTotals(dir)
	if files != 3 || bytes != 2160 {
		t.Errorf("dirTotals = (%d files, %d bytes), want (3, 2160)", files, bytes)
	}
}
