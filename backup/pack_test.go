package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFiles lays out a corpus and returns the rows an index pass would have
// produced for it.
func writeFiles(t *testing.T, root string, bodies map[string]string) []fileRow {
	t.Helper()
	var rows []fileRow
	for rel, body := range bodies {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		rows = append(rows, fileRow{
			Path:   rel,
			Class:  "covers",
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(body)),
		})
	}
	return rows
}

func packBytes(t *testing.T, root string, plan packPlan) ([]byte, []packMember) {
	t.Helper()
	var buf bytes.Buffer
	members, n, err := writePack(&buf, root, plan)
	if err != nil {
		t.Fatalf("writePack: %v", err)
	}
	if n != int64(buf.Len()) {
		t.Fatalf("writePack reported %d bytes but wrote %d", n, buf.Len())
	}
	return buf.Bytes(), members
}

// The format's entire justification is that `unzip` is a working restore path
// with no bespoke tool. Hand-written headers make that a claim rather than a
// fact, so check it against a reader nobody here wrote.
func TestAPackIsARealZipReadableWithoutOurCode(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"covers/1.jpg":       "first cover body",
		"covers/2.jpg":       "second cover body, a little longer",
		"covers/sub/3.jpg":   "third",
		"covers/empty.jpg":   "",
		"covers/binary.webp": string([]byte{0x00, 0xFF, 0x10, 0x00, 0x89}),
	}
	rows := writeFiles(t, root, bodies)

	plans := planPacks("covers", rows, 1<<20, 100)
	if len(plans) != 1 {
		t.Fatalf("expected one pack for a tiny corpus, got %d", len(plans))
	}
	raw, members := packBytes(t, root, plans[0])

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("archive/zip could not open the pack, so neither could unzip: %v", err)
	}
	if len(zr.File) != len(bodies) {
		t.Fatalf("pack has %d members, want %d", len(zr.File), len(bodies))
	}
	for _, f := range zr.File {
		want, ok := bodies[f.Name]
		if !ok {
			t.Errorf("unexpected member %q", f.Name)
			continue
		}
		// STORED, not deflate: deflate output depends on the zlib/Go version, so
		// a stdlib upgrade would change pack bytes and therefore pack IDs.
		if f.Method != zip.Store {
			t.Errorf("%s: method %d, want Store", f.Name, f.Method)
		}
		rc, err := f.Open()
		if err != nil {
			t.Errorf("%s: open: %v", f.Name, err)
			continue
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			// A CRC mismatch surfaces here, which is the check that the header
			// values are right rather than merely well-formed.
			t.Errorf("%s: read: %v", f.Name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: content round-tripped wrong", f.Name)
		}
	}

	// Members must be reported with the same names, or the manifest and the
	// archive disagree about what was backed up.
	if len(members) != len(bodies) {
		t.Fatalf("writePack returned %d members, want %d", len(members), len(bodies))
	}
}

// A non-ASCII filename must survive a restore. Without general purpose bit 11 a
// reader falls back to CP437 and `unzip` writes the name as mojibake — a
// different file from the one that was backed up, silently.
//
// This is checked by asserting the FLAG rather than by reading the name back,
// because the readers available here all assume UTF-8 and so round-trip the
// name whether or not the flag is set. The bug was found by running a real
// `unzip` into a directory and diffing; this test is the cheap standing guard
// that the fix stays in place.
func TestFilenamesAreMarkedUTF8(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{
		"covers/ノベル.jpg":         "japanese",
		"covers/Ärger.jpg":       "umlaut",
		"covers/plain-ascii.jpg": "ascii",
	})
	plans := planPacks("covers", rows, 1<<20, 100)
	raw, _ := packBytes(t, root, plans[0])

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Flags&0x0800 == 0 {
			t.Errorf("%s: the UTF-8 name flag is not set, so unzip would decode the name as CP437", f.Name)
		}
		if f.NonUTF8 {
			t.Errorf("%s: reported as a non-UTF-8 name", f.Name)
		}
	}
}

