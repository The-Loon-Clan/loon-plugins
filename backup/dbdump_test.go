package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/schedule"
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

// The pre-flight must estimate what a dump ACTUALLY costs, not what the
// database weighs.
//
// Production ran into this the day the dump went daily: 55 GB database, 34.7 GB
// dump (eleven tables excluded), and three consecutive refusals reading
// "insufficient disk for a database dump" on nights when the dump would have
// finished. The guard was not wrong about arithmetic -- it was comparing
// against a number that exclusions had made obsolete, and the operator reads
// that message as "buy a bigger disk".
func TestDumpSpaceNeededMeasuresTheLastDump(t *testing.T) {
	dir := t.TempDir()
	const dbSize = 55 << 30

	// Nothing to measure yet: the database size is the honest fallback, and a
	// first run has no other evidence.
	need, basis := dumpSpaceNeeded(dir, dbSize)
	if need != dbSize {
		t.Errorf("with no dumps: need = %d, want the database size %d", need, dbSize)
	}
	if !strings.Contains(basis, "database size") {
		t.Errorf("basis should say where the number came from, got %q", basis)
	}

	// One published dump of 100 MB -> 115 MB, and NOT the 55 GB database.
	older := filepath.Join(dir, "20260804T010101Z")
	if err := os.MkdirAll(older, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(older, "1.dat.gz"), 100<<20)

	need, basis = dumpSpaceNeeded(dir, dbSize)
	if want := int64(100<<20) + int64(100<<20)/100*15; need != want {
		t.Errorf("need = %d, want %d (last dump + 15%%)", need, want)
	}
	if need >= dbSize {
		t.Errorf("still using the database size (%d); the whole point is not to", need)
	}
	if !strings.Contains(basis, "last dump") {
		t.Errorf("basis = %q, want it to name the measured dump", basis)
	}

	// A NEWER dump must win, or the estimate drifts as exclusions change.
	newer := filepath.Join(dir, "20260805T010101Z")
	if err := os.MkdirAll(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(newer, "1.dat.gz"), 200<<20)
	need, _ = dumpSpaceNeeded(dir, dbSize)
	if want := int64(200<<20) + int64(200<<20)/100*15; need != want {
		t.Errorf("need = %d, want %d -- the NEWEST dump must be measured", need, want)
	}

	// An in-progress dump is not evidence: pg_dump is still writing it, so its
	// size is whatever it happens to have reached.
	incoming := filepath.Join(dir, dumpIncoming+"20260806T010101Z")
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(incoming, "partial.dat.gz"), 5<<20)
	need, _ = dumpSpaceNeeded(dir, dbSize)
	if want := int64(200<<20) + int64(200<<20)/100*15; need != want {
		t.Errorf("need = %d, want %d -- an .incoming dump must be ignored", need, want)
	}

	// The margin has to point the safe way: a dump that grew must not squeeze in.
	if need <= 200<<20 {
		t.Errorf("need = %d, must exceed the measured %d so growth has room",
			need, 200<<20)
	}
}

func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Abandoned partial dumps were never deleted by anything.
//
// The RemoveAll in runDBDump only ever targeted the CURRENT stamp -- a
// directory that by definition does not already exist -- and publishedDumps
// skips the .incoming- prefix, so retention never saw them either. Production
// was still carrying one from 2026-08-04, and the space it held is why three
// consecutive dumps refused for want of disk.
//
// A dump takes ~58 minutes and dies with any container recreate, so this
// accumulates once per deploy that lands in the dump window -- and deploys just
// became a single command.
func TestClearStaleIncomingReclaimsAbandonedPartials(t *testing.T) {
	dir := t.TempDir()

	// Two abandoned partials, one of them the real name seen on production.
	for _, name := range []string{".incoming-20260804T020809Z", ".incoming-20260805T010101Z"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSized(t, filepath.Join(p, "partial.dat.gz"), 4<<20)
	}
	// A PUBLISHED dump, which must survive -- it is the backup.
	published := filepath.Join(dir, "20260805T072837Z")
	if err := os.MkdirAll(published, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(published, "1.dat.gz"), 1<<20)
	// Something this plugin did not create. Never a deletion candidate -- the
	// same rule publishedDumps and retention already follow.
	foreign := filepath.Join(dir, ".incoming-not-a-stamp")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	n, freed := clearStaleIncoming(dir)
	if n != 2 {
		t.Errorf("cleared %d, want 2", n)
	}
	if freed < 8<<20 {
		t.Errorf("reclaimed %d bytes, want at least %d", freed, 8<<20)
	}
	if _, err := os.Stat(published); err != nil {
		t.Error("the PUBLISHED dump was deleted -- that is the backup")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("deleted a directory whose name is not our stamp format")
	}
	for _, name := range []string{".incoming-20260804T020809Z", ".incoming-20260805T010101Z"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", name)
		}
	}

	// Idempotent: a second sweep finds nothing and reports nothing.
	if n, _ := clearStaleIncoming(dir); n != 0 {
		t.Errorf("second sweep cleared %d, want 0", n)
	}
}

