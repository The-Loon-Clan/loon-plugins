package tracker

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// mirrorReader publishes the tracker's catalogue as pluginapi.TorrentMirrors:
// the "is this release also a torrent" question, answered a page at a time.
//
// An adapter rather than a method on the store, because the two types say
// different things. The store's Torrent is this schema's row — info bytes,
// piece length, uploader, snatches. The seam's TorrentMirror is what a
// FOREIGN page renders: a hash to link to and a swarm to show. Keeping them
// apart is what stops a caller from growing a dependency on a column that
// happens to be in the struct.
// TorrentPath is where this plugin renders one torrent.
//
// Exported and used by mirrorReader below rather than being a string literal in
// a FOREIGN template: the route is registered in Provision as "/tracker/t/…",
// and a host that hardcoded that would break silently the day it moved. The
// path is built here, beside the registration, so both change together.
func TorrentPath(infoHash string) string {
	if infoHash == "" {
		return ""
	}
	return "/tracker/t/" + infoHash
}

type mirrorReader struct{ store Store }

var (
	_ pluginapi.TorrentMirrors     = mirrorReader{}
	_ pluginapi.TorrentMirrorMaker = mirrorReader{}
)

// Mirror makes a torrent for a release that has none, and returns the existing
// one for a release that already does.
//
// Idempotent twice over, which is what makes it safe behind a button. The
// lookup below catches a release that was mirrored earlier; and even without
// it, BuildTorrent is deterministic, so a second call produces the same
// info_hash and the upsert's ON CONFLICT keeps the first row's uploader.
func (m mirrorReader) Mirror(ctx context.Context, req pluginapi.MirrorRequest) (pluginapi.TorrentMirror, error) {
	if req.ReleaseID <= 0 || req.Size <= 0 {
		// Refused rather than mirrored as a zero-byte torrent: a release with
		// no size is one the index has not finished assembling, and a torrent
		// of nothing is worse than no torrent.
		return pluginapi.TorrentMirror{}, fmt.Errorf("mirror: release %d has no size", req.ReleaseID)
	}
	if existing, err := m.store.TorrentByNzbID(ctx, req.ReleaseID); err != nil {
		return pluginapi.TorrentMirror{}, err
	} else if existing != nil {
		return toMirror(existing), nil
	}

	built := BuildTorrent(req)
	t := &Torrent{
		InfoHash: built.InfoHash, Name: built.Name, Size: built.Size,
		PieceLength: built.PieceLength, FileCount: built.FileCount,
		FilesJSON: built.FilesJSON, InfoBytes: built.InfoBytes,
		NzbID: &req.ReleaseID,
	}
	if req.UserID > 0 {
		t.UploadedBy = &req.UserID
	}
	if err := m.store.UpsertTorrent(ctx, t); err != nil {
		return pluginapi.TorrentMirror{}, err
	}
	// Read back rather than returning what was written: the upsert keeps the
	// first row's uploader and swarm counts on a conflict, so the row in the
	// table is the answer and the struct above is only a proposal.
	stored, err := m.store.Torrent(ctx, built.InfoHash)
	if err != nil {
		return pluginapi.TorrentMirror{}, err
	}
	if stored == nil {
		return pluginapi.TorrentMirror{}, fmt.Errorf("mirror: torrent %s vanished after upsert", built.InfoHash)
	}
	return toMirror(stored), nil
}

// toMirror is the one place a stored row becomes the seam's struct, so the read
// and the write cannot describe the same torrent differently.
func toMirror(t *Torrent) pluginapi.TorrentMirror {
	return pluginapi.TorrentMirror{
		InfoHash: t.InfoHash,
		Name:     t.Name,
		Href:     TorrentPath(t.InfoHash),
		Seeders:  t.Seeders,
		Leechers: t.Leechers,
	}
}

func (m mirrorReader) MirrorsOf(ctx context.Context, releaseIDs []int64) (map[int64]pluginapi.TorrentMirror, error) {
	rows, err := m.store.TorrentsByNzbIDs(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]pluginapi.TorrentMirror, len(rows))
	for id, t := range rows {
		out[id] = toMirror(t)
	}
	return out, nil
}
