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

// Every marker must round-trip to the token it claims to be. A typo in the list
// is a marker that never fires, which is silent — the release simply stays
// obfuscated and nothing reports it.
func TestRot18MarkersDecodeToTheirClaimedTokens(t *testing.T) {
	want := map[string]string{
		"[SHYY]": "[FULL]", "@RSArg": "@EFNet", " lRap": " yEnc", "lRap ": "yEnc ",
		"CNE7": "PAR2", "OyhEnl": "BluRay", "ERZHK": "REMUX", ".zxi": ".mkv",
		"k719": "x264", "k710": "x265", "UQGI": "HDTV", "JRO-QY": "WEB-DL",
		"QIQEvc": "DVDRip",
	}
	if len(want) != len(rot18Markers) {
		t.Fatalf("the marker list has %d entries but this test knows %d — "+
			"a new marker needs its expected decoding here", len(rot18Markers), len(want))
	}
	for _, m := range rot18Markers {
		exp, ok := want[m]
		if !ok {
			t.Errorf("marker %q has no expected decoding in this test", m)
			continue
		}
		if got := rot18(m); got != exp {
			t.Errorf("marker %q decodes to %q, but it is listed as meaning %q", m, got, exp)
		}
	}
}

// The expensive half of the trade: none of the markers may appear in a real
// title. A false positive rotates a correct title into gibberish, which is
// strictly worse than leaving an obfuscated one alone.
func TestRot18MarkersDoNotFireOnRealTitles(t *testing.T) {
	real := []string{
		`[001/732] - "Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy.part001.rar" yEnc (62/100) 104857600`,
		`[Erai-raws] Yofukashi no Uta - 01 [1080p][Multiple Subtitle]`,
		`Some.Show.S01E01.2160p.WEB-DL.x265.HDR-GROUP.mkv yEnc (1/45)`,
		`The.Movie.2019.1080p.BluRay.x264-SPARKS.mkv`,
		`Old.Film.1954.DVDRip.XviD-CLASSIC.avi`,
		`Doc.Series.S01.HDTV.x264 [PAR2 recovery] yEnc`,
		`SPYxFAMILY - 01 [1080p]`,
		`Godfathers Godfathers`,
		`[89759]-[FULL]-[#a.b.teevee@EFNet]-[ Stargate.Universe.S01E01 ]`,
	}
	for _, s := range real {
		if isRot18Subject(s) {
			t.Errorf("FALSE POSITIVE — a real title would be rotated into gibberish: %q", s)
		}
	}
}

// And the widened list must still catch subjects the original three markers
// would have missed: a rotated post carrying no poster tag at all.
func TestRot18CatchesSubjectsWithoutThePosterTag(t *testing.T) {
	for _, plain := range []string{
		`Some.Movie.2019.1080p.BluRay.x264-GRP.mkv (1/50)`,
		`Another.Show.S01E02.HDTV.x265.mkv (3/9)`,
		`Old.Thing.1999.DVDRip.avi vol012+11.PAR2 (1/4)`,
	} {
		rotated := rot18(plain)
		if isRot18Subject(plain) {
			t.Errorf("the plain form was flagged as rotated: %q", plain)
		}
		if !isRot18Subject(rotated) {
			t.Errorf("a rotated subject with no poster tag was missed:\n  plain   %q\n  rotated %q", plain, rotated)
		}
		if back, _ := deobfuscateSubject(rotated); back != plain {
			t.Errorf("round trip failed:\n  got  %q\n  want %q", back, plain)
		}
	}
}
