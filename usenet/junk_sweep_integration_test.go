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
