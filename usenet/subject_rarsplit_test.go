package usenet

import "testing"

// The rar-volume split, explained and pinned.
//
// docs/BACKLOG.md in loon-demo-site recorded this as unexplained: whole rar
// volumes of one release each became their own one-file "release", each holding
// a SINGLE segment while claiming the whole volume's size. New splits stopped
// after the par fix and 419 historical rows were deleted, but the mechanism was
// never established, so a recurrence had nothing to start from.
//
// It is the collision already documented in parseSubject, reached by a second
// route. A multi-volume post with no [i/j] file counter looks like this:
//
//	"Release.Name-GRP.part001.rar" yEnc (81/137)
//	"Release.Name-GRP.part002.rar" yEnc (81/137)
//
// Every volume shares one base subject, and with no file counter fileNum stays
// 0 for all of them. The staging field key is formatFieldKey(fileNum, partNum),
// so volume 1's segment 81 and volume 2's segment 81 are both "0:81". Forty-two
// volumes numbering 1..137 compete for the same 137 fields, each overwriting the
// last, and what assembles is one segment per key claiming a whole volume's
// size — which is exactly the shape of the junk rows.
//
// These tests do not FIX that. They pin the two facts a fix would have to
// change, so the next person starts from evidence rather than from a paragraph:
// the base subject is shared, and the field key collides.
func TestRarVolumesShareOneStagingKey(t *testing.T) {
	const seg = 81
	subjects := []string{
		`"Ratatouille.2007.1080p.DVDR-COX.part001.rar" yEnc (81/137)`,
		`"Ratatouille.2007.1080p.DVDR-COX.part002.rar" yEnc (81/137)`,
		`"Ratatouille.2007.1080p.DVDR-COX.part042.rar" yEnc (81/137)`,
	}

	bases := map[string]bool{}
	keys := map[string]int{}
	for _, s := range subjects {
		base, pn, _, _, fn, _, _ := parseSubject(s)
		bases[base] = true
		keys[formatFieldKey(fn, pn)]++
		if pn != seg {
			t.Errorf("parseSubject(%q) segment = %d, want %d", s, pn, seg)
		}
	}

	// One base across every volume: correct, and half the reason they collide.
	// The volume number is part of the FILENAME, not of the release, so a
	// parser that kept it apart would split one release into 42.
	if len(bases) != 1 {
		t.Fatalf("volumes produced %d base subjects, want 1: %v", len(bases), bases)
	}

	// The other half. If this ever reports 3 distinct keys, somebody has taught
	// the parser to read .partNNN as a file counter and this bug is fixed --
	// update the test rather than deleting it.
	if len(keys) != 1 {
		t.Fatalf("volumes produced %d field keys, want 1 (the collision): %v", len(keys), keys)
	}
	for k, n := range keys {
		if n != len(subjects) {
			t.Fatalf("field key %q collected %d of %d volumes", k, n, len(subjects))
		}
		t.Logf("all %d volumes stage under one field key %q -- %d-1 articles are overwritten",
			len(subjects), k, len(subjects))
	}
}

// The par fix did not address this, and the distinction matters: it changed
// which subjects are SPLIT APART, not which ones collide. A .partNN.par2 volume
// still parses to the same base and the same fileNum 0 as its .rar siblings.
//
// So new splits stopping after the par fix is consistent with the trigger going
// away rather than the mechanism being repaired, which is why this is pinned.
func TestParVolumesCollideTheSameWay(t *testing.T) {
	a, apn, _, _, afn, _, _ := parseSubject(`"Some.Release-GRP.part01.rar" yEnc (5/60)`)
	b, bpn, _, _, bfn, _, _ := parseSubject(`"Some.Release-GRP.part02.rar" yEnc (5/60)`)

	if a != b {
		t.Fatalf("rar volumes parsed to different bases: %q vs %q", a, b)
	}
	if formatFieldKey(afn, apn) != formatFieldKey(bfn, bpn) {
		t.Fatalf("rar volumes no longer collide (%q vs %q) -- the bug may be fixed; update this test",
			formatFieldKey(afn, apn), formatFieldKey(bfn, bpn))
	}
}

// The real thing, from production staging — and it is NOT what the paragraph
// above guessed.
//
// The backlog's hypothesis was that multi-volume posts carry no file counter at
// all, so fileNum stays 0 and volumes collide. That happens. But the posts
// actually doing the damage carry TWO counters:
//
//	"BB520.part001.rar" - (001/225) - yEnc (100/391)
//	                      ^^^^^^^^^         ^^^^^^^^
//	                      file 1 of 225     segment 100 of 391
//
// parseSubject prefers the counter BEFORE yEnc, on the documented reasoning
// that "anything after the keyword is a file-count indicator rather than a
// segment counter". In THIS shape that is exactly inverted: the counter before
// yEnc is the file counter and the one after it is the segment counter.
//
// So every one of the 391 segments of volume 001 parses as part 1 of 225 and
// stages under the same key. Measured against 7.2M staged articles: 30,646
// articles in this shape across 103 posts collapse onto 532 keys — 98.3%
// overwritten. What survives is one segment per volume claiming the whole
// volume's size, which is precisely the junk row the backlog described.
//
// These assertions pin the WRONG behaviour deliberately, because a failing test
// in a shared repository is somebody else's broken build. When the parser is
// fixed they will fail with a message saying so, and that is the signal to
// rewrite them as the correct expectation.
func TestTwoCounterSubjectReadsTheFileCounterAsSegments(t *testing.T) {
	const subject = `"BB520.part001.rar" - (001/225) - yEnc (100/391)`
	base, pn, tp, _, fn, tf, _ := parseSubject(subject)

	if base != "BB520" {
		t.Errorf("base = %q, want BB520 (the base is right; the counters are not)", base)
	}
	// What it SHOULD be: pn=100, tp=391 (the yEnc counter), fn=1, tf=225.
	if pn == 100 && tp == 391 {
		t.Fatalf("the segment counter is now read correctly (%d/%d) — this bug is FIXED; "+
			"rewrite this test as the correct expectation", pn, tp)
	}
	if pn != 1 || tp != 225 {
		t.Errorf("segment parsed as %d/%d; the bug reads the FILE counter (1/225)", pn, tp)
	}
	if fn != 0 || tf != 0 {
		t.Errorf("file parsed as %d/%d; the bug leaves it unset (0/0)", fn, tf)
	}

	// The consequence: two different segments of one volume share a key.
	_, pnA, _, _, fnA, _, _ := parseSubject(`"BB520.part001.rar" - (001/225) - yEnc (100/391)`)
	_, pnB, _, _, fnB, _, _ := parseSubject(`"BB520.part001.rar" - (001/225) - yEnc (250/391)`)
	if formatFieldKey(fnA, pnA) != formatFieldKey(fnB, pnB) {
		t.Fatalf("segments 100 and 250 no longer collide — the bug is FIXED; update this test")
	}
	t.Logf("segments 100 and 250 of one volume both stage under %q", formatFieldKey(fnA, pnA))
}
