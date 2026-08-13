package usenet

import "strings"

// ROT18-obfuscated subjects: ROT13 on letters, ROT5 on digits.
//
// A long-running poster family (the a.b.teevee / EFNet "[FULL]" tags) rotates
// the ENTIRE subject line, digits included. Found on production while looking
// at why 299 releases were unsearchable:
//
//	stored  [SHYY] - HGGRE XRRX CERFRAGF  Hc.7554.275c.OyhEnl.k719...CNE7 93 bs 08  7377399 XO (7/0)
//	decoded [FULL] - UTTER KEEK PRESENTS  Up.2009.720p.BluRay.x264...PAR2 48 of 53  2822844 KB (2/5)
//
// THE TITLE IS THE SMALL PART. Rotating digits rotates the counters, so the
// crawler read "(7/0)" as segment 7 of 0 rather than 2 of 5, "93 of 08" as file
// 93 of 8 rather than 48 of 53, and the size as 7,377,399 KB rather than
// 2,822,844. Every structural decision downstream — completeness, file grouping,
// the sized junk rules — was made on rotated numbers. That is why this is
// undone at INGEST, before parseSubject sees the string, and not in the title
// cleaner where only the display name would be repaired.
//
// ROT18 is its own inverse (13+13=26, 5+5=10), so decoding and encoding are the
// same operation and applying it twice is a no-op. That is what makes an
// accidental double-decode harmless, and it is also why detection has to be a
// positive signal rather than "does this look like English".

// rot18 rotates letters by 13 and digits by 5. Self-inverse.
func rot18(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		case r >= '0' && r <= '9':
			return '0' + (r-'0'+5)%10
		}
		return r
	}, s)
}

// rot18Markers are strings that only appear in a rotated subject.
//
// A LITERAL MATCH, not a heuristic, and that is the whole design. The tempting
// approach — rotate everything and keep whichever version "looks more like a
// release" — needs a scoring function, runs on every subject the crawler sees,
// and is wrong in both directions: it mangles a legitimate title that happens to
// score badly, and it misses a rotated one whose decoded form scores no better.
// Matching a fixed token that cannot occur by accident costs a substring scan
// and cannot produce a false positive on real text.
//
// The obvious cheap general test does NOT work here, and it is worth recording
// why so nobody re-derives it. "Real binary subjects contain yEnc; a rotated one
// will not; so rotate anything lacking yEnc and see if yEnc appears" is one
// substring scan on the common path and beautifully general — except the poster
// family this was found on emits no yEnc marker in EITHER form. The sample that
// started this decodes to "...PAR2 48 of 53  2822844 KB (2/5)": no yEnc before,
// none after. A detector built on it would have missed all 299 rows.
//
// So the list is instead a set of rotated forms of tokens that occur in almost
// every release subject. Each is long enough that it cannot appear by chance in
// English or in a real title, and the whole check is N substring scans of a
// ~100-character string.
//
//	[SHYY]  = [FULL]    the poster tag
//	@RSArg  = @EFNet    the attribution block
//	lRap    = yEnc      when the encoding marker is rotated too
//	CNE7    = PAR2      recovery volumes; on nearly every large post
//	OyhEnl  = BluRay
//	ERZHK   = REMUX
//	.zxi    = .mkv
//	k719    = x264
//	k710    = x265
//	UQGI    = HDTV
//	JRO-QY  = WEB-DL
//	QIQEvc  = DVDRip
//
// Deliberately omitted: short rotations like "iby" (vol) and ".ene" (.rar),
// which are three or four characters and could plausibly fall inside a real
// title. The cost of a miss here is one release staying obfuscated; the cost of
// a false positive is a correct title rotated into gibberish, so the list only
// takes tokens where that trade is obviously safe.
var rot18Markers = []string{
	"[SHYY]", "@RSArg", " lRap", "lRap ",
	"CNE7", "OyhEnl", "ERZHK", ".zxi", "k719", "k710", "UQGI", "JRO-QY", "QIQEvc",
}

// isRot18Subject reports whether a subject is ROT18-obfuscated.
func isRot18Subject(subject string) bool {
	for _, m := range rot18Markers {
		if strings.Contains(subject, m) {
			return true
		}
	}
	return false
}

// deobfuscateSubject returns the subject with ROT18 undone, and whether it was.
//
// The caller keeps the second return value: a release recovered this way should
// be MARKED as having had an obfuscated subject, because "we decoded this" and
// "the poster titled it plainly" are different facts and only one of them should
// be trusted when a later pass disagrees about the title.
func deobfuscateSubject(subject string) (string, bool) {
	if !isRot18Subject(subject) {
		return subject, false
	}
	return rot18(subject), true
}