// The sweep must run BEFORE the disk check, or the check refuses on space the
// sweep is about to return -- which is exactly what production did.
func TestSweepPrecedesTheDiskCheck(t *testing.T) {
	src, err := os.ReadFile("dbdump.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	sweep := strings.Index(s, "clearStaleIncoming(deps.DBDumpDir)")
	check := strings.Index(s, "deps.FreeDisk(ctx)")
	if sweep < 0 || check < 0 {
		t.Fatal("could not locate both the sweep and the disk check")
	}
	if sweep > check {
		t.Error("the disk check runs before the sweep, so it will refuse on space " +
			"the sweep would have reclaimed")
	}
}

// ─── retention ordering: the outage fix ────────────────────────────────────

// mkDumps creates n published dump directories with ascending stamps, oldest
// first, and returns their names.
func mkDumps(t *testing.T, dir string, n int) []string {
	t.Helper()
	var names []string
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		name := base.AddDate(0, 0, i).Format(dumpStampFormat)
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "toc.dat"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestPruneDumpsToKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	names := mkDumps(t, dir, 5)

	SetDeps(Deps{DBDumpDir: dir})
	p := &Plugin{dumpJob: schedule.RegisterJob("test-prune-keeps-newest", "test")}

	if got := p.pruneDumpsTo(2); got != 3 {
		t.Fatalf("pruned %d dump(s), want 3", got)
	}
	left := publishedDumps(dir)
	if len(left) != 2 {
		t.Fatalf("%d dump(s) left, want 2: %v", len(left), left)
	}
	// publishedDumps is newest-first, and the newest two are the LAST two created.
	if left[0] != names[4] || left[1] != names[3] {
		t.Errorf("kept %v, want the newest two %v", left, []string{names[4], names[3]})
	}
}

// A keep of zero or less must never mean "delete every dump we have". The
// pre-flight path derives its argument by subtracting one, so this is the guard
// on keep=1 turning into "empty the directory, then start a dump that may fail".
func TestPruneDumpsToNeverEmptiesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	mkDumps(t, dir, 3)

	SetDeps(Deps{DBDumpDir: dir})
	p := &Plugin{dumpJob: schedule.RegisterJob("test-prune-refuses-zero", "test")}

	for _, keep := range []int{0, -1} {
		if got := p.pruneDumpsTo(keep); got != 0 {
			t.Errorf("pruneDumpsTo(%d) pruned %d, want 0", keep, got)
		}
	}
	if left := publishedDumps(dir); len(left) != 3 {
		t.Errorf("%d dump(s) left, want all 3", len(left))
	}
}

// The ordering bug that cost an outage: retention ran only after a SUCCESSFUL
// dump, while the disk pre-flight could refuse and return before ever reaching
// it. Too full to dump therefore meant too full to prune, permanently. This is
// the sibling of TestSweepPrecedesTheDiskCheck — same file, same mistake, third
// instance — so it is pinned the same way.
func TestPrunePrecedesTheDiskCheck(t *testing.T) {
	src, err := os.ReadFile("dbdump.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	prune := strings.Index(s, "p.pruneDumpsTo(room)")
	check := strings.Index(s, "deps.FreeDisk(ctx)")
	if prune < 0 || check < 0 {
		t.Fatal("could not locate both the pre-flight prune and the disk check")
	}
	if prune > check {
		t.Error("the disk check runs before retention, so a full disk means no dump " +
			"AND no prune — the ratchet that filled 257 GB and took the site offline")
	}
}
