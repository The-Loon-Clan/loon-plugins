// mirrors.go declares how a page finds out that a release is ALSO on the
// tracker — the seam that lets one row offer both ways of getting the same
// thing.
//
// A site that runs an indexer and a tracker holds the same content twice: an
// NZB posted to Usenet, and a torrent made from it. `torrents.nzb_id` has
// always recorded which release a torrent came from, and nothing ever read it
// in bulk — so a listing showed the NZB and the tracker showed the torrent and
// a member had to know both pages existed. This is the read that lets a
// release row say "also on the tracker" without the host learning the
// tracker's schema, and without asking one question per row.
package pluginapi

import "context"

// TorrentMirrorsName is the Core extension-registry key. Absent means no
// tracker, or one that is idle — every caller is gated on the lookup and
// simply renders the NZB side alone, which is the whole page a pure indexer
// has ever had.
const TorrentMirrorsName = "tracker.mirrors"

// TorrentMirror is what the tracker carries for one release. Only the fields a
// listing renders: enough for a link and a swarm figure, and nothing that
// would make this a second copy of the tracker's row.
type TorrentMirror struct {
	// InfoHash identifies the torrent, and is what the tracker's own pages key
	// on — so a caller can link straight to it.
	InfoHash string
	// Name is the torrent's name, which need not equal the release title: a
	// torrent may have been made from a repack or renamed on upload.
	Name string
	// Href is where the tracker renders this torrent. The TRACKER fills it in,
	// because the route is the tracker's to own and to move — the mirror image
	// of the ReleaseURL seam, by which the tracker asks the host where a
	// release is browsable rather than hardcoding /release/%d.
	//
	// Empty means "no page to send anybody to", and a caller then renders the
	// mirror as a fact rather than as a link.
	Href string
	// Seeders and Leechers are the denormalised swarm counts as of the last
	// announce sweep. Zero seeders is a real answer — a dead torrent — and is
	// why a caller must distinguish "no mirror" (no map entry) from "a mirror
	// with nobody on it".
	Seeders  int
	Leechers int
}

// TorrentMirrorMakerName is where the tracker publishes the WRITE side: making
// a torrent for a release that has none.
const TorrentMirrorMakerName = "tracker.mirror.make"

// MirrorFile is one file of a release, for the torrent's file list.
type MirrorFile struct {
	Path   string
	Length int64
}

// MirrorRequest describes the release to mirror. The caller owns the index and
// therefore owns every fact here; the tracker owns what a torrent is.
type MirrorRequest struct {
	// ReleaseID is the host's id for the release, stored as torrents.nzb_id —
	// the link that makes the mirror findable from the index side afterwards.
	ReleaseID int64
	// Name becomes the torrent's name, and is what a member recognises it by.
	Name string
	// Size is the release's total size in bytes.
	Size int64
	// Files is the release's real file list, when the caller has one — an NZB
	// names its files and their sizes, so the torrent's structure can be true
	// even where its piece hashes are not. Empty is fine: the tracker then
	// models the usual rar set from Size.
	Files []MirrorFile
	// UserID is who asked for the mirror, recorded as the uploader.
	UserID int64

	// Pieces and PieceLength are the REAL piece hashes, for a caller that holds
	// the content and has hashed it.
	//
	// Empty is the ordinary case for an indexer: an NZB is a pointer to
	// articles, not the bytes, so nothing on this side can hash them. The
	// tracker then generates a deterministic placeholder chain, and the
	// resulting .torrent announces and downloads but would fail a client's
	// verification — which is the honest state of a mirror made from a release
	// nobody holds. A host that DOES hold the payload fills these in and gets a
	// torrent that works.
	Pieces      []byte
	PieceLength int64
}

// TorrentMirrorMaker makes a torrent for a release that has none.
type TorrentMirrorMaker interface {
	// Mirror registers a torrent for the release and returns it.
	//
	// IDEMPOTENT, and it has to be: this is reachable from a button, and a
	// second click must find the first torrent rather than make a second one
	// for the same release. A release that is already mirrored returns its
	// existing torrent unchanged, including its uploader.
	Mirror(ctx context.Context, req MirrorRequest) (TorrentMirror, error)
}

// TorrentMirrors answers which releases the tracker also carries.
type TorrentMirrors interface {
	// MirrorsOf maps release id → the torrent made from it, for those release
	// ids that have one. Ids with no torrent are simply absent from the map,
	// never present-and-zero.
	//
	// BATCHED deliberately. The caller is a listing with fifty rows on it, and
	// the per-row question (TorrentByNzbID) would be fifty round trips for a
	// badge. An empty or nil slice is not an error and returns an empty map.
	//
	// Where a release has more than one torrent — the schema permits it, a
	// repack upload being the usual cause — the newest is the one returned,
	// matching what the tracker's own per-release lookup shows.
	MirrorsOf(ctx context.Context, releaseIDs []int64) (map[int64]TorrentMirror, error)
}
