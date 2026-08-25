package usenet

import (
	"context"
	"fmt"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// THE PLUGIN HALF OF A CROSS-REPO GOLDEN VECTOR.
//
// contentFileKeyDoc here writes content_file_key at ingest. fileKeyFromNames in
// the site's crosspost backfill (indexer-site/pkg/services) writes it for the
// ~908k rows that predate the column. If the two disagree on anything —
// separator byte, sort order, digest length, the short-single-name guard —
// backfilled rows stop matching newly ingested ones, the dedup silently does
// nothing, and every counter still reads healthy.
//
// Both sides hash the SAME fixture and assert the SAME constant, because two
// implementations of one hash in two repositories is exactly the shape that has
// already bitten this codebase twice (\b vs \y in a Postgres regex; \1 rewritten
// to 0x01 by a helper), green at every gate both times.
//
// The site's copy is TestFileKeyGoldenVector in pkg/services/filekey_golden_test.go.
// wantFileKey must be identical in both, and was verified independently in
// Python rather than pasted from either implementation's output.
const wantFileKey = "57a3c2d41e3800066c63accd077b6c51"

// The real subjects from the five "Call Of The Night (2022) S02 ... AVC-iVy"
// rows the site was showing as separate releases.
func goldenDoc() nzbDoc {
	return nzbDoc{Files: []nzbFile{
		{Subject: `[001/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.par2" yEnc (1/1) 697512`},
		{Subject: `[002/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.part001.rar" yEnc (62/100) 104857600`},
		{Subject: `[003/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.part002.rar" yEnc (2/100) 104857600`},
	}}
}

func TestFileKeyGoldenVector(t *testing.T) {
	got := contentFileKeyDoc(goldenDoc())
	if got != wantFileKey {
		t.Errorf("contentFileKeyDoc = %q, want %q\n"+
			"  If this changed deliberately, the site's TestFileKeyGoldenVector\n"+
			"  must change to the same value in the same commit, or ingest and\n"+
			"  backfill will write different keys and neither will report a fault.",
			got, wantFileKey)
	}
}

// The property the whole key rests on: a second upload of the same content has
// different message-ids and a different representative segment per file, so the
// yEnc counter in the subject differs — and must not reach the key. Measured on
// the five copies: (62/100) against (87/100) for the same file.
func TestFileKeyIgnoresPerCopySubjectNoise(t *testing.T) {
	other := nzbDoc{Files: []nzbFile{
		{Subject: `[001/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.par2" yEnc (1/1) 697512`},
		{Subject: `[002/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.part001.rar" yEnc (87/100) 104857600`},
		{Subject: `[003/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.part002.rar" yEnc (20/100) 104857600`},
	}}
	if a, b := contentFileKeyDoc(goldenDoc()), contentFileKeyDoc(other); a != b {
		t.Errorf("the same files with different yEnc counters keyed differently:\n  %q\n  %q", a, b)
	}
}

// And the property that makes it necessary: message-ids do NOT survive a
// repost, so the sketch cannot connect the same two documents the file key can.
func TestFileKeySucceedsWhereSketchCannot(t *testing.T) {
	withIDs := func(ids ...string) nzbDoc {
		d := goldenDoc()
		for i := range d.Files {
			d.Files[i].Segments = nzbSegments{Segment: []nzbSegment{
				{Number: 1, Bytes: 100, Value: ids[i]},
			}}
		}
		return d
	}
	upload1 := withIDs("a1@post1", "a2@post1", "a3@post1")
	upload2 := withIDs("b1@post2", "b2@post2", "b3@post2")

	if s1, s2 := contentSketchDoc(upload1), contentSketchDoc(upload2); s1 == s2 {
		t.Fatalf("premise broken: two uploads with disjoint message-ids sketched "+
			"the same (%q) — the file key would be unnecessary", s1)
	}
	if k1, k2 := contentFileKeyDoc(upload1), contentFileKeyDoc(upload2); k1 != k2 {
		t.Errorf("two uploads of the SAME files keyed differently:\n  %q\n  %q\n"+
			"  This is precisely the case the key exists for.", k1, k2)
	}
}

func TestFileKeyIsOrderIndependent(t *testing.T) {
	d := goldenDoc()
	rev := nzbDoc{Files: []nzbFile{d.Files[2], d.Files[1], d.Files[0]}}
	if contentFileKeyDoc(d) != contentFileKeyDoc(rev) {
		t.Error("file key depends on file enumeration order")
	}
}

// An unidentifiable document must key to "" — and "" must never match another
// "", or every obfuscated release in the index merges into one row.
func TestFileKeyRefusesUnidentifiableDocuments(t *testing.T) {
	unquoted := nzbDoc{Files: []nzbFile{
		{Subject: `[001/732] - "Show.Name.part001.rar" yEnc (1/100) 100`},
		{Subject: `[002/732] - no quoted name here yEnc (1/100) 100`},
	}}
	if got := contentFileKeyDoc(unquoted); got != "" {
		t.Errorf("a document with an unquoted filename keyed to %q, want empty", got)
	}
	if got := contentFileKeyDoc(nzbDoc{}); got != "" {
		t.Errorf("an empty document keyed to %q, want empty", got)
	}
	short := nzbDoc{Files: []nzbFile{{Subject: `[1/1] - "1.rar" yEnc (1/1) 100`}}}
	if got := contentFileKeyDoc(short); got != "" {
		t.Errorf("a lone generic filename keyed to %q, want empty", got)
	}
	long := nzbDoc{Files: []nzbFile{
		{Subject: `[1/1] - "Call Of The Night S02 1080p.mkv" yEnc (1/1) 100`},
	}}
	if contentFileKeyDoc(long) == "" {
		t.Error("a lone but distinctive filename should still key")
	}
}

func TestFileKeySeparatorPreventsConcatenationCollision(t *testing.T) {
	mk := func(a, b string) nzbDoc {
		return nzbDoc{Files: []nzbFile{
			{Subject: `[1/2] - "` + a + `" yEnc (1/1) 1`},
			{Subject: `[2/2] - "` + b + `" yEnc (1/1) 1`},
		}}
	}
	if contentFileKeyDoc(mk("aaaaaaaaaaaaaaaaaaaab", "c")) ==
		contentFileKeyDoc(mk("aaaaaaaaaaaaaaaaaaaa", "bc")) {
		t.Error("adjacent names concatenate — the NUL separator is missing or ineffective")
	}
}

func TestQuotedFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[002/732] - "Show.part001.rar" yEnc (62/100) 104857600`, "Show.part001.rar"},
		{`no quotes at all`, ""},
		{`only one " quote`, ""},
		{`"" empty`, ""},
		{`  "  padded.rar  " x`, "padded.rar"},
	}
	for _, c := range cases {
		if got := quotedFilename(c.in); got != c.want {
			t.Errorf("quotedFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The multi-file twin of the lone-generic-name rule: two different picture
// sets each posted as fifty numbered members list IDENTICAL names, and the
// old single-file-only guard let the second be swallowed as a repost of the
// first — the sink resolved the first set's row, and its upgrade path could
// even overwrite that row's blob with the second set's document.
func TestFileKeyRefusesGenericMultiFileSets(t *testing.T) {
	var files []nzbFile
	for i := 1; i <= 50; i++ {
		files = append(files, nzbFile{
			Subject: fmt.Sprintf(`[%03d/050] - "%03d.jpg" yEnc (1/1) 100`, i, i),
		})
	}
	if got := contentFileKeyDoc(nzbDoc{Files: files}); got != "" {
		t.Errorf("a numbered dump keyed to %q, want empty", got)
	}
	// One distinctive name is identity enough — the guard must not void real
	// releases that carry numbered members.
	withName := append(files[:len(files):len(files)], nzbFile{
		Subject: `[051/051] - "Family.Album.Kyoto.2024.par2" yEnc (1/1) 100`,
	})
	if contentFileKeyDoc(nzbDoc{Files: withName}) == "" {
		t.Error("a numbered set carrying one distinctive name should still key")
	}
}

// internalSink must hand BOTH content identities to insertNzb. The struct
// literal is deliberately lossy, and dropping ContentFileKey left migration
// 035's column NULL on every internal-mode row — the repost lookup matched
// nothing, silently, while host mode deduped fine.
func TestInternalSinkCarriesBothContentIdentities(t *testing.T) {
	fake := &captureInsertStore{}
	p := &Plugin{st: fake}
	_, _, err := internalSink{p: p}.store(context.Background(), pluginapi.AssembledRelease{
		Title: "Some Release", ContentHash: "h1", ContentSketch: "s1", ContentFileKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.rows) != 1 {
		t.Fatalf("insertNzb called %d times, want 1", len(fake.rows))
	}
	got := fake.rows[0]
	if got.ContentSketch != "s1" || got.ContentFileKey != "k1" || got.ContentHash != "h1" {
		t.Errorf("identities dropped on the way to insertNzb: hash=%q sketch=%q fileKey=%q",
			got.ContentHash, got.ContentSketch, got.ContentFileKey)
	}
}

type captureInsertStore struct {
	Store
	rows []nzbRow
}

func (c *captureInsertStore) insertNzb(_ context.Context, n nzbRow) (int64, bool, error) {
	c.rows = append(c.rows, n)
	return int64(len(c.rows)), true, nil
}
