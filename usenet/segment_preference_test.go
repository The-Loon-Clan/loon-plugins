package usenet

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// Two articles claiming the same (file, part) must resolve by MERIT, not by
// arrival or iteration order.
//
// The staging write used to be a plain HSET keyed on (fileNum, partNum), and
// makeFile's read used to keep whichever article it reached first. Between them
// a truncated or corrupt re-post of one segment could displace the good article
// with nothing noticing: the NZB carries the short article, and the health job
// STATs the message-id, finds it present, and marks the release HEALTHY.
func TestSegmentPreferenceKeepsTheBetterArticle(t *testing.T) {
	good := stagedArticle{MessageID: "<good@news>", PartNum: 7, Bytes: 700000, TotalParts: 10}
	short := stagedArticle{MessageID: "<short@news>", PartNum: 7, Bytes: 12000, TotalParts: 10}
	idless := stagedArticle{MessageID: "", PartNum: 7, Bytes: 900000, TotalParts: 10}

	if !betterArticle(good, short) {
		t.Error("the larger article lost to the truncated one")
	}
	if betterArticle(short, good) {
		t.Error("betterArticle is not antisymmetric on size")
	}
	// A usable message-id outranks size: an article we cannot fetch is worthless
	// however many bytes it claims.
	if !betterArticle(short, idless) {
		t.Error("an article with no message-id beat one that has a message-id")
	}

	// End to end through the document, in both input orders.
	for _, in := range [][]stagedArticle{{short, good}, {good, short}} {
		f := makeFile(in)
		if len(f.Segments.Segment) != 1 {
			t.Fatalf("makeFile emitted %d segments for one part", len(f.Segments.Segment))
		}
		if got := f.Segments.Segment[0].Value; got != "good@news" {
			t.Errorf("document kept %q, want the larger article — input order must not decide", got)
		}
	}
}

// The whole document must be a deterministic function of the staged set. Redis
// returns HGetAll's map and Go randomises map iteration, so anything that reads
// "the first article" is reading a random one — which made two builds of one set
// produce different bytes, and could put a "(37/120)" subject and an arbitrary
// segment's date on the <file> element.
//
// It also matters for identity: content_sketch is computed off the document, so
// a nondeterministic document is a nondeterministic dedup key.
func TestBuildNZBIsDeterministicUnderShuffle(t *testing.T) {
	base := make([]stagedArticle, 0, 60)
	for i := 1; i <= 60; i++ {
		base = append(base, stagedArticle{
			MessageID: fmt.Sprintf("<seg-%02d@news>", i),
			Subject:   fmt.Sprintf("Release.Name (%d/60)", i),
			Poster:    "iVy",
			Posted:    time.Unix(1700000000+int64(i), 0),
			Bytes:     700000,
			PartNum:   i, TotalParts: 60, SegTotal: 60,
		})
	}
	want, wantTotals, err := buildNZB(base)
	if err != nil {
		t.Fatalf("buildNZB: %v", err)
	}

	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 12; round++ {
		shuffled := append([]stagedArticle{}, base...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, totals, err := buildNZB(shuffled)
		if err != nil {
			t.Fatalf("buildNZB shuffled: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d: shuffling the staged set changed the document bytes", round)
		}
		if totals.Sketch != wantTotals.Sketch {
			t.Fatalf("round %d: sketch changed under shuffle (%s vs %s) — the dedup key would "+
				"depend on map iteration order", round, totals.Sketch, wantTotals.Sketch)
		}
	}

	// And the file attributes come from part 1, not from a random segment.
	doc := makeFile(base)
	if doc.Subject != "Release.Name (1/60)" {
		t.Errorf("file subject = %q, want part 1's — it is the one that describes the file", doc.Subject)
	}
	if doc.Date != time.Unix(1700000001, 0).Unix() {
		t.Errorf("file date = %d, want part 1's", doc.Date)
	}
}
