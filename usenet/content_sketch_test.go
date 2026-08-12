package usenet

import (
	"fmt"
	"testing"
)

// sketchOf runs the REAL assembly path and returns the sketch the sink would
// receive. Going through buildNZB rather than calling contentSketchDoc directly
// is the point: the sketch is defined over the assembled document, and a test
// that skipped assembly could not catch a divergence introduced there.
func sketchOf(t *testing.T, arts []stagedArticle) string {
	t.Helper()
	_, totals, err := buildNZB(arts)
	if err != nil {
		t.Fatalf("buildNZB: %v", err)
	}
	return totals.Sketch
}

// arts builds a single-file staged set with synthetic message-ids. PartNum is
// distinct per article, which is what makeFile keys its dedup on.
func arts(n int, prefix string) []stagedArticle {
	out := make([]stagedArticle, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, stagedArticle{
			MessageID: fmt.Sprintf("<%s-%d@news>", prefix, i),
			PartNum:   i + 1,
			Bytes:     100,
		})
	}
	return out
}

// drop removes the articles at the given indexes, modelling one crawl seeing
// slightly less of a posting than another.
func drop(in []stagedArticle, idx ...int) []stagedArticle {
	skip := make(map[int]bool, len(idx))
	for _, i := range idx {
		skip[i] = true
	}
	out := make([]stagedArticle, 0, len(in))
	for i, a := range in {
		if !skip[i] {
			out = append(out, a)
		}
	}
	return out
}

// The bug this key exists for, in the shape production produced it.
//
// "Call Of The Night (2022) S02 ... AVC-iVy" is ONE posting crossposted to
// alt.binaries.teevee and alt.binaries.mom. Each group was crawled separately
// and saw a slightly different subset — 79,081 articles against 79,082, with
// 100% of message-ids shared. content_hash covers every observed id, so one
// article's difference gave two hashes and both copies were indexed. Across
// that release's 12 files the index held 57 rows with 57 distinct hashes.
func TestContentSketchSurvivesPartialView(t *testing.T) {
	full := arts(79082, "iVy")
	teevee := drop(full, 4231) // one article this crawl did not see

	if contentHashArticles(teevee) == contentHashArticles(full) {
		t.Fatal("content_hash collided on differing article sets — it is defined over ALL ids, " +
			"so this test no longer models the bug")
	}
	if got, want := sketchOf(t, teevee), sketchOf(t, full); got != want {
		t.Errorf("sketch differs across two views of one posting: %s vs %s\n"+
			"this is the crosspost duplicate — one dropped article out of 79,082 must not change it", got, want)
	}
}

// Tolerance is (1 - D/N)^K, so a handful of missing articles is survivable and a
// large fraction is not. 148 of 79,082 is the real figure for the third copy of
// that release: D/N is 0.19%, giving ~97% survival.
func TestContentSketchToleratesManyMissing(t *testing.T) {
	full := arts(79082, "iVy")
	idx := make([]int, 0, 148)
	for i := 0; i < 148; i++ {
		idx = append(idx, 500+i*37)
	}
	partial := drop(full, idx...)

	if sketchOf(t, partial) != sketchOf(t, full) {
		t.Errorf("sketch changed after dropping 148 of 79,082 articles (D/N = 0.19%%). " +
			"That is inside the documented (1-D/N)^K tolerance, so this is a real failure, " +
			"not the expected tail — check sketchK and the digest derivation")
	}
}

// The other half of the contract: a genuine RE-POST is a different posting and
// must stay indexable. Same title, same file count, fresh message-ids — like
// the misc copy of that release, which shared 0 of 79,033 ids with the others.
func TestContentSketchDistinguishesRepost(t *testing.T) {
	if sketchOf(t, arts(79081, "iVy-run1")) == sketchOf(t, arts(79033, "iVy-run2")) {
		t.Error("a re-post with entirely fresh message-ids sketched the same as the original — " +
			"it would be suppressed as a duplicate, and it is a separate posting")
	}
}

// Identical article sets must always agree, because that is what lets the
// sketch REPLACE content_hash as the dedup key rather than merely supplement
// it. Order must not matter: staging returns articles in no guaranteed order.
func TestContentSketchIsOrderIndependentAndStable(t *testing.T) {
	a := arts(5000, "x")
	b := make([]stagedArticle, len(a))
	for i := range a {
		b[len(a)-1-i] = a[i]
	}
	if sketchOf(t, a) != sketchOf(t, b) {
		t.Error("sketch depends on article order — staging does not guarantee one")
	}
}

