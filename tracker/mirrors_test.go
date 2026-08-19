package tracker

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The mirror seam: the index asking the tracker whether it carries a release,
// and asking it to. What the torrent itself must look like is
// torrentbuild_test.go's business.

func sampleRequest() pluginapi.MirrorRequest {
	return pluginapi.MirrorRequest{
		ReleaseID: 42,
		Name:      "Some.Show.S01E02.1080p.WEB-DL.x264-GRP",
		Size:      1_500_000_000,
		UserID:    7,
		Files: []pluginapi.MirrorFile{
			{Path: "some.show.s01e02.mkv", Length: 1_499_000_000},
			{Path: "some.show.s01e02.nfo", Length: 1_000_000},
		},
	}
}

// Mirroring is reachable from a button, so a second click must find the first
// torrent rather than make another one for the same release.
func TestMirrorIsIdempotent(t *testing.T) {
	m := mirrorReader{NewMemStore()}
	ctx := context.Background()

	first, err := m.Mirror(ctx, sampleRequest())
	if err != nil {
		t.Fatalf("first mirror: %v", err)
	}
	if first.InfoHash == "" || first.Href == "" {
		t.Fatalf("mirror = %+v, want a hash and the tracker's own link", first)
	}

	// A second call, by a DIFFERENT member: still one torrent.
	req := sampleRequest()
	req.UserID = 99
	second, err := m.Mirror(ctx, req)
	if err != nil {
		t.Fatalf("second mirror: %v", err)
	}
	if second.InfoHash != first.InfoHash {
		t.Errorf("second mirror made a different torrent: %s vs %s", second.InfoHash, first.InfoHash)
	}
	got, err := m.MirrorsOf(ctx, []int64{42})
	if err != nil {
		t.Fatalf("MirrorsOf: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("release 42 has %d torrents, want exactly 1", len(got))
	}
}

// A release with no size is one the index has not finished assembling. A
// torrent of nothing is worse than no torrent, so it is refused rather than
// stored as a zero-byte row somebody has to explain later.
func TestMirrorRefusesASizelessRelease(t *testing.T) {
	m := mirrorReader{NewMemStore()}
	for _, req := range []pluginapi.MirrorRequest{
		{ReleaseID: 42, Name: "x", Size: 0},
		{ReleaseID: 0, Name: "x", Size: 100},
	} {
		if _, err := m.Mirror(context.Background(), req); err == nil {
			t.Errorf("mirrored %+v", req)
		}
	}
}

// The read side and the write side describe one torrent the same way — both go
// through toMirror, and this is what says so. Two descriptions of one torrent
// is how a badge and the page it links to end up disagreeing.
func TestMirrorReadAndWriteAgree(t *testing.T) {
	m := mirrorReader{NewMemStore()}
	ctx := context.Background()
	made, err := m.Mirror(ctx, sampleRequest())
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	read, err := m.MirrorsOf(ctx, []int64{42})
	if err != nil {
		t.Fatalf("MirrorsOf: %v", err)
	}
	if read[42] != made {
		t.Errorf("read %+v, write returned %+v", read[42], made)
	}
}

// The batched read is what a listing calls, so its edges are a page's edges: no
// ids at all, and ids for releases nothing was ever mirrored from.
func TestMirrorsOfAnswersOnlyWhatExists(t *testing.T) {
	m := mirrorReader{NewMemStore()}
	ctx := context.Background()
	if _, err := m.Mirror(ctx, sampleRequest()); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	got, err := m.MirrorsOf(ctx, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("MirrorsOf(nil) = %v, %v; want an empty map and no error", got, err)
	}
	// A release with no torrent is ABSENT, never present-and-zero: 0 seeders is
	// a dead torrent, and it must not read the same as no torrent.
	got, err = m.MirrorsOf(ctx, []int64{42, 43, 44})
	if err != nil {
		t.Fatalf("MirrorsOf: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d mirrors for 3 ids, want only the 1 that exists", len(got))
	}
	if _, present := got[43]; present {
		t.Error("a release with no torrent has an entry in the map")
	}
}
