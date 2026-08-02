package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The pack ID is what makes "only fetch what changed" work, so it must depend
// on the member CONTENTS and nothing else.
func TestPackIDTracksContentNotPosition(t *testing.T) {
	member := func(path, sha string, size int64) packMember {
		return packMember{Path: path, SHA256: sha, Size: size}
	}
	base := packPlan{Class: "covers", Members: []packMember{
		member("covers/1.jpg", "aaa", 10),
		member("covers/2.jpg", "bbb", 20),
	}}

	if packID(base) != packID(base) {
		t.Fatal("packID is not deterministic")
	}

	// Offsets are assigned at write time and say nothing about content; a pack
	// whose files are untouched must keep its ID or every resume re-fetches.
	withOffsets := packPlan{Class: "covers", Members: []packMember{
		member("covers/1.jpg", "aaa", 10),
		member("covers/2.jpg", "bbb", 20),
	}}
	withOffsets.Members[0].Offset = 999
	withOffsets.Members[1].CRC32 = 12345
	if packID(withOffsets) != packID(base) {
		t.Error("pack ID changed when only write-time fields differed; unchanged data would be re-transferred")
	}

	// An edited file must change the ID, or a puller keeps stale bytes forever.
	edited := packPlan{Class: "covers", Members: []packMember{
		member("covers/1.jpg", "aaa", 10),
		member("covers/2.jpg", "CHANGED", 20),
	}}
	if packID(edited) == packID(base) {
		t.Error("pack ID survived a content change — the new bytes would never be fetched")
	}

	// So must a resize, a rename, and a different class.
	for name, p := range map[string]packPlan{
		"resized": {Class: "covers", Members: []packMember{member("covers/1.jpg", "aaa", 10), member("covers/2.jpg", "bbb", 21)}},
		"renamed": {Class: "covers", Members: []packMember{member("covers/1.jpg", "aaa", 10), member("covers/9.jpg", "bbb", 20)}},
		"other":   {Class: "banners", Members: []packMember{member("covers/1.jpg", "aaa", 10), member("covers/2.jpg", "bbb", 20)}},
		"dropped": {Class: "covers", Members: []packMember{member("covers/1.jpg", "aaa", 10)}},
	} {
		if packID(p) == packID(base) {
			t.Errorf("%s: pack ID collided with the original", name)
		}
	}

	// Order is part of the identity: the members are written in this order and
	// the bytes differ if it changes, so the ID must too.
	reordered := packPlan{Class: "covers", Members: []packMember{
		member("covers/2.jpg", "bbb", 20),
		member("covers/1.jpg", "aaa", 10),
	}}
	if packID(reordered) == packID(base) {
		t.Error("pack ID ignored member order, but the pack bytes depend on it")
	}
}

// Resume has to land on a byte boundary that means the same thing on the second
// attempt as on the first, or a resumed transfer silently corrupts the pack.
func TestSkipWriterResumesExactly(t *testing.T) {
	full := []byte("0123456789abcdefghijklmnopqrstuvwxyz")

	for _, skip := range []int64{0, 1, 10, 35, 36} {
		var got bytes.Buffer
		w := &skipWriter{w: &got, remaining: skip}
		// Written in awkward chunks so the skip boundary lands mid-write, which
		// is the case that actually breaks naive implementations.
		for _, chunk := range [][]byte{full[:7], full[7:8], full[8:30], full[30:]} {
			n, err := w.Write(chunk)
			if err != nil {
				t.Fatalf("skip=%d: %v", skip, err)
			}
			// Must report the full length: the caller is io.Copy, and a short
			// count without an error is an io.ErrShortWrite.
			if n != len(chunk) {
				t.Errorf("skip=%d: Write reported %d for a %d-byte chunk", skip, n, len(chunk))
			}
		}
		if want := string(full[skip:]); got.String() != want {
			t.Errorf("skip=%d: got %q, want %q", skip, got.String(), want)
		}
	}

	// Skipping past the end yields nothing rather than misbehaving.
	var got bytes.Buffer
	w := &skipWriter{w: &got, remaining: 999}
	if _, err := w.Write(full); err != nil {
		t.Fatal(err)
	}
	if got.Len() != 0 {
		t.Errorf("skipping past the end emitted %d bytes", got.Len())
	}
}

// A resumed fetch must produce exactly the tail of the same pack — this is the
// property that lets a dropped transfer continue instead of starting over.
func TestResumedPackMatchesTheTailOfAFullPack(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"covers/a.jpg": "first body here",
		"covers/b.jpg": "second body, longer than the first",
		"covers/c.jpg": "third",
	}
	rows := writeFiles(t, root, bodies)
	plan := planPacks("covers", rows, 1<<20, 100)[0]

	whole, _ := packBytes(t, root, plan)

	for _, skip := range []int64{1, 17, int64(len(whole)) - 5} {
		var tail bytes.Buffer
		if _, _, err := writePack(&skipWriter{w: &tail, remaining: skip}, root, plan); err != nil {
			t.Fatalf("skip=%d: %v", skip, err)
		}
		if !bytes.Equal(tail.Bytes(), whole[skip:]) {
			t.Errorf("skip=%d: resumed bytes do not match the tail of the full pack "+
				"(got %d bytes, want %d)", skip, tail.Len(), len(whole)-int(skip))
		}
	}
}

