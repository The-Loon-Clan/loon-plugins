//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The sweep's second pass against real Postgres: the blob fetch, its
// chunking, and the spare/delete resolution. Three rows — one the build
// stored under the kind vouch (articles name .pdf, title carries no
// extension), one under the tag bypass, one genuine junk with a nameless
// blob — and exactly the junk row may go.
func TestJunkSweepSparesWhatTheBuildVouchedFor(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	mkBlob := func(subjects ...string) []byte {
		var arts []stagedArticle
		for i, sub := range subjects {
			arts = append(arts, stagedArticle{
				MessageID: fmt.Sprintf("<s%d@x>", i), Subject: sub, Poster: "p",
				Posted: time.Now(), PartNum: 1, TotalParts: 1, Bytes: 100,
			})
		}
		xmlOut, _, err := buildNZB(arts)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzipBytes(xmlOut)
		if err != nil {
			t.Fatal(err)
		}
		return gz
	}

	rows := []nzbRow{
		{Title: "Erotic Magazine - Fiesta Readers Wives 23", Filename: "fiesta.nzb",
			Size: 2 << 20, Group: "alt.binaries.mag", ContentHash: "hash-sweep-0001",
			Posted: time.Now(), Data: mkBlob(`Magazine - "issue23.pdf" yEnc (1/3)`)},
		{Title: "Some Doujin CG [hentai]", Filename: "doujin.nzb",
			Size: 4 << 20, Group: "alt.binaries.pictures", ContentHash: "hash-sweep-0002",
			Posted: time.Now(), Data: mkBlob("nameless page post")},
		{Title: "elys-1907283e54a1cae9 - qoS5w5nwO0OvHnwj", Filename: "junk.nzb",
			Size: 700 << 10, Group: "alt.binaries.junk", ContentHash: "hash-sweep-0003",
			Posted: time.Now(), Data: mkBlob("wholly nameless")},
	}
	var junkID int64
	for i, n := range rows {
		id, ok, err := s.insertNzb(ctx, n)
		if err != nil || !ok {
			t.Fatalf("seed %d: ok=%v err=%v", i, ok, err)
		}
		if i == 2 {
			junkID = id
		}
	}

	removed, err := s.deleteJunkNzbs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("sweep removed %d rows, want exactly the junk row", removed)
	}
	var left int
	if err := s.db.DB().QueryRow(
		`SELECT COUNT(*) FROM ` + s.db.Schema() + `.nzbs WHERE title IN (
			'Erotic Magazine - Fiesta Readers Wives 23', 'Some Doujin CG [hentai]')`).
		Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("a vouched or tagged release was deleted (%d of 2 remain)", left)
	}
	var junkLeft int
	if err := s.db.DB().QueryRow(
		`SELECT COUNT(*) FROM `+s.db.Schema()+`.nzbs WHERE id = $1`, junkID).
		Scan(&junkLeft); err != nil {
		t.Fatal(err)
	}
	if junkLeft != 0 {
		t.Error("the junk row survived the sweep")
	}
}

// insertNzb's file-key branch, against real Postgres: a repost — fresh
// message-ids, so neither hash nor sketch can match — dedups by the filename
// set, and an empty key never matches an empty key. This branch had zero
// coverage while internalSink dropped the key, so the lookup was dead code.
func TestInsertNzbDedupsByFileKey(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	a := nzbRow{Title: "Call Of The Night S02", Filename: "cotn.nzb", Size: 1 << 30,
		Group: "a.b.anime", ContentHash: "hash-fk-0001", ContentSketch: "sketch-fk-0001",
		ContentFileKey: "filekey-shared-01", Posted: time.Now(), Data: []byte{0x1f, 0x8b}}
	idA, created, err := s.insertNzb(ctx, a)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}

	// The repost shape: everything message-id-derived differs, filenames match.
	b := a
	b.ContentHash, b.ContentSketch = "hash-fk-0002", "sketch-fk-0002"
	idB, created, err := s.insertNzb(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if created || idB != idA {
		t.Errorf("repost was not deduped by file key: created=%v id=%d want existing %d", created, idB, idA)
	}

	// Empty never matches empty: two keyless rows both insert.
	for i, hash := range []string{"hash-fk-0003", "hash-fk-0004"} {
		r := nzbRow{Title: fmt.Sprintf("Keyless %d", i), Filename: fmt.Sprintf("k%d.nzb", i),
			Size: 1 << 20, Group: "a.b.anime", ContentHash: hash,
			Posted: time.Now(), Data: []byte{0x1f, 0x8b}}
		if _, created, err := s.insertNzb(ctx, r); err != nil || !created {
			t.Fatalf("keyless row %d: created=%v err=%v — empty matched empty", i, created, err)
		}
	}
}

// seriesSeasons and seasonPresence must give ONE answer about an SxxE00
// special: it is a release, not an episode number. The count used to say 9
// while presence said 8-and-no-special — two contradictory answers about one
// row, from queries thirty lines apart.
func TestSeriesCountAgreesWithPresenceOnSpecials(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	seed := func(title string) {
		t.Helper()
		if _, ok, err := s.insertNzb(ctx, nzbRow{
			Title: title, Filename: safeFilename(title) + ".nzb", Size: 1 << 30,
			Group: "a.b.tv", ContentHash: "hash-" + safeFilename(title),
			Posted: time.Now(), Data: []byte{0x1f, 0x8b},
		}); err != nil || !ok {
			t.Fatalf("seed %q: ok=%v err=%v", title, ok, err)
		}
	}
	for ep := 1; ep <= 8; ep++ {
		seed(fmt.Sprintf("The.Show.S14E%02d.1080p.WEB", ep))
	}
	seed("The.Show.S14E00.Special.1080p")
	seed("The.Show.S14.COMPLETE.1080p")
	if _, _, err := s.fillEpisodes(ctx, 100); err != nil {
		t.Fatal(err)
	}

	seasons, err := s.seriesSeasons(ctx, "theshow")
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].Season != 14 {
		t.Fatalf("seasons = %+v, want one season 14", seasons)
	}
	if seasons[0].Releases != 10 || seasons[0].Episodes != 8 {
		t.Errorf("season 14: releases=%d episodes=%d, want 10 and 8 — the special or the pack is counted as an episode",
			seasons[0].Releases, seasons[0].Episodes)
	}
	eps, pack, err := s.seasonPresence(ctx, "theshow", 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 8 || !pack {
		t.Errorf("presence: %d eps pack=%v, want 8 and true", len(eps), pack)
	}
	for ep := 1; ep <= 8; ep++ {
		if !eps[ep] {
			t.Errorf("episode %d missing from presence", ep)
		}
	}
}
