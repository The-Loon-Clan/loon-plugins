package usenet

import (
	"strings"
	"testing"
)

// The real subject from production release 72720154, which sat in the index
// unsearchable and structurally misparsed.
const (
	rotSubject   = `[SHYY] - HGGRE XRRX CERFRAGF  Hc.7554.275c.OyhEnl.k719.iby552+52.CNE7 93 bs 08  7377399 XO (7/0)`
	plainSubject = `[FULL] - UTTER KEEK PRESENTS  Up.2009.720p.BluRay.x264.vol007+07.PAR2 48 of 53  2822844 KB (2/5)`
)

func TestRot18DecodesARealProductionSubject(t *testing.T) {
	got, was := deobfuscateSubject(rotSubject)
	if !was {
		t.Fatal("a rotated subject was not detected")
	}
	if got != plainSubject {
		t.Errorf("decode mismatch:\n  got  %q\n  want %q", got, plainSubject)
	}
}

// The counters are the reason this runs at ingest rather than in the title
// cleaner. Rotating digits rotates them, so the crawler read segment 7 of 0 and
// file 93 of 8 — and a "total parts" of 0 means isComplete can never be
// satisfied, whatever else is right.
func TestRot18RepairsTheCountersNotJustTheTitle(t *testing.T) {
	_, _, rotTotalParts, _, _, rotTotalFiles, _ := parseSubject(rotSubject)
	decoded, _ := deobfuscateSubject(rotSubject)
	_, _, totalParts, _, _, totalFiles, _ := parseSubject(decoded)

	if rotTotalParts == totalParts && rotTotalFiles == totalFiles {
		t.Fatal("premise broken: the rotated and decoded subjects parsed identically, " +
			"so the digits were not actually rotated")
	}
	if totalParts != 5 {
		t.Errorf("decoded totalParts = %d, want 5 (the rotated form gave %d)", totalParts, rotTotalParts)
	}
	if totalFiles != 53 {
		t.Errorf("decoded totalFiles = %d, want 53 (the rotated form gave %d)", totalFiles, rotTotalFiles)
	}
}

// ROT18 is its own inverse, which is what makes an accidental second pass safe.
func TestRot18IsSelfInverse(t *testing.T) {
	for _, s := range []string{rotSubject, plainSubject, "Mixed 123 aZ!", ""} {
		if got := rot18(rot18(s)); got != s {
			t.Errorf("rot18 applied twice changed %q into %q", s, got)
		}
	}
}

// The guard that matters for the rest of the index: an ordinary subject must be
// left exactly alone. Detection is a literal marker match precisely so this
// cannot go wrong on real titles.
func TestRot18LeavesOrdinarySubjectsUntouched(t *testing.T) {
	ordinary := []string{
		`[001/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.par2" yEnc (1/1) 697512`,
		`[Erai-raws] Yofukashi no Uta - 01 [1080p][Multiple Subtitle]`,
		`Some.Show.S01E01.1080p.WEB-DL.x264-GROUP.mkv yEnc (1/45)`,
		`[FULL] - a normal poster tag that is NOT rotated`,
		`HUNTER HUNTER`,
		``,
	}
	for _, s := range ordinary {
		got, was := deobfuscateSubject(s)
		if was {
			t.Errorf("ordinary subject detected as rotated: %q", s)
		}
		if got != s {
			t.Errorf("ordinary subject was modified:\n  in  %q\n  out %q", s, got)
		}
	}
}

// Each marker independently triggers detection — a subject may carry the poster
// tag, the network attribution, or the rotated yEnc keyword, not always all three.
func TestRot18DetectsEachMarker(t *testing.T) {
	cases := map[string]string{
		"poster tag": `[SHYY] - fbzrguvat`,
		"network":    `[12345]-[#n.o.grrirr@RSArg]-[ fbzrguvat ]`,
		"yenc":       `fbzrguvat.zxi lRap (7/0)`,
	}
	for name, subject := range cases {
		if !isRot18Subject(subject) {
			t.Errorf("%s marker not detected in %q", name, subject)
		}
	}
}

// "lRap" is checked with its surrounding space so it cannot fire from the middle
// of a word — the kind of near-miss that turns a cheap literal check into a
// corruption bug.
func TestRot18YencMarkerRequiresWordBoundary(t *testing.T) {
	if isRot18Subject("SomethinglRapish.mkv") {
		t.Error("the yEnc marker fired inside a word")
	}
	if !isRot18Subject("something lRap (1/2)") {
		t.Error("the yEnc marker did not fire when properly delimited")
	}
}

// The decoded title is the point of the exercise: it has to be something a
// member could actually search for.
func TestRot18YieldsASearchableTitle(t *testing.T) {
	decoded, _ := deobfuscateSubject(rotSubject)
	for _, want := range []string{"Up.2009.720p", "BluRay", "x264", "PAR2"} {
		if !strings.Contains(decoded, want) {
			t.Errorf("decoded subject lacks %q: %s", want, decoded)
		}
	}
}
