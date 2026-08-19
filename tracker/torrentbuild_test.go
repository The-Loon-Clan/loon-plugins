package tracker

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"sort"
	"testing"

	"github.com/the-loon-clan/loon/bencode"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The built torrents, read back the way a client would read them.
//
// Every check here exists because the failure it catches is INVISIBLE from the
// database. A torrent whose keys are out of order, whose piece count does not
// match its length, or whose info dict was re-encoded on the way out still
// stores as a perfectly ordinary row — and the first person to notice is
// somebody whose client says the tracker does not know this torrent, on a
// machine nobody here can see.
//
// Lifted with the builder itself out of the demo host, where it was testing the
// site's copy of this code. There is one builder now, and this is its test.

// A spread of real sizes: the smallest that still splits into volumes, one in
// the middle, and one at the top of what an indexer carries.
var buildSizes = []int64{
	632 * 1000 * 1000,
	4728 * 1000 * 1000,
	35 * 1000 * 1000 * 1000,
	// Below the split threshold, so this one takes the single-file branch.
	volBytes / 16,
}

func TestBuildTorrentIsAValidTorrent(t *testing.T) {
	const title = "Some.Release.2024.1080p.WEB-DL.DDP5.1.H.264-GROUP"
	for _, size := range buildSizes {
		// No Files: the modelled-rar-set branch, which is what an indexer that
		// did not parse the NZB's file list produces.
		dt := BuildTorrent(pluginapi.MirrorRequest{ReleaseID: 1, Name: title, Size: size})

		// 1. The hash is the hash OF the bytes. If these ever disagree the
		//    announce path looks up a torrent nobody can download.
		sum := sha1.Sum(dt.InfoBytes)
		if want := hex.EncodeToString(sum[:]); dt.InfoHash != want {
			t.Errorf("size %d: info_hash %s is not the SHA-1 of info_bytes (%s)",
				size, dt.InfoHash, want)
		}

		keys, err := bencode.ScanTopDict(dt.InfoBytes)
		if err != nil {
			t.Fatalf("size %d: info dict does not parse: %v", size, err)
		}

		// 2. Keys ascend. BEP-3 requires it, bencode.Writer does not sort (it
		//    cannot — it has no idea when a dict is finished), and a client
		//    hashes what it was given without re-sorting. So the encoder here
		//    is the only thing keeping this true, which is why it is tested
		//    rather than assumed.
		order := make([]string, 0, len(keys))
		for k := range keys {
			order = append(order, k)
		}
		sort.Slice(order, func(i, j int) bool {
			return keys[order[i]].Start < keys[order[j]].Start
		})
		if !sort.StringsAreSorted(order) {
			t.Errorf("size %d: info dict keys are not in ascending order: %v", size, order)
		}

		// 3. A private tracker's torrents say so — no DHT, no PEX (BEP 27).
		if priv, err := bencode.DecodeInt(dt.InfoBytes, keys["private"]); err != nil || priv != 1 {
			t.Errorf("size %d: private = %d, %v; want 1", size, priv, err)
		}

		// 4. The file lengths total the release size. One byte out here and the
		//    torrent claims a size the row beside it disagrees with.
		var total int64
		files, err := bencode.DecodeList(dt.InfoBytes, keys["files"])
		if err != nil {
			t.Fatalf("size %d: files is not a list: %v", size, err)
		}
		for _, span := range files {
			fd, err := bencode.ScanDict(dt.InfoBytes, span)
			if err != nil {
				t.Fatalf("size %d: file entry is not a dict: %v", size, err)
			}
			n, err := bencode.DecodeInt(dt.InfoBytes, fd["length"])
			if err != nil {
				t.Fatalf("size %d: file length: %v", size, err)
			}
			total += n
			// path is a LIST of components. A bare string parses as bencode and
			// is rejected by clients, which is the sort of thing that only shows
			// up in somebody else's torrent client.
			if _, err := bencode.DecodeList(dt.InfoBytes, fd["path"]); err != nil {
				t.Errorf("size %d: file path is not a list: %v", size, err)
			}
		}
		if total != size {
			t.Errorf("size %d: file lengths total %d", size, total)
		}
		if len(files) != dt.FileCount {
			t.Errorf("size %d: file_count %d but %d files in the dict",
				size, dt.FileCount, len(files))
		}

		// 5. One 20-byte hash per piece, and the piece count covers the whole
		//    size. A short pieces string is the classic silent corruption: the
		//    dict parses, the hash is stable, and the client stalls at the end.
		pieces, err := bencode.DecodeString(dt.InfoBytes, keys["pieces"])
		if err != nil {
			t.Fatalf("size %d: pieces: %v", size, err)
		}
		wantPieces := int((size + dt.PieceLength - 1) / dt.PieceLength)
		if len(pieces) != wantPieces*sha1.Size {
			t.Errorf("size %d: pieces is %d bytes, want %d (%d pieces × 20)",
				size, len(pieces), wantPieces*sha1.Size, wantPieces)
		}

		// 6. files_json describes the same files, since it is what an operator
		//    or a later feature would read instead of re-parsing the dict.
		var fj []torrentFile
		if err := json.Unmarshal(dt.FilesJSON, &fj); err != nil {
			t.Fatalf("size %d: files_json: %v", size, err)
		}
		if len(fj) != len(files) {
			t.Errorf("size %d: files_json has %d entries, dict has %d",
				size, len(fj), len(files))
		}
	}
}

