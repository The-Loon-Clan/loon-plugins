package usenet

import "testing"

// The production case, verbatim from filter_hits: 41.6 million articles junked
// as high_special_chars because a perfectly ordinary release title arrived
// RFC 2047 encoded and nothing decoded it. The assertion that matters is not
// that the bytes decode — it is that the junk verdict flips.
func TestEncodedSubjectIsNoLongerJunk(t *testing.T) {
	const raw = `=?UTF-8?q?Gecko_Crowned_in_a_Hundred_Days_-_S01E09_=E7=99=BE=E6=97=A5?= ` +
		`=?UTF-8?q?=E6=88=90=E7=8E=8B;_Bai_Ri_Cheng_Wang_BILI.WEB-DL_1080P_HEVC?=`

	rawBase, _, _, _, _, _, _ := parseSubject(raw)
	if whichJunkRule(rawBase) != "high_special_chars" {
		t.Fatalf("fixture no longer reproduces the bug: raw junk verdict = %q",
			whichJunkRule(rawBase))
	}

	base, _, _, _, _, _, _ := parseSubject(decodeSubject(raw))
	if rule := whichJunkRule(base); rule != "" {
		t.Errorf("decoded title still junked as %q: %q", rule, base)
	}
	if !contains(base, "Gecko Crowned in a Hundred Days") {
		t.Errorf("title not recovered: %q", base)
	}
}

// A base64 encoded-word can swallow the whole subject, yEnc markers included.
// Decoding has to happen BEFORE parsing or the segment counter is invisible and
// every article of the release collides on part 1/1 — the collapse that ate
// four days of the forward crawl.
func TestBase64SubjectExposesTheSegmentCounter(t *testing.T) {
	// The whole subject, markers and all, inside one base64 encoded-word:
	// "[Erai-raws] Thunder 3 - 01 [1080p].mkv yEnc (1/45)"
	const raw = `=?UTF-8?B?W0VyYWktcmF3c10gVGh1bmRlciAzIC0gMDEgWzEwODBwXS5ta3YgeUVuYyAoMS80NSk=?=`

	if _, _, tp, _, _, _, _ := parseSubject(raw); tp != 1 {
		t.Fatalf("fixture assumes the raw form hides the counter, got total=%d", tp)
	}
	base, pn, tp, _, _, _, _ := parseSubject(decodeSubject(raw))
	if pn != 1 || tp != 45 {
		t.Errorf("segment counter = %d/%d, want 1/45 — articles would collide on one key", pn, tp)
	}
	if base != "[Erai-raws] Thunder 3 - 01 [1080p]" {
		t.Errorf("base = %q", base)
	}
}

// Stateful escape encodings must NOT be passed through: measuring showed a
// subject the junk engine accepted encoded became high_special_chars once its
// ESC bytes were spilled into the title. Keeping the raw header is strictly
// better, so decoding has to decline rather than do harm.
func TestStatefulCharsetsKeepTheRawHeader(t *testing.T) {
	for _, raw := range []string{
		`=?ISO-2022-JP?B?GyRCJDIkcyQ4JHMbKEI=?= - Episode 01.mkv yEnc (1/20)`,
		`=?UTF-7?B?SGVsbG8=?= - Episode 02.mkv yEnc (1/20)`,
	} {
		if got := decodeSubject(raw); got != raw {
			t.Errorf("passed through a stateful charset:\n raw %q\n got %q", raw, got)
		}
	}

	// ASCII-transparent ones still decode — the concession is scoped.
	const shiftJIS = `=?Shift_JIS?Q?Anime_-_01?= yEnc (1/9)`
	if got := decodeSubject(shiftJIS); got != "Anime - 01 yEnc (1/9)" {
		t.Errorf("Shift_JIS not passed through: %q", got)
	}
}

// This sits on the per-article ingest path — tens of millions of subjects a
// pass, almost none of them encoded. The untouched path must be exactly that.
func TestPlainSubjectsAreUntouched(t *testing.T) {
	for _, s := range []string{
		"[Erai-raws] Thunder 3 - 01 [1080p].mkv yEnc (1/45)",
		"",
		"Some.Release.2024.1080p.WEB-DL (1/2)",
	} {
		if got := decodeSubject(s); got != s {
			t.Errorf("decodeSubject(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Never trade a parseable raw subject for a broken decode. A truncated or
// malformed encoded-word is common in overview data, and an empty title would
// index a nameless release.
func TestBrokenEncodingFallsBackToRaw(t *testing.T) {
	for _, raw := range []string{
		`=?UTF-8?q?`,                    // truncated mid-word
		`=?UTF-8?B?!!!!not-base64!!!?=`, // undecodable body
		`=?UTF-8?B?ICAg?=`,              // decodes to whitespace only
		`=?`,
	} {
		if got := decodeSubject(raw); got != raw {
			t.Errorf("decodeSubject(%q) = %q, want the raw subject back", raw, got)
		}
	}
}
