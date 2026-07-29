package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
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
