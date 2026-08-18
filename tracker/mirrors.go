package tracker

import (
	"context"

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

var _ pluginapi.TorrentMirrors = mirrorReader{}

func (m mirrorReader) MirrorsOf(ctx context.Context, releaseIDs []int64) (map[int64]pluginapi.TorrentMirror, error) {
	rows, err := m.store.TorrentsByNzbIDs(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]pluginapi.TorrentMirror, len(rows))
	for id, t := range rows {
		out[id] = pluginapi.TorrentMirror{
			InfoHash: t.InfoHash,
			Name:     t.Name,
			Href:     TorrentPath(t.InfoHash),
			Seeders:  t.Seeders,
			Leechers: t.Leechers,
		}
	}
	return out, nil
}