// The sketch is defined over the DOCUMENT, and this is the case that forces it.
//
// makeFile deduplicates by PART NUMBER, keeping the first article seen for each
// part. A staged set holding two articles for one part with different
// message-ids therefore writes only ONE of them into the NZB. A sketch computed
// over the staged articles would include the id that never reached the
// document, so the ingest path and a host backfilling from the stored file
// would disagree about the same release — and disagreeing is indistinguishable
// from "not a duplicate", so the dedup would quietly stop working across that
// boundary while every test using clean fixtures still passed.
func TestContentSketchIgnoresArticlesTheDocumentDrops(t *testing.T) {
	base := arts(40, "dup")
	// A second article claiming an already-seen part, with its own message-id.
	// makeFile keeps the first and silently drops this one.
	shadowed := append(append([]stagedArticle{}, base...), stagedArticle{
		MessageID: "<shadow-not-in-document@news>",
		PartNum:   base[7].PartNum,
		Bytes:     100,
	})

	doc, totals, err := buildNZB(shadowed)
	if err != nil {
		t.Fatalf("buildNZB: %v", err)
	}
	if totals.Segments != 40 {
		t.Fatalf("document holds %d segments, want 40 — the fixture no longer models "+
			"makeFile's part dedup", totals.Segments)
	}
	if got, want := totals.Sketch, sketchOf(t, base); got != want {
		t.Errorf("sketch %s changed because of an article the document does not contain (want %s).\n"+
			"It must be computed from the assembled document, or a backfill reading the stored "+
			"NZB can never reproduce it", got, want)
	}
	_ = doc
}

// A repeated message-id must not occupy two of the K slots, so a crawl that saw
// an article twice agrees with one that saw it once.
func TestContentSketchIgnoresDuplicateMessageIDs(t *testing.T) {
	a := arts(1000, "d")
	// Same id AND same part: the document keeps one copy either way, but the
	// sketch must not depend on that coincidence.
	withDupes := append(append([]stagedArticle{}, a...), a[0], a[1], a[2])
	if sketchOf(t, withDupes) != sketchOf(t, a) {
		t.Error("a repeated message-id changed the sketch")
	}
}

// Angle brackets are stripped when the document is written, so the sketch —
// being defined over the document — is invariant to how staging held them. A
// host recomputing from the stored file only ever sees the trimmed form.
func TestContentSketchIsInvariantToAngleBrackets(t *testing.T) {
	bracketed := []stagedArticle{
		{MessageID: "<part1@news.example>", PartNum: 1, Bytes: 10},
		{MessageID: "<part2@news.example>", PartNum: 2, Bytes: 10},
	}
	bare := []stagedArticle{
		{MessageID: "part1@news.example", PartNum: 1, Bytes: 10},
		{MessageID: "part2@news.example", PartNum: 2, Bytes: 10},
	}
	if got, want := sketchOf(t, bracketed), sketchOf(t, bare); got != want {
		t.Errorf("sketch depends on angle brackets: %s vs %s\n"+
			"the NZB stores the trimmed form, so a backfill can only ever see that one", got, want)
	}
}

// Below K articles the sketch degenerates to the whole set — today's exact
// behaviour. Worth pinning so nobody reads the tolerance as unconditional.
func TestContentSketchSmallSetsAreExact(t *testing.T) {
	full := arts(8, "s")
	if sketchOf(t, drop(full, 3)) == sketchOf(t, full) {
		t.Error("an 8-article set tolerated a missing article; below sketchK the sketch is the whole " +
			"set and must be exact, which is what makes it no worse than content_hash for small posts")
	}
}

// No usable ids means no identity — "" must not become a value that every such
// release collides on. The unique index excludes it for the same reason.
func TestContentSketchEmptyWithoutMessageIDs(t *testing.T) {
	if got := contentSketchDoc(nzbDoc{}); got != "" {
		t.Errorf("sketch of an empty document = %q, want empty", got)
	}
	got := contentSketchDoc(nzbDoc{Files: []nzbFile{{
		Segments: nzbSegments{Segment: []nzbSegment{{Number: 1, Value: ""}}},
	}}})
	if got != "" {
		t.Errorf("sketch of id-less segments = %q, want empty", got)
	}
}