// The caller's file list is used verbatim, and it is the reason MirrorRequest
// carries one: an NZB names its real files, so a torrent mirrored from a
// release can carry the real names rather than an invented .part01.rar. The
// structure is then true even where the piece hashes are not.
func TestBuildTorrentKeepsTheCallersFiles(t *testing.T) {
	dt := BuildTorrent(pluginapi.MirrorRequest{
		ReleaseID: 1, Name: "Some.Show.S01E02.1080p-GRP", Size: 1_500_000_000,
		Files: []pluginapi.MirrorFile{
			{Path: "some.show.s01e02.mkv", Length: 1_499_000_000},
			{Path: "some.show.s01e02.nfo", Length: 1_000_000},
			// Dropped: a zero-length entry would be a file the dict claims and
			// no client can ever complete.
			{Path: "empty.txt", Length: 0},
		},
	})
	if dt.FileCount != 2 {
		t.Fatalf("file count = %d, want the 2 usable files the caller gave", dt.FileCount)
	}
	keys, err := bencode.ScanTopDict(dt.InfoBytes)
	if err != nil {
		t.Fatalf("info dict does not parse: %v", err)
	}
	files, err := bencode.DecodeList(dt.InfoBytes, keys["files"])
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %d entries, %v; want 2", len(files), err)
	}
	fd, err := bencode.ScanDict(dt.InfoBytes, files[0])
	if err != nil {
		t.Fatalf("first file: %v", err)
	}
	parts, err := bencode.DecodeList(dt.InfoBytes, fd["path"])
	if err != nil || len(parts) != 1 {
		t.Fatalf("path = %d components, %v; want 1", len(parts), err)
	}
	name, _ := bencode.DecodeString(dt.InfoBytes, parts[0])
	if string(name) != "some.show.s01e02.mkv" {
		t.Errorf("first file = %q, want the caller's name", name)
	}
}

// A caller that HOLDS the content passes real hashes, and they must reach the
// dict untouched — that is the difference between a torrent a client can verify
// and one it cannot.
func TestBuildTorrentUsesRealPiecesWhenGiven(t *testing.T) {
	real := make([]byte, 2*sha1.Size)
	for i := range real {
		real[i] = byte(i)
	}
	dt := BuildTorrent(pluginapi.MirrorRequest{
		ReleaseID: 1, Name: "Held.Locally-GRP", Size: 2 << 20,
		PieceLength: 1 << 20, Pieces: real,
	})
	if dt.PieceLength != 1<<20 {
		t.Errorf("piece length = %d, want the caller's", dt.PieceLength)
	}
	keys, _ := bencode.ScanTopDict(dt.InfoBytes)
	got, err := bencode.DecodeString(dt.InfoBytes, keys["pieces"])
	if err != nil || string(got) != string(real) {
		t.Error("the caller's piece hashes were not the ones stored")
	}
}

// The end-to-end check, and the one that matters most: the .torrent a member
// downloads must announce on the hash the tracker recorded.
//
// BuildForUser splices info_bytes into an outer dict UNCHANGED — that is the
// whole reason the bytes are stored rather than the fields — and
// bencode.InfoHash re-derives the hash from the result. If the builder ever
// produced something the splice had to touch, this is where it shows.
func TestBuiltTorrentSurvivesTheDownloadPath(t *testing.T) {
	dt := BuildTorrent(pluginapi.MirrorRequest{
		ReleaseID: 1, Name: "Another.Release.S01E02.2160p.WEB-DL-GROUP", Size: 4575 * 1000 * 1000,
	})
	blob := BuildForUser(dt.InfoBytes, "http://localhost:8090/api/tracker/announce/deadbeef")

	sum, err := bencode.InfoHash(blob)
	if err != nil {
		t.Fatalf("the built .torrent has no readable info hash: %v", err)
	}
	if got := hex.EncodeToString(sum[:]); got != dt.InfoHash {
		t.Fatalf("the .torrent announces %s but the tracker recorded %s", got, dt.InfoHash)
	}
}

// Deterministic: the same release built twice is the same torrent. Random piece
// hashes would pass every check above and fail this one — and mirroring a
// release that is already mirrored would make a second copy of it rather than
// finding the first.
func TestBuildTorrentIsDeterministic(t *testing.T) {
	req := pluginapi.MirrorRequest{ReleaseID: 1, Name: "Deterministic.Release.1080p-GROUP", Size: 3660 * 1000 * 1000}
	if a, b := BuildTorrent(req), BuildTorrent(req); a.InfoHash != b.InfoHash {
		t.Fatalf("two builds of the same release gave %s and %s", a.InfoHash, b.InfoHash)
	}
}

func TestPieceLengthStaysInBounds(t *testing.T) {
	for _, size := range append(buildSizes, 1, 1<<40) {
		pl := pieceLength(size)
		if pl < minPieceLength || pl > maxPieceLength {
			t.Errorf("size %d: piece length %d is outside [%d, %d]",
				size, pl, minPieceLength, maxPieceLength)
		}
		if pl&(pl-1) != 0 {
			t.Errorf("size %d: piece length %d is not a power of two", size, pl)
		}
	}
}
