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
//
// Scope note (8 Aug 2026): the TWO-counter subfamilies of the rar split are
// fixed — see the swap tests at the bottom of this file. THIS form, a single
// counter with the volume number only in the filename, still collides exactly
// as pinned here: there is no file counter in the subject to read, and
// deriving one from ".partNN" is the separately-measured risk the parser's
// reArchivePart comment documents. The next two tests remain the pin, not a
// fossil.
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

// The two-counter form, from production staging — and it is NOT what the
// paragraph above guessed.
//
// The backlog's hypothesis was that multi-volume posts carry no file counter at
// all, so fileNum stays 0 and volumes collide. That happens (the tests above
// pin it, and it is STILL unfixed). But the posts that did the measured damage
// carry TWO counters:
//
//	"BB520.part001.rar" - (001/225) - yEnc (100/391)
//	                      ^^^^^^^^^         ^^^^^^^^
//	                      file 1 of 225     segment 100 of 391
//
// parseSubject used to prefer the counter BEFORE yEnc, reasoning that
// "anything after the keyword is a file-count indicator rather than a segment
// counter" — a shape that occurs in zero of the 288 two-counter posts in a
// 7.6M-article index. Under that reading every segment of volume 001 parsed
// as part 1 of 225 and staged under one key: 98.1% of these articles
// overwrote each other, and what survived was one segment per volume claiming
// the whole volume's size — the junk row the backlog described.
//
// Fixed 8 Aug 2026 (loon-demo-site docs/SUBJECT-PARSING-REVIEW.md holds the
// full validation: zero base changes and zero introduced collisions across
// 6.99M distinct subjects). These assertions are the correct expectation the
// old pinned-wrong version told its finder to write.
func TestTwoCounterSubjectSwapsFileAndSegmentCounters(t *testing.T) {
	const subject = `"BB520.part001.rar" - (001/225) - yEnc (100/391)`
	base, pn, tp, seg, fn, tf, fp := parseSubject(subject)

	// The base must stay on the stripAllMarkers route. The [i/j] release-name
	// rule would take the text before the counter and produce
	// `"BB520.part001.rar` — quotes, volume number and extension intact
	// (cleanBase does not trim quotes) — splitting every volume into its own
	// release and re-keying live staging.
	if base != "BB520" {
		t.Errorf("base = %q, want BB520 (must stay on the stripAllMarkers route)", base)
	}
	if pn != 100 || tp != 391 || seg != 391 {
		t.Errorf("segment = %d/%d seg=%d, want 100/391 seg=391 (the counter AFTER yEnc)", pn, tp, seg)
	}
	if fn != 1 || tf != 225 || !fp {
		t.Errorf("file = %d/%d fp=%v, want 1/225 fp=true (the counter BEFORE yEnc)", fn, tf, fp)
	}

	// The healed consequence, both axes: two segments of one volume get their
	// own keys, and the same segment index of two volumes does too.
	_, pnA, _, _, fnA, _, _ := parseSubject(`"BB520.part001.rar" - (001/225) - yEnc (100/391)`)
	_, pnB, _, _, fnB, _, _ := parseSubject(`"BB520.part001.rar" - (001/225) - yEnc (250/391)`)
	_, pnC, _, _, fnC, _, _ := parseSubject(`"BB520.part002.rar" - (002/225) - yEnc (100/391)`)
	if formatFieldKey(fnA, pnA) == formatFieldKey(fnB, pnB) {
		t.Errorf("segments 100 and 250 of volume 001 still share key %q", formatFieldKey(fnA, pnA))
	}
	if formatFieldKey(fnA, pnA) == formatFieldKey(fnC, pnC) {
		t.Errorf("segment 100 of volumes 001 and 002 still share key %q", formatFieldKey(fnA, pnA))
	}
}

// The no-yEnc variant of the same bug — the one that actually produced the
// [Superboys.of.Malegaon.2025] fragment releases the evidence doc used as its
// headline (it misattributed them to the yEnc form; the download test that
// exposed that is in SUBJECT-PARSING-REVIEW.md). No yEnc anywhere, two
// counters: the first is the file counter, the last the segment counter.
//
// Before the fix the segment counter was already read correctly (the last
// (a/b) wins when there is no yEnc to scope by) but the file counter went
// unread — all 23 files of the post fought for the "0:s" key space, and what
// assembled was an interleaved mosaic. Two of six such fragments passed a real
// newsreader's post-processing as "Completed" because their surviving keys
// held only whole par2 files: no data, nothing to verify, status green.
func TestNoYencTwoCounterSubjectReadsBothCounters(t *testing.T) {
	const subject = `[Superboys.of.Malegaon.2025] (06/23) - "Superboys.of.Malegaon.2025.1080p.AMZN.WEB-DL.DDP5.1..Atmos.H.264-WADU.part04.rar" (0683/1621)`
	base, pn, tp, _, fn, tf, fp := parseSubject(subject)

	if fn != 6 || tf != 23 || !fp {
		t.Errorf("file = %d/%d fp=%v, want 6/23 fp=true (the FIRST counter)", fn, tf, fp)
	}
	if pn != 683 || tp != 1621 {
		t.Errorf("segment = %d/%d, want 683/1621 (the LAST counter, unchanged from before the fix)", pn, tp)
	}
	// Byte-identical to the pre-fix base, so the post's staged set and its
	// junk-rule memoisation survive the change.
	const wantBase = "[Superboys.of.Malegaon.2025] - Superboys.of.Malegaon.2025.1080p.AMZN.WEB-DL.DDP5.1..Atmos.H.264-WADU"
	if base != wantBase {
		t.Errorf("base = %q, want %q", base, wantBase)
	}

	// A single counter without yEnc must NOT grow a file counter — that is
	// the Ratatouille shape pinned above, deliberately untouched.
	_, _, _, _, fn2, tf2, fp2 := parseSubject(`"Some.Release-GRP.part01.rar" (5/60)`)
	if fp2 || fn2 != 0 || tf2 != 0 {
		t.Errorf("single no-yEnc counter grew a file counter (%d/%d fp=%v); the ≥2-counter guard is gone", fn2, tf2, fp2)
	}
}