// The pack ID is the hash of these bytes. Anything that varies run-to-run over
// unchanged content — a timestamp, a map iteration, a stdlib compressor — turns
// "nothing changed" into a full re-transfer of 131 GB.
func TestPackBytesAreIdenticalAcrossRuns(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{}
	for i := 0; i < 40; i++ {
		bodies[fmt.Sprintf("covers/%d.jpg", i)] = fmt.Sprintf("body of cover %d", i)
	}
	rows := writeFiles(t, root, bodies)

	plans := planPacks("covers", rows, 1<<20, 1000)
	first, _ := packBytes(t, root, plans[0])

	// Re-plan from a DIFFERENT input order. Rows arrive from a query, and a
	// query without a total ORDER BY may return them in any order.
	shuffled := make([]fileRow, len(rows))
	for i := range rows {
		shuffled[i] = rows[len(rows)-1-i]
	}
	replans := planPacks("covers", shuffled, 1<<20, 1000)
	second, _ := packBytes(t, root, replans[0])
	if !bytes.Equal(first, second) {
		t.Error("pack bytes depend on the order rows arrived in; every pack ID would churn at random")
	}

	// Touching a file without changing its content must not change the pack.
	// Real mtimes in the headers would re-transfer data that is identical — the
	// reason the DOS timestamp is pinned to the ZIP epoch.
	touch := filepath.Join(root, "covers", "7.jpg")
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(touch, future, future); err != nil {
		t.Fatal(err)
	}
	third, _ := packBytes(t, root, plans[0])
	if !bytes.Equal(first, third) {
		t.Error("touching a file changed the pack bytes; an unchanged corpus would re-transfer")
	}
}

// Restoring one file must be a Range GET, not a 64 MiB download. That is the
// answer to the usual objection to packing, so the offset has to be exact.
func TestMemberOffsetPointsAtTheFileData(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"covers/a.jpg": "aaaaaaaaaaaaaaaa",
		"covers/b.jpg": "bbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"covers/c.jpg": "cc",
	}
	rows := writeFiles(t, root, bodies)
	plans := planPacks("covers", rows, 1<<20, 100)
	raw, members := packBytes(t, root, plans[0])

	for _, m := range members {
		if m.Offset <= 0 {
			t.Errorf("%s: offset %d is not inside the pack", m.Path, m.Offset)
			continue
		}
		if m.Offset+m.Size > int64(len(raw)) {
			t.Errorf("%s: offset %d + size %d runs past the pack end %d", m.Path, m.Offset, m.Size, len(raw))
			continue
		}
		// Exactly what a Range GET would return.
		got := raw[m.Offset : m.Offset+m.Size]
		if string(got) != bodies[m.Path] {
			t.Errorf("%s: a Range read at the recorded offset returned %q, want %q",
				m.Path, got, bodies[m.Path])
		}
		// And the hash the manifest promises must be the hash of those bytes,
		// or a restore cannot verify itself.
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != m.SHA256 {
			t.Errorf("%s: bytes at the offset do not match the recorded sha256", m.Path)
		}
	}
}

// A file edited in place leaves several rows sharing a path, because files is
// keyed (path, sha256). Two ZIP members with one name make `unzip -o` write
// whichever lands last, and since the sort key is the path alone their order is
// unspecified — so the pack would restore stale content, non-deterministically.
func TestDuplicatePathsCollapseToTheNewest(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{"covers/a.jpg": "current content"})
	current := rows[0]

	stale := current
	stale.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	stale.Size = 4

	// Caller contract: newest first, as DISTINCT ON ... ORDER BY last_gen DESC
	// delivers them.
	plans := planPacks("covers", []fileRow{current, stale}, 1<<20, 100)
	var got []packMember
	for _, p := range plans {
		got = append(got, p.Members...)
	}
	if len(got) != 1 {
		t.Fatalf("a path appearing twice produced %d members; a pack must name each file once", len(got))
	}
	if got[0].SHA256 != current.SHA256 {
		t.Error("the stale revision won; a restore would write old content over new")
	}

	// It must also survive the write, since a size mismatch between the plan and
	// the file on disk is caught there.
	raw, _ := packBytes(t, root, plans[0])
	if _, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatalf("pack unreadable: %v", err)
	}
}

