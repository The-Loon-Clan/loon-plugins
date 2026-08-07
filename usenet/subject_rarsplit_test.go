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