// Nothing may be staged on disk to serve a pack — that is the entire reason
// this path exists, since the archive job's local staging needs more free space
// than the box has and so has never run.
func TestStreamingWritesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{
		"covers/a.jpg": "aaaa",
		"covers/b.jpg": "bbbb",
	})
	plan := planPacks("covers", rows, 1<<20, 100)[0]

	before := countFiles(t, root)
	if _, _, err := writePack(io.Discard, root, plan); err != nil {
		t.Fatal(err)
	}
	if after := countFiles(t, root); after != before {
		t.Errorf("serving a pack changed the file count under root from %d to %d — "+
			"streaming must not stage anything", before, after)
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	if err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// PackInfo.Bytes must be what the pack STREAMS, not what its members contain.
//
// It used to be the member-size sum, which is always short by the ZIP structure
// — 30 bytes plus the name per local header, 46 plus the name per central entry,
// 22 for the end-of-central-directory record. A client using it as
// Content-Length, or as the mark for "done", stops before the EOCD on every
// single pack and stores an archive no reader will open.
func TestAdvertisedSizeIsTheWireSizeNotTheContentSize(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{
		"covers/a.jpg":      "aaaa",
		"covers/bb.jpg":     "bbbbbbbb",
		"covers/deep/c.jpg": "c",
	})
	plan := planPacks("covers", rows, 1<<20, 100)[0]

	actual, _ := packBytes(t, root, plan)
	if got := packWireSize(plan); got != int64(len(actual)) {
		t.Errorf("packWireSize = %d but writePack emitted %d — a client sizing the transfer from "+
			"this would truncate every pack", got, len(actual))
	}
	// And the old value must be visibly different, or the test proves nothing.
	if plan.Bytes >= int64(len(actual)) {
		t.Errorf("content sum %d is not less than the wire size %d; this fixture cannot "+
			"distinguish the two", plan.Bytes, len(actual))
	}
}

// A same-size in-place rewrite must not be served under the old pack ID.
//
// The ID encodes each member's recorded sha256, so writing different bytes
// under it hands a puller stale content it can never detect: the CRC in the
// header is recomputed from the new bytes and is perfectly self-consistent.
// Size alone cannot catch this, which is precisely why statKey tracks more.
func TestSameSizeRewriteIsRefusedNotServed(t *testing.T) {
	root := t.TempDir()
	rows := writeFiles(t, root, map[string]string{"covers/a.jpg": "original"})
	plan := planPacks("covers", rows, 1<<20, 100)[0]

	// Same length, different content — the case a size check waves through.
	if err := os.WriteFile(filepath.Join(root, "covers", "a.jpg"), []byte("REPLACED"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := writePack(io.Discard, root, plan)
	if err == nil {
		t.Fatal("a same-size content change was packed under the old ID — a puller would keep " +
			"stale bytes forever with nothing able to notice")
	}
	if !strings.Contains(err.Error(), "changed content") {
		t.Errorf("got %v, want a content-change error", err)
	}
}

// Member paths must never escape the asset root. Not reachable from today's
// data — paths come from filepath.Rel during the walk — but serving packs over
// HTTP turns any bad row into an arbitrary file read, and "the data happens to
// be clean" is not a security property.
func TestMemberPathsCannotEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"../../etc/passwd",
		"covers/../../../etc/passwd",
		"/etc/passwd",
		"",
		"..",
	} {
		if _, err := memberPath(root, bad); err == nil {
			t.Errorf("memberPath accepted %q — that is an arbitrary file read over the pack endpoint", bad)
		}
	}

	// A non-empty root is no defence on its own, so prove the join is checked
	// rather than merely prefixed.
	if _, err := memberPath("/srv/assets", "../../etc/passwd"); err == nil {
		t.Error("a traversal escaped a non-empty root")
	}

	// Ordinary paths still resolve.
	for _, ok := range []string{"covers/1.jpg", "covers/deep/sub/2.jpg", "a.png"} {
		if _, err := memberPath(root, ok); err != nil {
			t.Errorf("memberPath rejected the legitimate path %q: %v", ok, err)
		}
	}

	// And writePack refuses rather than reading the file.
	bad := packPlan{Class: "covers", Members: []packMember{{Path: "../secret", Size: 1}}}
	if _, _, err := writePack(io.Discard, root, bad); err == nil {
		t.Error("writePack followed a traversing member path")
	}
}