// Packing decides the unit of re-fetch and of retention, so the grouping rules
// have to hold rather than approximately hold.
func TestPackPlanningRespectsItsLimits(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{}
	for i := 0; i < 20; i++ {
		bodies[fmt.Sprintf("covers/%02d.jpg", i)] = string(make([]byte, 100))
	}
	rows := writeFiles(t, root, bodies)

	// 250-byte target over 100-byte files: 2 per pack.
	plans := planPacks("covers", rows, 250, 100)
	if len(plans) != 10 {
		t.Errorf("got %d packs, want 10 (20 files of 100B into a 250B target)", len(plans))
	}
	for _, p := range plans {
		if p.Bytes > 250 {
			t.Errorf("a pack holds %d bytes, over the 250 target", p.Bytes)
		}
		if p.Class != "covers" {
			t.Errorf("pack class %q; packs must stay class-pure so one tier can be retained without the others", p.Class)
		}
	}

	// The member cap keeps a pack inside the classic central-directory limit.
	capped := planPacks("covers", rows, 1<<30, 3)
	for _, p := range capped {
		if len(p.Members) > 3 {
			t.Errorf("a pack holds %d members, over the cap of 3", len(p.Members))
		}
	}
	if len(capped) != 7 {
		t.Errorf("got %d packs under a 3-member cap, want 7", len(capped))
	}

	// Files are never split, so the target is a fill line rather than a hard cap.
	// The invariant that must hold: a pack may exceed the target ONLY when it
	// holds a single file that exceeds it alone. Anything else means the grouping
	// rule let two files past the line, and pack sizes stop being bounded.
	//
	// The oversized file is named to sort into the MIDDLE of the corpus. Putting
	// it last would let a broken rule pass by luck, since the final flush
	// isolates whatever is left over.
	big := writeFiles(t, root, map[string]string{"covers/07-huge.jpg": string(make([]byte, 900))})
	mixed := planPacks("covers", append(append([]fileRow{}, rows...), big...), 250, 100)
	var alone bool
	for _, p := range mixed {
		if p.Bytes > 250 && len(p.Members) != 1 {
			t.Errorf("a pack of %d bytes holds %d members; only a single oversized file may pass the target",
				p.Bytes, len(p.Members))
		}
		for _, m := range p.Members {
			if m.Path == "covers/07-huge.jpg" {
				alone = len(p.Members) == 1
			}
		}
	}
	if !alone {
		t.Error("an oversized file was packed alongside others instead of getting its own pack")
	}

	// Every file must land in exactly one pack. A planner that drops a file
	// backs up nothing and says nothing.
	seen := map[string]int{}
	for _, p := range mixed {
		for _, m := range p.Members {
			seen[m.Path]++
		}
	}
	if len(seen) != len(rows)+1 {
		t.Errorf("%d of %d files made it into a pack", len(seen), len(rows)+1)
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s appears in %d packs", p, n)
		}
	}
	if len(planPacks("covers", nil, 250, 100)) != 0 {
		t.Error("an empty class produced a pack")
	}
}

