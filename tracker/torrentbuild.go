package tracker

import (
	"crypto/sha1" //nolint:gosec // BitTorrent info hashes ARE SHA-1; this is the protocol, not a security choice
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/the-loon-clan/loon/bencode"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Building a torrent from a description of a release.
//
// A torrent IS its info dictionary: the info_hash is that dictionary's SHA-1,
// the .torrent download splices the stored bytes in unchanged, and the announce
// path keys on the hash. Get the encoding wrong and the rows look perfectly
// correct in psql while every .torrent the site hands out is one no client will
// accept — a failure that surfaces on somebody else's machine, in a client,
// days later. So the dictionary is built with loon/bencode, the same encoder
// the .torrent download uses to rebuild a torrent per member.
//
// THE PIECE HASHES ARE THE HONEST PART.
//
// Piece hashes are SHA-1 of the actual file bytes, and an INDEX does not have
// the bytes — an NZB is a pointer to articles on a news server, not the
// content. So a caller that holds the payload passes the real hashes in
// (MirrorRequest.Pieces) and gets a torrent a client can verify; a caller that
// does not gets a deterministic placeholder chain, and gets a torrent that
// announces, downloads as a .torrent, and would fail verification the moment a
// client actually fetched a piece.
//
// Deterministic rather than random, and that is load-bearing: the same release
// built twice produces the same info_hash, so mirroring a release that is
// already mirrored collides with itself instead of making a second copy.

// Piece length bounds and target, which are the conventions every torrent
// creator follows rather than anything this project decided: powers of two
// between 256 KiB and 16 MiB, chosen so the piece COUNT stays near 1500. Too
// few pieces and a client cannot verify incrementally; too many and the
// .torrent itself becomes megabytes of hashes.
const (
	minPieceLength = 256 << 10
	maxPieceLength = 16 << 20
	piecesTarget   = 1500
)

// Shape of the rar set modelled when a caller supplies no file list. A release
// on Usenet is an NFO, an SFV and a run of volumes, so that is what these
// describe — and it means file_count and files_json say something, rather than
// a listing whose Files column reads 1 on every row.
const (
	nfoBytes = 4 << 10   // an NFO is a few kilobytes of ASCII art and a description
	sfvBytes = 1 << 10   // one CRC line per volume
	volBytes = 500 << 20 // 500 MiB volumes, the common posting size
)

// BuiltTorrent is a torrent ready to store: the info dict, its hash, and the
// denormalised fields the catalogue reads without decoding it again.
type BuiltTorrent struct {
	InfoHash    string
	Name        string
	Size        int64
	PieceLength int64
	FileCount   int
	FilesJSON   []byte
	InfoBytes   []byte
}

// torrentFile is one entry of the info dict's file list, and of files_json.
type torrentFile struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// BuildTorrent turns a described release into a torrent.
//
// Exported because two callers build torrents and they must build them the same
// way: the mirror seam below, and a host seeding demo data. A second
// implementation of this is a second bencode encoder to keep byte-identical,
// and "the demo's torrents hash differently from the real ones" is a bug nobody
// would find by reading either copy.
func BuildTorrent(req pluginapi.MirrorRequest) BuiltTorrent {
	name := torrentName(req.Name)
	files := requestFiles(name, req)
	pieceLen := req.PieceLength
	if pieceLen <= 0 {
		pieceLen = pieceLength(req.Size)
	}
	pieces := req.Pieces
	if len(pieces) == 0 {
		pieces = placeholderPieces(name, int((req.Size+pieceLen-1)/pieceLen))
	}

	info := infoDict(name, files, pieceLen, pieces)
	sum := sha1.Sum(info) //nolint:gosec // see the file header
	fj, err := json.Marshal(files)
	if err != nil {
		// Marshalling a []torrentFile cannot fail — every field is a string or
		// an int64 — so this is unreachable rather than handled. NULL is a legal
		// files_json and nothing renders it, so a torrent survives it.
		fj = nil
	}
	return BuiltTorrent{
		InfoHash:    hex.EncodeToString(sum[:]),
		Name:        name,
		Size:        req.Size,
		PieceLength: pieceLen,
		FileCount:   len(files),
		FilesJSON:   fj,
		InfoBytes:   info,
	}
}

// requestFiles is the caller's file list, or a modelled rar set when it gave
// none.
//
// The caller's list is preferred and is the whole reason MirrorRequest carries
// one: an NZB names its files and their sizes, so a torrent mirrored from a
// release can carry the REAL names rather than an invented .part01.rar — the
// structure is then true even where the piece hashes are not.
func requestFiles(name string, req pluginapi.MirrorRequest) []torrentFile {
	out := make([]torrentFile, 0, len(req.Files))
	for _, f := range req.Files {
		path := torrentName(f.Path)
		if path == "" || f.Length <= 0 {
			continue
		}
		out = append(out, torrentFile{Path: path, Length: f.Length})
	}
	if len(out) > 0 {
		return out
	}
	return modelledRarSet(name, req.Size)
}

// infoDict encodes the info dictionary.
//
// The keys are written in ascending order — files, name, piece length, pieces,
// private — because bencode requires it and no client re-sorts before hashing.
// A dict out of order still parses, still stores, and hashes to something the
// tracker has never heard of.
func infoDict(name string, files []torrentFile, pieceLen int64, pieces []byte) []byte {
	var w bencode.Writer
	w.BeginDict()
	w.Str("files")
	w.BeginList()
	for _, f := range files {
		w.BeginDict()
		w.Str("length")
		w.Int(f.Length)
		// path is a LIST of components, not a string: it is how a torrent
		// expresses a subdirectory, and a client reading a bare string here
		// rejects the file.
		w.Str("path")
		w.BeginList()
		w.Str(f.Path)
		w.End()
		w.End()
	}
	w.End()
	w.Str("name")
	w.Str(name)
	w.Str("piece length")
	w.Int(pieceLen)
	w.Str("pieces")
	w.Bytes(pieces)
	// private (BEP 27): no DHT, no PEX, no peer exchange — the tracker is the
	// only way to find peers. Set because this IS a private tracker, and a
	// torrent that advertised itself as public would be teaching the wrong
	// shape.
	w.Str("private")
	w.Int(1)
	w.End()
	return w.Out()
}

// modelledRarSet describes a release the way it sits on Usenet, for a caller
// that gave no file list.
func modelledRarSet(base string, size int64) []torrentFile {
	rest := size - nfoBytes - sfvBytes
	if rest < volBytes/8 {
		// Too small to be worth splitting. Single file, and the length is the
		// whole size so the dict still totals correctly.
		return []torrentFile{{Path: base + ".bin", Length: size}}
	}
	files := []torrentFile{
		{Path: base + ".nfo", Length: nfoBytes},
		{Path: base + ".sfv", Length: sfvBytes},
	}
	vols := int((rest + volBytes - 1) / volBytes)
	for i := range vols {
		n := int64(volBytes)
		if i == vols-1 {
			// The last volume carries the remainder, so the file lengths sum to
			// exactly the release size. Off by one byte here and the torrent
			// claims a size the database disagrees with.
			n = rest - int64(vols-1)*volBytes
		}
		files = append(files, torrentFile{
			Path: fmt.Sprintf("%s.part%02d.rar", base, i+1), Length: n,
		})
	}
	return files
}

// pieceLength picks the power of two that keeps the piece count near the
// target, within the conventional bounds.
func pieceLength(size int64) int64 {
	pl := int64(minPieceLength)
	for pl < maxPieceLength && size/pl > piecesTarget {
		pl <<= 1
	}
	return pl
}

// placeholderPieces produces count × 20 bytes of piece hashes for a caller that
// does not hold the content.
//
// A SHA-1 chain from the name rather than crypto/rand, and the difference
// matters: random bytes would give a different info_hash on every call, so
// mirroring the same release twice would make two torrents instead of finding
// the first one.
//
// These hashes describe no real data. A client that fetched a piece would fail
// to verify it — which is the truth about an index that holds pointers to
// articles rather than files, and is why MirrorRequest.Pieces exists for the
// callers that can do better.
func placeholderPieces(seed string, count int) []byte {
	if count < 1 {
		count = 1
	}
	out := make([]byte, 0, count*sha1.Size)
	h := sha1.Sum([]byte("loon-mirror:" + seed)) //nolint:gosec // see the file header
	for range count {
		out = append(out, h[:]...)
		h = sha1.Sum(h[:]) //nolint:gosec // see the file header
	}
	return out
}

// torrentName makes a release title safe to use as a name or a path component.
//
// Only the separators and control characters, not a general scrub: a release
// name is what a member recognises the torrent by, and rewriting the brackets
// and dots out of it would leave something they cannot match to the index.
func torrentName(title string) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 {
			return '.'
		}
		return r
	}, strings.TrimSpace(title))
	if name == "" {
		return "release"
	}
	return name
}