// A THIRD route into the rar split, and the one that is live on production
// today — reported from the demo index 26 Aug 2026, where 56 indexed
// "releases" were really 4 posts and the flagship /series page offered "31
// copies" of one episode, every one a 10 MB fragment of the same rip.
//
// This is the INVERSE of the collision pinned above. There, 42 volumes share
// one key and overwrite each other; here they share no key at all and each
// ships as its own complete, downloadable one-volume release.
//
// The mechanism is the trailing \b in reArchivePart. Some posters weld their
// collection tag straight onto the archive name with no separator:
//
//	Army Wives S06disc 5.part109.rararmys06disc5nlsubs
//	Greys Anatomy S09E02 …-DIMENSION NLSUBS.part011.rargrey
//
// "rar" followed by "a" is not a word boundary, so the volume suffix never
// comes off and every volume derives its own base — the exact failure the
// reArchivePart comment says the rule exists to prevent, reached through the
// one shape its anchor cannot see. Measured on production 26 Aug 2026: 637
// rows across 39 sets, newest four days old, so this is live and not
// historical. None of the 39 carry an anime_id, which is why it has gone
// unnoticed on an anime site.
func TestConcatenatedTagDefeatsTheVolumeStrip(t *testing.T) {
	// Same poster, same release, one space apart. The spaced form is handled
	// correctly, which is what makes the boundary — not the mid-string match —
	// the thing that fails.
	spaced, _, _, _, _, _, _ := parseSubject(
		`Army Wives S06disc 5.part109.rar - armys06disc5nlsubs - yEnc (1/15)`)
	if spaced != "Army Wives S06disc 5 - armys06disc5nlsubs" {
		t.Fatalf("spaced form = %q, want the volume suffix stripped", spaced)
	}

	// The welded form. If these two ever compare EQUAL, somebody has dropped
	// the \b and this half of the bug is fixed — read the next test before
	// celebrating, because the other half is what makes that dangerous.
	a, _, _, _, _, _, _ := parseSubject(`Army Wives S06disc 5.part109.rararmys06disc5nlsubs yEnc (1/15)`)
	b, _, _, _, _, _, _ := parseSubject(`Army Wives S06disc 5.part110.rararmys06disc5nlsubs yEnc (1/15)`)
	if a == b {
		t.Fatalf("welded volumes now share base %q — the \b was dropped; see TestDroppingTheBoundaryAloneBuildsAMosaic", a)
	}
	if a != "Army Wives S06disc 5.part109.rararmys06disc5nlsubs" {
		t.Errorf("base = %q, want the volume suffix retained (the bug)", a)
	}
}

// The trap, and the reason the one-character fix above must NOT be made on its
// own. Measured rather than argued: dropping the \b was tried, and this is what
// it produced.
//
// These posts carry a single counter — the SEGMENT counter — and no file
// counter anywhere, so fileNum stays 0 for every volume. Collapsing the bases
// therefore lands all 120 volumes in one staged set where volume 109's segment
// 1 and volume 110's segment 1 are both "0:1". Completeness is then satisfied
// the moment 15 distinct part numbers exist (COUNT(DISTINCT part_num) >=
// MAX(total_parts), and total_parts is 15 PER VOLUME), so the set assembles
// from the first volume that lands and what ships is one segment per key
// claiming a whole volume's size.
//
// That trades 120 honest fragments for one dishonest release: the member who
// was offered 31 obvious 10 MB pieces is instead offered a single 1.2 GB NZB
// that is an interleaved mosaic. SUBJECT-PARSING-REVIEW.md records two such
// mosaics passing a real newsreader's post-processing as "Completed" — no
// data, nothing to verify, status green. A visible wrong answer became an
// invisible one.
//
// So the fix is base + file key + completeness TOGETHER, not the regex alone:
// derive the file number from ".partNNN" (the risk reArchivePart's own comment
// measures), and give the file-parts completeness rule a total_files > 0 guard,
// because MAX(total_files) = 0 makes "COUNT(DISTINCT file_num) >= 0" true for
// the first article through the door.
func TestDroppingTheBoundaryAloneBuildsAMosaic(t *testing.T) {
	// Pre-stripped stand-ins for what the volumes look like once the suffix
	// comes off — i.e. exactly what dropping the \b produces.
	_, pnA, _, _, fnA, _, fpA := parseSubject(`Army Wives S06disc 5 armys06disc5nlsubs yEnc (1/15)`)
	_, pnB, _, _, fnB, _, fpB := parseSubject(`Army Wives S06disc 5 armys06disc5nlsubs yEnc (1/15)`)

	if fpA || fpB || fnA != 0 || fnB != 0 {
		t.Fatalf("a file counter appeared (fp=%v/%v fn=%d/%d) — if .partNNN now feeds fileNum, "+
			"the mosaic risk is gone and this test should assert the new keys instead", fpA, fpB, fnA, fnB)
	}
	if formatFieldKey(fnA, pnA) != formatFieldKey(fnB, pnB) {
		t.Fatalf("volumes no longer share a field key — the completeness half may be fixed; update this test")
	}
	t.Logf("both volumes stage under %q with no file counter to separate them: "+
		"collapsing the base alone merges them destructively", formatFieldKey(fnA, pnA))
}
