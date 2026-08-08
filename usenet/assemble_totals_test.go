package usenet

import "testing"

// The release row must describe the NZB IT SERVES, not the staged input it was
// built from.
//
// summarize(arts) and len(arts) count every loaded article; makeFile emits one
// segment per (file, part). Whenever a set holds two articles with the same
// part number — a repost, or a staging-key collision — the row used to count
// both while the document carried one. Measured over 84,112 catalogued
// releases: 2,305 claimed more than they deliver, 1,296 of those by 10x or
// more, together claiming 477.8 GB against 14.2 GB of real content.
//
// The two cases below are the two ways it happens, and they are NOT the same
// bug: reposts are healthy (the NZB is complete, only the row lied), while
// collisions are broken content. This change fixes the row for both; it does
// not pretend to repair the second.
func TestNZBTotalsCountTheDocumentNotTheInput(t *testing.T) {
	// A repost: the same release posted twice. Every part number appears with
	// two different message-ids, so pg staging holds both rows.
	arts := []stagedArticle{
		{MessageID: "<a1@x>", Subject: "rel (1/2) yEnc", Group: "a.b", Bytes: 1000, PartNum: 1, TotalParts: 2},
		{MessageID: "<a2@x>", Subject: "rel (2/2) yEnc", Group: "a.b", Bytes: 1000, PartNum: 2, TotalParts: 2},
		{MessageID: "<b1@x>", Subject: "rel (1/2) yEnc", Group: "a.b", Bytes: 1000, PartNum: 1, TotalParts: 2},
		{MessageID: "<b2@x>", Subject: "rel (2/2) yEnc", Group: "a.b", Bytes: 1000, PartNum: 2, TotalParts: 2},
	}
	xmlBytes, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Segments != 2 {
		t.Errorf("segments = %d, want 2 (the document holds 2; len(arts) is 4)", totals.Segments)
	}
	if totals.Bytes != 2000 {
		t.Errorf("bytes = %d, want 2000 (summarize would say 4000)", totals.Bytes)
	}
	// The old values, kept here so the difference is on the record rather than
	// implied: this is exactly the 2x inflation seen on real reposted releases.
	if got, _ := summarize(arts); got != 4000 {
		t.Errorf("summarize = %d, want 4000 — if this changed, the premise moved", got)
	}
	if len(xmlBytes) == 0 {
		t.Fatal("empty NZB")
	}

	// A collision: several volumes of one post staged under one file bucket,
	// every article claiming the same part number. The document can only carry
	// one, and the row must say so — this is the 1-segment-claiming-gigabytes
	// shape, in miniature.
	wreck := []stagedArticle{
		{MessageID: "<v1@x>", Subject: "vol1 (1/1) yEnc", Group: "a.b", Bytes: 700000, PartNum: 1, TotalParts: 1},
		{MessageID: "<v2@x>", Subject: "vol2 (1/1) yEnc", Group: "a.b", Bytes: 700000, PartNum: 1, TotalParts: 1},
		{MessageID: "<v3@x>", Subject: "vol3 (1/1) yEnc", Group: "a.b", Bytes: 700000, PartNum: 1, TotalParts: 1},
	}
	_, wt, err := buildNZB(wreck)
	if err != nil {
		t.Fatal(err)
	}
	if wt.Segments != 1 || wt.Bytes != 700000 {
		t.Errorf("collision totals = %d segs / %d bytes, want 1 / 700000", wt.Segments, wt.Bytes)
	}
}

// Multi-file sets dedup PER FILE: the same part number in two different files
// is two legitimate segments, not a duplicate. A totals implementation that
// deduped globally would silently undercount every multi-file release — the
// opposite error, and a quieter one.
func TestNZBTotalsDedupePerFileNotGlobally(t *testing.T) {
	arts := []stagedArticle{
		{MessageID: "<f1p1@x>", Subject: `rel [1/2] - "a.rar" yEnc (1/2)`, Group: "a.b", Bytes: 10, PartNum: 1, TotalParts: 2, SegTotal: 2, FileNum: 1, TotalFiles: 2, FileParts: true},
		{MessageID: "<f1p2@x>", Subject: `rel [1/2] - "a.rar" yEnc (2/2)`, Group: "a.b", Bytes: 10, PartNum: 2, TotalParts: 2, SegTotal: 2, FileNum: 1, TotalFiles: 2, FileParts: true},
		{MessageID: "<f2p1@x>", Subject: `rel [2/2] - "b.rar" yEnc (1/2)`, Group: "a.b", Bytes: 10, PartNum: 1, TotalParts: 2, SegTotal: 2, FileNum: 2, TotalFiles: 2, FileParts: true},
		{MessageID: "<f2p2@x>", Subject: `rel [2/2] - "b.rar" yEnc (2/2)`, Group: "a.b", Bytes: 10, PartNum: 2, TotalParts: 2, SegTotal: 2, FileNum: 2, TotalFiles: 2, FileParts: true},
	}
	_, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Segments != 4 || totals.Bytes != 40 {
		t.Errorf("totals = %d segs / %d bytes, want 4 / 40 — part numbers repeat across files legitimately",
			totals.Segments, totals.Bytes)
	}
}

// A healthy single-copy set must be unchanged by all of this: totals equal the
// old summarize/len(arts) exactly. 97.3% of the catalogue is this case, and it
// is the one the change must not disturb.
func TestNZBTotalsUnchangedForCleanSets(t *testing.T) {
	arts := []stagedArticle{
		{MessageID: "<a@x>", Subject: "rel (1/3) yEnc", Group: "a.b", Bytes: 111, PartNum: 1, TotalParts: 3},
		{MessageID: "<b@x>", Subject: "rel (2/3) yEnc", Group: "a.b", Bytes: 222, PartNum: 2, TotalParts: 3},
		{MessageID: "<c@x>", Subject: "rel (3/3) yEnc", Group: "a.b", Bytes: 333, PartNum: 3, TotalParts: 3},
	}
	_, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	oldSize, _ := summarize(arts)
	if totals.Bytes != oldSize || totals.Segments != len(arts) {
		t.Errorf("clean set changed: totals=%d/%d, old=%d/%d — the common case must be untouched",
			totals.Bytes, totals.Segments, oldSize, len(arts))
	}
}
