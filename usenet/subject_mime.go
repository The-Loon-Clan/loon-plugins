package usenet

import (
	"fmt"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// RFC 2047 encoded-words in overview subjects.
//
// A subject is a raw header value, and posters outside pure ASCII encode it:
//
//	=?UTF-8?q?Gecko_Crowned_in_a_Hundred_Days_-_S01E09_=E7=99=BE=E6=97=A5?=
//
// Undecoded, that is unparseable as a release name and — worse — reads as
// punctuation soup to the junk engine, which dropped it as high_special_chars
// at ingest. Production had junked 41.6 million articles that way: entire
// non-English posters silently absent from the catalogue, with the only trace a
// junk counter that looked like it was doing its job.
//
// Decoding here, at the single point raw wire subjects enter the pipeline,
// means everything downstream — junk rules, base grouping, tags, the blacklist,
// the NZB filename — sees the title the poster actually wrote.

var subjectDecoder = mime.WordDecoder{CharsetReader: passthroughCharset}

// passthroughCharset accepts charsets Go has no table for (Shift_JIS, GBK,
// Big5) by handing the bytes back unconverted, rather than failing the header.
//
// Those are all ASCII-transparent, so the Latin half of such a title — usually
// the searchable half, and always the episode numbering and the yEnc markers —
// survives intact while the CJK half stays mojibake. A decoded structure with
// mojibake in it still parses into the right release, groups with its siblings,
// and gets indexed; failing keeps the =?…?= wrapper, which is exactly the shape
// the junk engine kills. Approximate beats absent.
//
// The ISO-2022 family and UTF-7 are the exception and are refused: they are
// stateful, encoding non-ASCII by shifting modes with ESC sequences. Passing
// those through emits raw control bytes into the title, and measuring it showed
// the damage runs the wrong way — a subject the junk engine had accepted in its
// encoded form became high_special_chars once "decoded". For those, keeping the
// raw header is strictly better.
func passthroughCharset(charset string, input io.Reader) (io.Reader, error) {
	c := strings.ToLower(charset)
	if strings.HasPrefix(c, "iso-2022") || strings.HasPrefix(c, "utf-7") || strings.HasPrefix(c, "hz-") {
		return nil, fmt.Errorf("stateful charset %q: keeping the raw header", charset)
	}
	return input, nil
}

// unmojibake reverses a subject whose encoded-word LIED about its charset.
//
// The lie is common and Go believes it: =?ISO-8859-1?Q?Espa=C3=B1ol?= carries
// UTF-8 bytes under a Latin-1 label, and mime.WordDecoder does exactly what it
// was told -- widen each byte to the code point of the same value. "EspaÃ
// ±ol" becomes "EspaÃ±ol", and a right single quote (E2 80 99)
// becomes "a": two C1 CONTROL CHARACTERS, which are forbidden in
// HTML. 2,008 rows in this demo's index carry them, and every one of them
// reverses -- see docs/BACKLOG.md item 9 in loon-demo-site.
//
// The reversal is the encode run backwards: narrow every rune to one byte,
// then read those bytes as UTF-8. It applies ONLY when all three hold --
//
//	every rune fits in a byte  (it cannot be an honest string with real
//	                            non-Latin-1 characters)
//	the bytes are valid UTF-8  (an honest Latin-1 string almost never is:
//	                            "Español" narrows to a lone 0xF1, invalid)
//	the result differs         (pure ASCII narrows to itself and is left)
//
// -- which leaves an honest =?UTF-8?= subject and an honest =?ISO-8859-1?=
// subject both untouched, verified by test.
//
// The residual false positive is a title that genuinely contains "Ã±"
// and means it. Such a title is mojibake somebody else made; there is no
// reading of it that this makes worse.
func unmojibake(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return s
		}
		b = append(b, byte(r))
	}
	if !utf8.Valid(b) || string(b) == s {
		return s
	}
	return string(b)
}

// decodeSubject expands encoded-words, returning the subject unchanged when
// there are none (the overwhelming majority — this sits on the per-article
// ingest path) or when decoding fails.
// decodeRaw8Bit reads a header that carries no encoded-words and is not valid
// UTF-8 as Windows-1252.
//
// Such a header is the poster's raw 8-bit bytes. Left alone they reach
// pgSafeText, which can only replace them with U+FFFD to make them storable --
// and that is destruction, not sanitisation: the byte is gone from the
// database and only a re-crawl could recover it. Measured on production
// 2026-08-26: 2,834 of 1,143,872 titles carried U+FFFD, still accruing (newest
// the day before), concentrated in the German audiobook and European music
// groups. "H<?>rbuch" is Hörbuch; ö is 0xF6. Two magazine titles wanted an en
// dash, 0x96 -- which is why this is CP1252 rather than Latin-1, where 0x96 is
// a control character.
//
// SAFE PRECISELY BECAUSE OF WHERE IT SITS. The one thing that must not be
// CP1252-decoded is a Shift_JIS/GBK encoded-word, which passthroughCharset
// hands back as invalid UTF-8 BY DESIGN -- read as CP1252 it would become
// mojibake, worse than the replacement character. Those subjects contain "=?"
// and leave through the branch below, never reaching here. This sees only
// headers with no encoded-words at all.
//
// A last guard: if the bytes were already valid UTF-8 this is not called, so a
// correct subject is never rewritten.
func decodeRaw8Bit(s string) string {
	out, err := charmap.Windows1252.NewDecoder().String(s)
	if err != nil || out == "" {
		return s
	}
	return out
}

func decodeSubject(s string) string {
	if !strings.Contains(s, "=?") {
		// No encoded-words: the poster's own bytes. If they are not UTF-8 they
		// are almost certainly CP1252, and decoding beats losing them.
		if !utf8.ValidString(s) {
			return decodeRaw8Bit(s)
		}
		return s
	}
	d, err := subjectDecoder.DecodeHeader(s)
	if err != nil || strings.TrimSpace(d) == "" {
		// Never trade a parseable raw subject for an empty or half-decoded one.
		return s
	}
	// Only what WE decoded. A raw subject arrives as the poster's own bytes and
	// may be mojibake they made; this reverses the widening this path performs.
	return unmojibake(d)
}