// The classic ZIP header fields are 32-bit, and Go truncates a conversion
// silently. An archive built past the limit opens, lists plausible sizes, and
// restores garbage — refusing is the only safe answer, since zip64 would cost
// the break-glass compatibility this format exists for.
func TestOversizeIsRefusedRatherThanTruncated(t *testing.T) {
	root := t.TempDir()

	// A LONE oversized member is no longer refused — it is served raw, with no
	// container (see writeRawPack). That path is exercised by
	// TestWriteRawPackEmitsExactlyTheFile; what still has to be refused is an
	// oversized member that raw cannot represent, i.e. one sharing a pack.
	//
	// Bytes deliberately left at zero, so ONLY the per-member check can refuse
	// this. A plan is data; a caller that builds one without totalling it must
	// still not be able to produce a wrapped header.
	member := packPlan{
		Class: "screenshots",
		Members: []packMember{
			{Path: "screenshots/huge.mkv", Size: 1 << 33},
			{Path: "screenshots/small.jpg", Size: 1 << 10},
		},
	}
	_, _, err := writePack(io.Discard, root, member)
	// Assert on WHICH error. These fixtures are 8 GiB of nothing and were never
	// created, so os.Open fails too — a bare err != nil check passes with the
	// guard deleted, which is how the first version of this test managed to
	// assert nothing at all.
	if err == nil || !strings.Contains(err.Error(), "zip64") {
		t.Errorf("an 8 GiB member sharing a pack must be refused before any I/O; got %v", err)
	}

	// And the pack total is checked independently of any single member, since
	// the central-directory offset is 32-bit too.
	whole := packPlan{
		Class:   "screenshots",
		Members: []packMember{{Path: "screenshots/a.mkv", Size: 3 << 30}, {Path: "screenshots/b.mkv", Size: 3 << 30}},
		Bytes:   6 << 30,
	}
	_, _, err = writePack(io.Discard, root, whole)
	if err == nil || !strings.Contains(err.Error(), "zip64") {
		t.Errorf("a 6 GiB pack of individually-legal members must be refused; got %v", err)
	}

	// The third guard — the authoritative cdOffset check after the member loop —
	// is defence-in-depth for a plan whose Bytes understates reality, and cannot
	// be reached without writing 4 GiB. It is deliberately left uncovered rather
	// than covered by a test that pretends.
}

