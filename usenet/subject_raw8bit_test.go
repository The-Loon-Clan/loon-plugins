package usenet

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A raw 8-bit header carries the poster's own bytes. Left alone they reach
// pgSafeText, which can only replace them with U+FFFD to make them storable --
// and that is destruction: the byte leaves the database and only a re-crawl
// could recover it. Measured on production 2026-08-26: 2,834 of 1,143,872
// titles carried U+FFFD and it was still accruing.
func TestDecodeSubjectRecoversRaw8Bit(t *testing.T) {
	cases := []struct {
		name string
		raw  string // the poster's bytes, as CP1252
		want string
	}{
		// The German audiobook groups, where most of production's damage is.
		{"u-umlaut", "H\xf6rbuch - Die Hexe vom H\xf6llental", "Hörbuch - Die Hexe vom Höllental"},
		{"u-umlaut 2", "Leben, Larry und das Streben nach Ungl\xfcck", "Leben, Larry und das Streben nach Unglück"},
		// 0x96 is an en dash in CP1252 and a CONTROL character in Latin-1,
		// which is why the fallback is CP1252.
		{"en dash", "Australian Photography \x96 August-September 2026", "Australian Photography – August-September 2026"},
		{"n-tilde", "Espa\xf1a", "España"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if utf8.ValidString(tc.raw) {
				t.Fatal("precondition: the fixture must be invalid UTF-8")
			}
			got := decodeSubject(tc.raw)
			if got != tc.want {
				t.Errorf("decodeSubject = %q, want %q", got, tc.want)
			}
			if strings.ContainsRune(got, '\uFFFD') {
				t.Errorf("result still carries U+FFFD: %q", got)
			}
			// And it must now survive the bind unchanged.
			if pgSafeText(got) != got {
				t.Errorf("pgSafeText still alters it: %q", pgSafeText(got))
			}
		})
	}
}

// THE TRAP. passthroughCharset hands Shift_JIS and GBK encoded-words back as
// invalid UTF-8 deliberately. Read as CP1252 they would become mojibake --
// worse than the replacement character, because it looks like text. They carry
// "=?" so they leave through the encoded-word branch and must never reach the
// fallback.
func TestRaw8BitFallbackLeavesEncodedWordsAlone(t *testing.T) {
	// A Shift_JIS encoded-word: passthroughCharset returns the raw bytes.
	sjis := "=?Shift_JIS?B?g0mDgoNYg16D6oNY?= - 01"
	got := decodeSubject(sjis)
	if strings.Contains(got, "ï¾") || strings.Contains(got, "Ã") {
		t.Errorf("a passthrough charset was CP1252-decoded into mojibake: %q", got)
	}

	// An honest UTF-8 encoded-word still decodes normally.
	if got := decodeSubject("=?UTF-8?Q?Espa=C3=B1a?="); got != "España" {
		t.Errorf("UTF-8 encoded-word = %q, want España", got)
	}
	// An honest ISO-8859-1 encoded-word still decodes normally.
	if got := decodeSubject("=?ISO-8859-1?Q?Espa=F1a?="); got != "España" {
		t.Errorf("ISO-8859-1 encoded-word = %q, want España", got)
	}
}

// A subject that is already valid UTF-8 must never be rewritten -- the
// fallback is only reachable when the bytes cannot be read as UTF-8.
func TestRaw8BitFallbackLeavesValidUTF8Alone(t *testing.T) {
	for _, s := range []string{
		"[SubsPlease] Frieren - 01 [1080p]",
		"進撃の巨人 S04E28",
		"Espa\u00f1a - already correct",
		"",
	} {
		if got := decodeSubject(s); got != s {
			t.Errorf("decodeSubject(%q) = %q, want it unchanged", s, got)
		}
	}
}