// The manifest's ORDER is a safety property, not a cosmetic one: a first pull
// of this corpus runs for hours, and whatever it has when it stops is the
// backup. So the irreplaceable classes must be served first.
//
// This sort used to key on the class NAME, which threw away the priority the
// plan loop had just built — "site" sorts after "screenshots", so 14 files of
// user-uploaded artwork with no upstream anywhere were served LAST, behind
// 1,901 packs and 126 GB of regenerable frames. Nothing failed; the array
// simply did not contain the art. Found on the first real pull, 2026-07-30.
func TestManifestServesIrreplaceableClassesFirst(t *testing.T) {
	// The host's order: irreplaceable (10), database (20), re-fetchable (50),
	// regenerable (90) — collapsed to rank by orderedClasses.
	rank := map[string]int{
		"site": 0, "avatars": 1, "posters": 2,
		"db-dumps":   3,
		"covers":     4,
		"screenshot": 5,
	}
	packs := []PackInfo{
		{ID: "aaa", Class: "screenshot"},
		{ID: "bbb", Class: "covers"},
		{ID: "ccc", Class: "site"},
		{ID: "ddd", Class: "db-dumps"},
		{ID: "eee", Class: "screenshot"},
		{ID: "fff", Class: "avatars"},
	}
	sortPacksByPriority(packs, rank)

	got := make([]string, len(packs))
	for i, p := range packs {
		got[i] = p.Class
	}
	want := []string{"site", "avatars", "db-dumps", "covers", "screenshot", "screenshot"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pack order = %v, want %v — an interrupted pull keeps what came first", got, want)
		}
	}
	// The name is NOT the key: "site" must beat "screenshot" despite sorting
	// after it alphabetically. That is the exact inversion this pins.
	if packs[0].Class != "site" {
		t.Error("alphabetical ordering is back: the irreplaceable class is not first")
	}
	// Within a class the id still orders, so a generation's manifest is stable
	// and a resumed pull sees the same sequence it planned against.
	if packs[4].ID != "aaa" || packs[5].ID != "eee" {
		t.Errorf("within-class order is not by id: %v, %v", packs[4].ID, packs[5].ID)
	}
}

// A class the host never declared (a stale row, a renamed slug) ranks 0 in a
// map lookup and would jump the queue ahead of the real irreplaceable classes.
// It must not outrank them silently.
func TestUnknownClassDoesNotOutrankDeclaredOnes(t *testing.T) {
	rank := map[string]int{"site": 0, "screenshots": 1}
	packs := []PackInfo{{ID: "a", Class: "screenshots"}, {ID: "b", Class: "site"}, {ID: "c", Class: "ghost"}}
	sortPacksByPriority(packs, rank)
	if packs[0].Class != "site" {
		t.Errorf("order = %v…, want the declared irreplaceable class first", packs[0].Class)
	}
	if packs[2].Class != "ghost" {
		t.Errorf("an undeclared class sorted at position %d — a map miss returns 0, "+
			"which is the rank of the MOST irreplaceable class, so an unknown slug "+
			"would jump ahead of the artwork this ordering protects",
			indexOfClass(packs, "ghost"))
	}
}

func indexOfClass(packs []PackInfo, class string) int {
	for i, p := range packs {
		if p.Class == class {
			return i
		}
	}
	return -1
}

// The plugin's PackInfo and pluginapi.BackupPack are two structs describing
// one thing, and the conversion between them is the entire contract with every
// client. A field added to one and not copied across is invisible until a
// puller acts on a default it was never told about.
//
// That is not hypothetical: raw packs shipped exactly that way. The size came
// through — it rides Bytes, which was already copied — so the manifest looked
// right while the flag, the member path and the sha256 were all silently
// absent, leaving clients to open a raw gzip as a zip.
func TestEveryPackFieldSurvivesTheAPIConversion(t *testing.T) {
	internal := PackInfo{
		ID: "abc", Class: "db-dumps", Bytes: 20 << 30, Content: 20 << 30, Members: 1,
		Raw: true, Path: "db-dumps/x/6175.dat.gz", SHA256: "deadbeef",
	}
	got := lpapi.BackupPack{
		ID: internal.ID, Class: internal.Class, Bytes: internal.Bytes,
		Content: internal.Content, Members: internal.Members,
		Raw: internal.Raw, Path: internal.Path, SHA256: internal.SHA256,
	}

	// Compare FIELD COUNTS, so adding one to either struct without extending
	// this conversion fails here rather than in production.
	if a, b := reflect.TypeOf(internal).NumField(), reflect.TypeOf(got).NumField(); a != b {
		t.Fatalf("PackInfo has %d fields, lpapi.BackupPack has %d — one gained a field the other did not, "+
			"and the conversion in apiManifest almost certainly does not copy it", a, b)
	}
	// And every one of them must be non-zero here, or this test would pass
	// while silently ignoring a field nobody set.
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("%s came through zero", reflect.TypeOf(got).Field(i).Name)
		}
	}
}
