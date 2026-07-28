package usenet

import (
	"fmt"
	"io"
	"mime"
	"strings"
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

// decodeSubject expands encoded-words, returning the subject unchanged when
// there are none (the overwhelming majority — this sits on the per-article
// ingest path) or when decoding fails.
func decodeSubject(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	d, err := subjectDecoder.DecodeHeader(s)
	if err != nil || strings.TrimSpace(d) == "" {
		// Never trade a parseable raw subject for an empty or half-decoded one.
		return s
	}
	return d
}