// A file changing under the packer is normal — a re-scrape, a regenerated
// thumbnail — and the pack must fail rather than record a hash for bytes it did
// not write.
func TestAFileChangingUnderThePackerIsAnError(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{"covers/a.jpg": "original body"})
	plans := planPacks("covers", rows, 1<<20, 100)

	if err := os.WriteFile(filepath.Join(root, "covers", "a.jpg"), []byte("a longer replacement body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writePack(io.Discard, root, plans[0]); err == nil {
		t.Error("a file that changed size between index and pack was written anyway, with the indexed size in its header")
	}

	// A missing file must also fail loudly rather than yield a short pack.
	if err := os.Remove(filepath.Join(root, "covers", "a.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writePack(io.Discard, root, plans[0]); err == nil {
		t.Error("a deleted file produced a pack instead of an error")
	}
}

// A member over 4 GiB cannot be represented in classic ZIP, and the database
// class produces exactly that — pg_dump -Fd writes one file per table and nzbs
// is a 20 GB member. Prod hit this on the first dump that reached the pipeline:
// 7 of 9 packs transferred and the two biggest 404'd, which was 23 of the
// 24.3 GB. The raw path is how that member moves at all.
func TestOversizedMemberIsServedRaw(t *testing.T) {
	big := packPlan{Class: "db-dumps", Members: []packMember{
		{Path: "db-dumps/x/6175.dat.gz", SHA256: "abc", Size: 20 << 30},
	}}
	if !packIsRaw(big) {
		t.Fatal("a lone 20 GiB member must be raw — it cannot be zipped")
	}
	// The wire size is the member itself: no local header, no central
	// directory, no EOCD. A client using the ZIP figure would wait forever for
	// bytes that are never sent.
	if got := packWireSize(big); got != 20<<30 {
		t.Errorf("raw wire size = %d, want exactly the member's %d", got, int64(20<<30))
	}
}

// Everything that fits must keep its container, or the 2,000-odd image packs
// would all change shape for a problem they do not have.
func TestOrdinaryPacksAreNotRaw(t *testing.T) {
	for _, p := range []packPlan{
		{Class: "covers", Members: []packMember{{Path: "a.jpg", Size: 4 << 20}}},
		// Exactly at the limit is still representable.
		{Class: "covers", Members: []packMember{{Path: "a.bin", Size: math.MaxUint32}}},
		// Two members, one large: planPacks never builds this, and if it ever
		// did, raw could not represent it — writePack refuses instead.
		{Class: "covers", Members: []packMember{
			{Path: "a.bin", Size: 5 << 30}, {Path: "b.jpg", Size: 1 << 10},
		}},
	} {
		if packIsRaw(p) {
			t.Errorf("%+v was classed raw", p.Members)
		}
	}
}

// The invariant the raw path leans on: planPacks never groups a file larger
// than the fill target with anything else, so an oversized member is always
// alone and therefore always representable as a raw pack.
func TestAnOversizedFileIsAlwaysAloneInItsPack(t *testing.T) {
	rows := []fileRow{
		{Path: "db-dumps/d/0001.dat.gz", SHA256: "a", Size: 1 << 10},
		{Path: "db-dumps/d/6175.dat.gz", SHA256: "b", Size: 20 << 30},
		{Path: "db-dumps/d/6288.dat.gz", SHA256: "c", Size: 5 << 30},
		{Path: "db-dumps/d/9999.dat.gz", SHA256: "d", Size: 2 << 10},
	}
	for _, p := range planPacks("db-dumps", rows, packTargetBytes, packMaxMembers) {
		for _, m := range p.Members {
			if m.Size > math.MaxUint32 && len(p.Members) != 1 {
				t.Errorf("oversized member %s shares a pack with %d others", m.Path, len(p.Members)-1)
			}
		}
		if packIsRaw(p) && packWireSize(p) != p.Members[0].Size {
			t.Errorf("raw pack %s wire size disagrees with its member", p.Members[0].Path)
		}
	}
}

// writeRawPack must emit the member's bytes and nothing else, and must refuse
// a file whose length no longer matches what the generation recorded — every
// client was already told to expect that number.
func TestWriteRawPackEmitsExactlyTheFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "db-dumps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("pretend this is twenty gigabytes of table data")
	if err := os.WriteFile(filepath.Join(dir, "6175.dat.gz"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := packPlan{Class: "db-dumps", Members: []packMember{
		{Path: "db-dumps/6175.dat.gz", SHA256: "x", Size: int64(len(body))},
	}}

	var buf bytes.Buffer
	cw := &countingWriter{w: &buf}
	members, n, err := writeRawPack(cw, root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Errorf("raw pack wrote %q, want the file verbatim", buf.String())
	}
	if n != int64(len(body)) || members[0].Offset != 0 {
		t.Errorf("n=%d offset=%d, want %d and 0", n, members[0].Offset, len(body))
	}

	// A file that changed under us must not be served as if it matched.
	plan.Members[0].Size = int64(len(body)) + 1
	if _, _, err := writeRawPack(&countingWriter{w: &bytes.Buffer{}}, root, plan); err == nil {
		t.Error("served a member whose length disagreed with the generation")
	}
}

// Retiring the archive job removed its retention with it, orphaning every
// folder it had ever written — 54 GB on prod — with nothing left to clean
// them. The sweep is the fix, and its narrowness is the safety property: it
// must reclaim its own leftovers and nothing else.
func TestSweepRemovesOnlyTheRetiredArchivesOwnFolders(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, body string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, name, "f.bin"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("2026-07-04_215227", "eight bytes")   // the archive's format — goes
	mk("2026-06-27_211316", "more bytes!")   // ditto
	mk("db-dumps", "the database")           // THE BACKUP. Must survive.
	mk("2026-08-01T102531Z", "a dump stamp") // dump format, not archive format
	mk("notes", "somebody's own folder")
	if err := os.WriteFile(filepath.Join(dir, "2026-07-04_215227.txt"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := sweepRetiredArchives(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed %d, want exactly the 2 archive folders", removed)
	}
	if freed <= 0 {
		t.Errorf("freed %d bytes, want the reclaimed total reported", freed)
	}
	for _, keep := range []string{"db-dumps", "2026-08-01T102531Z", "notes", "2026-07-04_215227.txt"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s was removed — the sweep is too broad", keep)
		}
	}
	for _, gone := range []string{"2026-07-04_215227", "2026-06-27_211316"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived", gone)
		}
	}
	// Idempotent, and silent once there is nothing left — this runs on every
	// dump pass forever.
	if n, _, err := sweepRetiredArchives(dir); err != nil || n != 0 {
		t.Errorf("second sweep removed %d (err=%v), want 0", n, err)
	}
	// A directory that does not exist is not an error: an install that never
	// ran the archive job has no such folder.
	if n, _, err := sweepRetiredArchives(filepath.Join(dir, "nope")); err != nil || n != 0 {
		t.Errorf("missing dir: n=%d err=%v", n, err)
	}
}
