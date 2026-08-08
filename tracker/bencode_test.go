package tracker

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

// buildFakeTorrent hand-assembles a minimal .torrent so the expected info
// hash is knowable without round-tripping through an encoder.
func buildFakeTorrent() ([]byte, []byte) {
	info := []byte("d6:lengthi12345e4:name8:test.mkv12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	// outer keys: announce, comment, created by, info — all lex-sorted.
	outer := []byte("d8:announce12:http://old/a7:comment7:stripme10:created by8:stripper4:info")
	outer = append(outer, info...)
	outer = append(outer, 'e')
	return outer, info
}

func TestInfoHashStableAcrossRebuild(t *testing.T) {
	src, info := buildFakeTorrent()

	originalHash, err := InfoHash(src)
	if err != nil {
		t.Fatalf("InfoHash(src) failed: %v", err)
	}
	expected := sha1.Sum(info)
	if originalHash != expected {
		t.Fatalf("InfoHash mismatch: got %s want %s", hex.EncodeToString(originalHash[:]), hex.EncodeToString(expected[:]))
	}

	// Sanitize + rebuild: drop comment/created-by, swap announce. The info
	// dict must come out of the new .torrent byte-for-byte identical.
	rebuilt, err := SanitizeAndRebuild(src, "https://private.local/announce/PASSKEY")
	if err != nil {
		t.Fatalf("SanitizeAndRebuild failed: %v", err)
	}
	rebuiltHash, err := InfoHash(rebuilt)
	if err != nil {
		t.Fatalf("InfoHash(rebuilt) failed: %v", err)
	}
	if rebuiltHash != originalHash {
		t.Fatalf("info hash changed after rebuild: got %s want %s",
			hex.EncodeToString(rebuiltHash[:]), hex.EncodeToString(originalHash[:]))
	}

	// Confirm strippable markers are gone.
	top, err := ScanTopDict(rebuilt)
	if err != nil {
		t.Fatalf("ScanTopDict(rebuilt): %v", err)
	}
	for _, k := range []string{"comment", "created by", "announce-list", "source", "publisher", "publisher-url"} {
		if _, ok := top[k]; ok {
			t.Errorf("marker %q was not stripped", k)
		}
	}
	// And our announce URL is in place.
	ann, err := DecodeString(rebuilt, top["announce"])
	if err != nil || string(ann) != "https://private.local/announce/PASSKEY" {
		t.Errorf("announce URL not rewritten: got %q err=%v", ann, err)
	}
}

func TestSingleFileInfoSummary(t *testing.T) {
	src, _ := buildFakeTorrent()
	sum, err := SummarizeInfo(src)
	if err != nil {
		t.Fatalf("SummarizeInfo: %v", err)
	}
	if sum.Name != "test.mkv" {
		t.Errorf("name: got %q want test.mkv", sum.Name)
	}
	if sum.TotalSize != 12345 {
		t.Errorf("size: got %d want 12345", sum.TotalSize)
	}
	if sum.PieceLength != 16384 {
		t.Errorf("piece length: got %d want 16384", sum.PieceLength)
	}
	if len(sum.Files) != 1 {
		t.Errorf("files: got %d want 1", len(sum.Files))
	}
}

func TestMultiFileInfoSummary(t *testing.T) {
	// info with files list: d5:filesld6:lengthi100e4:pathl1:a1:be ed6:lengthi200e4:pathl1:cee4:name5:batch12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae
	info := []byte("d5:filesld6:lengthi100e4:pathl1:a1:beed6:lengthi200e4:pathl1:ceee4:name5:batch12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	outer := []byte("d8:announce10:http://x/a4:info")
	outer = append(outer, info...)
	outer = append(outer, 'e')

	sum, err := SummarizeInfo(outer)
	if err != nil {
		t.Fatalf("SummarizeInfo: %v", err)
	}
	if sum.Name != "batch" {
		t.Errorf("name: got %q", sum.Name)
	}
	if sum.TotalSize != 300 {
		t.Errorf("size: got %d want 300", sum.TotalSize)
	}
	if len(sum.Files) != 2 {
		t.Fatalf("files: got %d want 2", len(sum.Files))
	}
	if sum.Files[0].Path != "a/b" || sum.Files[0].Length != 100 {
		t.Errorf("file[0]: %+v", sum.Files[0])
	}
	if sum.Files[1].Path != "c" || sum.Files[1].Length != 200 {
		t.Errorf("file[1]: %+v", sum.Files[1])
	}
}

// A non-dict `info` has no info_hash, so it must be an error rather than a
// confident hash of whatever the span happened to cover.
//
// Both copies of this scanner had the gap. On the host side (now pkg/bencode) it
// let the RSS importer dedup two unrelated downloads onto one key; here it would
// register a torrent the swarm can never match. Same fix, kept in step.
func TestInfoHashRejectsNonDictInfo(t *testing.T) {
	for name, in := range map[string]string{
		"info is an int":    "d4:infoi5ee",
		"info is a string":  "d4:info5:helloe",
		"info is a list":    "d4:infol1:ae e",
		"info key missing":  "d8:announce3:abce",
		"not a dict at all": "i9e",
	} {
		t.Run(name, func(t *testing.T) {
			h, err := InfoHash([]byte(in))
			if err == nil {
				t.Errorf("accepted %q, returning %x", in, h)
			}
			if h != [20]byte{} {
				t.Errorf("non-zero hash alongside an error: %x", h)
			}
		})
	}
}
